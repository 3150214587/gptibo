package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"gptibo/platform/internal/commerce"
	"gptibo/platform/internal/store"
	"time"
)

// Manual evidence is required because the reference API has no reliable operation lookup by request id.
// This records the original result. It never repeats card funding or recharge submission.
func (e *Engine) ResolveCardOperation(ctx context.Context, actor string, r commerce.CardResolution) error {
	if r.Confirmation != "RESOLVE" || (r.Status != "succeeded" && r.Status != "failed") || len(r.Note) < 10 || len(r.Note) > 500 {
		return commerce.Fail("invalid_resolution", "填写卡台原操作凭证和至少 10 字说明，并输入 RESOLVE", 400)
	}
	var order, operation, status, configCipher string
	var oldCard, amount int64
	err := e.Store.DB.QueryRow(ctx, "SELECT c.order_id,c.operation,c.status,c.card_id,c.amount_minor,o.config_cipher FROM card_operations c JOIN orders o ON o.id=c.order_id WHERE c.reference=$1", r.Reference).Scan(&order, &operation, &status, &oldCard, &amount, &configCipher)
	if errors.Is(err, pgx.ErrNoRows) {
		return commerce.Fail("operation_missing", "卡操作不存在", 404)
	}
	if err != nil {
		return err
	}
	var replacementEnv, replacementCipher string
	var replacementCard int64
	replacementErr := e.Store.DB.QueryRow(ctx, "SELECT environment,old_card_id,config_cipher FROM card_replacements WHERE order_id=$1 AND $2=order_id||':card:'||(100+slot)::text AND status='opening'", order, r.Reference).Scan(&replacementEnv, &replacementCard, &replacementCipher)
	if replacementErr != nil && !errors.Is(replacementErr, pgx.ErrNoRows) {
		return replacementErr
	}
	maintenance := replacementErr == nil
	var snap commerce.Snapshot
	purpose := "order:" + order
	if maintenance {
		purpose = replacementPurpose(replacementEnv, replacementCard)
		configCipher = replacementCipher
	}
	data, err := e.Store.Vault.Open(purpose, configCipher)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(data, &snap); err != nil {
		return err
	}
	card := oldCard
	if operation == "open" && r.Status == "succeeded" {
		card = r.CardID
	}
	if r.Status == "succeeded" {
		if card <= 0 || operation == "fund" && r.CardID != oldCard {
			return commerce.Fail("card_mismatch", "只能登记原操作对应的卡 ID", 400)
		}
		resp, err := cardAPI(ctx, snap.Provider, "GET", fmt.Sprintf("/cards/%d?sync=1", card), nil, "")
		if err != nil {
			return commerce.Fail("card_query_failed", "卡台查询未确认，不能登记成功", 502)
		}
		row := obj(resp["data"])
		balance, valid := usdBalanceMinor(row["available_amount"])
		if checkedCardID(row, card) != nil || str(row["product_code"]) != snap.Rule.CardProduct || str(row["status"]) != "ACTIVE" || !cardLimitsReady(row, snap.Rule) || !valid || balance < amount {
			return commerce.Fail("card_evidence_mismatch", "卡 ID、卡头、限额或余额不能证明所登记的原操作", 409)
		}
	}
	return e.Store.Tx(ctx, func(tx pgx.Tx) error {
		var currentStage, orderStatus string
		var lease *time.Time
		if err := tx.QueryRow(ctx, "SELECT stage,status,lease_until FROM orders WHERE id=$1 FOR UPDATE", order).Scan(&currentStage, &orderStatus, &lease); err != nil {
			return err
		}
		if !maintenance && (orderStatus == "succeeded" || orderStatus == "failed") || lease != nil && lease.After(time.Now()) {
			return commerce.Fail("operation_busy", "订单已终止或正在执行，请等待本次执行结束后核对", 409)
		}
		if maintenance {
			var state string
			if err := tx.QueryRow(ctx, "SELECT status,lease_until FROM card_replacements WHERE environment=$1 AND old_card_id=$2 FOR UPDATE", replacementEnv, replacementCard).Scan(&state, &lease); err != nil {
				return err
			}
			if state != "opening" || lease != nil && lease.After(time.Now()) {
				return commerce.Fail("operation_busy", "换卡任务正在执行或已结束，请稍后核对", 409)
			}
		} else if currentStage != "funding" && currentStage != "card" {
			return commerce.Fail("operation_stage", "当前阶段不能登记卡操作结果", 409)
		}
		var current, oldNote string
		var currentCard int64
		if err := tx.QueryRow(ctx, "SELECT status,card_id,resolution_note FROM card_operations WHERE reference=$1 FOR UPDATE", r.Reference).Scan(&current, &currentCard, &oldNote); err != nil {
			return err
		}
		if current == "succeeded" || current == "failed" {
			if current == r.Status && currentCard == card && oldNote == r.Note {
				return nil
			}
			return commerce.Fail("operation_resolved", "原操作已核对，不能改写结果", 409)
		}
		if r.Status == "succeeded" {
			if _, err := tx.Exec(ctx, "INSERT INTO card_bindings(environment,card_id,business) VALUES($1,$2,$3) ON CONFLICT DO NOTHING", snap.Provider.Environment, card, snap.Business); err != nil {
				return err
			}
			var owner string
			if err := tx.QueryRow(ctx, "SELECT business FROM card_bindings WHERE environment=$1 AND card_id=$2", snap.Provider.Environment, card).Scan(&owner); err != nil {
				return err
			}
			if owner != snap.Business {
				return commerce.Fail("card_business_mismatch", "此卡属于其他业务", 409)
			}
			resource := fmt.Sprintf("card:%s:%d", snap.Provider.Environment, card)
			if _, err := tx.Exec(ctx, "INSERT INTO resource_locks(resource,order_id) VALUES($1,$2) ON CONFLICT DO NOTHING", resource, order); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, "SELECT order_id FROM resource_locks WHERE resource=$1", resource).Scan(&owner); err != nil {
				return err
			}
			if owner != order {
				return commerce.Fail("card_busy", "此卡正被其他订单占用", 409)
			}
		}
		if _, err := tx.Exec(ctx, "UPDATE card_operations SET status=$2,card_id=$3,resolution_note=$4 WHERE reference=$1", r.Reference, r.Status, card, r.Note); err != nil {
			return err
		}
		if maintenance {
			if _, err := tx.Exec(ctx, "UPDATE card_replacements SET next_at=now() WHERE environment=$1 AND old_card_id=$2", replacementEnv, replacementCard); err != nil {
				return err
			}
		} else {
			if _, err := tx.Exec(ctx, "UPDATE orders SET next_at=now() WHERE id=$1", order); err != nil {
				return err
			}
		}
		return store.Audit(ctx, tx, actor, "resolve_card_original", r.Reference, r.Status+": "+r.Note)
	})
}
