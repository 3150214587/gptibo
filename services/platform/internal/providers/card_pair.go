package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"gptibo/platform/internal/commerce"
)

func (e *Engine) lockCardRule(ctx context.Context, o commerce.Order, s commerce.Snapshot) error {
	return e.Store.Tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", "rule-config:"+s.Rule.Business+":"+s.Rule.ProductCode); err != nil {
			return err
		}
		resource := "card-rule:" + s.Rule.ID
		if _, err := tx.Exec(ctx, "INSERT INTO resource_locks(resource,order_id) VALUES($1,$2) ON CONFLICT DO NOTHING", resource, o.ID); err != nil {
			return err
		}
		var owner string
		if err := tx.QueryRow(ctx, "SELECT order_id FROM resource_locks WHERE resource=$1", resource).Scan(&owner); err != nil {
			return err
		}
		if owner != o.ID {
			return &UpstreamError{"resource_busy", false}
		}
		return nil
	})
}

func (e *Engine) selectPairCard(ctx context.Context, o commerce.Order, s commerce.Snapshot, j *Job, save func() error) error {
	if len(j.PoolCards) == 0 {
		rows, err := e.Store.DB.Query(ctx, "SELECT card_id FROM card_pool_slots WHERE rule_id=$1 AND environment=$2 AND card_product=$3 ORDER BY slot", s.Rule.ID, s.Provider.Environment, s.Rule.CardProduct)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			j.PoolCards = append(j.PoolCards, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(j.PoolCards) != 2 {
			return &UpstreamError{"card_pair_unconfigured", false}
		}
		if err = save(); err != nil {
			return err
		}
	}
	if j.CardID > 0 {
		return nil
	}
	for _, id := range j.PoolCards {
		var usable bool
		err := e.Store.DB.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM card_retirements WHERE environment=$1 AND card_id=$2)
   AND NOT EXISTS(SELECT 1 FROM card_declines WHERE environment=$1 AND card_id=$2 AND order_id=$3)
   AND NOT EXISTS(SELECT 1 FROM resource_locks WHERE resource='card:'||$1||':'||$2::text AND order_id<>$3)
   AND EXISTS(SELECT 1 FROM card_pool_slots WHERE environment=$1 AND card_id=$2 AND rule_id=$4 AND card_product=$5)`, s.Provider.Environment, id, o.ID, s.Rule.ID, s.Rule.CardProduct).Scan(&usable)
		if err != nil {
			return err
		}
		if usable {
			j.CardID = id
			return save()
		}
	}
	if j.CardAttempt == 0 {
		// No card was attempted; a replacement may replenish a slot before retry.
		j.PoolCards = nil
		if err := save(); err != nil {
			return err
		}
		return &UpstreamError{"resource_busy", false}
	}
	return &UpstreamError{"card_pair_unavailable", false}
}

func (e *Engine) declinePairCard(ctx context.Context, o commerce.Order, s commerce.Snapshot, j *Job, save func() error) (Result, error) {
	if len(j.PoolCards) != 2 || j.CardID != j.PoolCards[0] && j.CardID != j.PoolCards[1] {
		return Result{}, &UpstreamError{"card_pair_unconfirmed", false}
	}
	err := e.Store.Tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", fmt.Sprintf("decline:%s:%d", s.Provider.Environment, j.CardID)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "INSERT INTO card_declines(environment,card_id,order_id,intent_id,method_id,reason) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING", s.Provider.Environment, j.CardID, o.ID, j.DeclinedIntent, j.DeclinedMethod, j.DeclineCode); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM card_declines WHERE environment=$1 AND card_id=$2", s.Provider.Environment, j.CardID).Scan(&count); err != nil {
			return err
		}
		if count >= 2 {
			if _, err := tx.Exec(ctx, "INSERT INTO card_retirements(environment,card_id,order_id,reason) VALUES($1,$2,$3,'two_declines') ON CONFLICT DO NOTHING", s.Provider.Environment, j.CardID, o.ID); err != nil {
				return err
			}
			data, _ := json.Marshal(s)
			cipher := e.Store.Vault.Seal(replacementPurpose(s.Provider.Environment, j.CardID), data)
			if _, err := tx.Exec(ctx, `INSERT INTO card_replacements(environment,old_card_id,rule_id,slot,order_id,config_cipher)
    SELECT environment,card_id,rule_id,slot,$3,$4 FROM card_pool_slots WHERE environment=$1 AND card_id=$2 AND rule_id=$5
    ON CONFLICT DO NOTHING`, s.Provider.Environment, j.CardID, o.ID, cipher, s.Rule.ID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	// Both confirmed-unpaid attempts stop. Keep recipient locks and frozen supply
	// funds for review, but let unrelated orders use the account and healthy cards.
	other := j.PoolCards[0]
	if other == j.CardID {
		other = j.PoolCards[1]
	}
	var otherReady bool
	err = e.Store.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM card_pool_slots WHERE rule_id=$1 AND environment=$2 AND card_id=$3)
 AND NOT EXISTS(SELECT 1 FROM card_retirements WHERE environment=$2 AND card_id=$3)`, s.Rule.ID, s.Provider.Environment, other).Scan(&otherReady)
	if err != nil {
		return Result{}, err
	}
	if j.CardAttempt >= 1 || !otherReady {
		_, err = e.Store.DB.Exec(ctx, "DELETE FROM resource_locks WHERE order_id=$1 AND (resource LIKE 'card:%' OR resource LIKE 'card-rule:%' OR resource LIKE 'x-account:%')", o.ID)
		return Result{Status: "unknown", Stage: "declined", Failure: "card_pair_declined"}, err
	}
	j.CardAttempt++
	j.CardID = 0
	j.Method = ""
	j.Stage = "card"
	if err = save(); err != nil {
		return Result{}, err
	}
	return Result{Status: "running", Stage: j.Stage, Failure: "card_switching_backup"}, nil
}

func replacementPurpose(environment string, id int64) string {
	return fmt.Sprintf("card-replacement:%s:%d", environment, id)
}

// Check the provider's actual card identity before allowing a pool assignment.
func (e *Engine) ValidatePair(ctx context.Context, r commerce.CardRule) (map[int64]string, error) {
	p, err := (&commerce.Service{Store: e.Store}).Provider(ctx)
	if err != nil {
		return nil, err
	}
	result := map[int64]string{}
	for _, id := range []int64{r.CardID, r.BackupCardID} {
		data, err := cardAPI(ctx, p, "GET", fmt.Sprintf("/cards/%d?sync=1", id), nil, "")
		if err != nil {
			return nil, commerce.Fail("card_query_failed", "无法核对主备卡，请稍后重试", 502)
		}
		c := obj(data["data"])
		pan := str(c["card_number"])
		if !matchesID(c["id"], id) || str(c["product_code"]) != r.CardProduct || str(c["status"]) != "ACTIVE" || len(pan) < 4 || !cardLimitsReady(c, r) {
			return nil, commerce.Fail("card_pair_invalid", "主备卡须为所选卡头的有效卡片", 409)
		}
		var unavailable bool
		if err = e.Store.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM card_retirements WHERE environment=$1 AND card_id=$2)
   OR EXISTS(SELECT 1 FROM card_bindings WHERE environment=$1 AND card_id=$2 AND business<>'x')`, p.Environment, id).Scan(&unavailable); err != nil {
			return nil, err
		}
		if unavailable {
			return nil, commerce.Fail("card_pair_invalid", "已停用或用于其他业务的卡不能加入 X 主备卡", 409)
		}
		result[id] = pan[len(pan)-4:]
	}
	return result, nil
}
