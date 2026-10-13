package management

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"gptibo/storefront/internal/auth"
	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/finance"
	"gptibo/storefront/internal/security"
)

const paidRefundHold = `EXISTS(SELECT 1 FROM partner_earning_orders h JOIN partner_payouts hp ON hp.id=h.payout_id LEFT JOIN recharge_orders t ON t.order_id=h.id WHERE h.partner_id=$1 AND hp.status='paid' AND (h.refund_hold=1 OR (h.payment_status='paid' AND t.financial_hold=1)))`

func financialLock(ctx context.Context, tx pgx.Tx) error {
	_, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(903500)`)
	return e
}
func (s Service) verifyPayout(ctx context.Context, partner, payout string) (map[string]int64, error) {
	list, e := rows(ctx, s.Store.DB, `SELECT o.id FROM partner_earning_orders o JOIN recharge_orders t ON t.order_id=o.id WHERE ($2<>'' AND o.payout_id=$2) OR ($2='' AND o.partner_id=$1 AND o.payout_id IS NULL AND o.payment_status='paid' AND o.status='success' AND o.refund_hold=0 AND t.financial_hold=0)`, partner, payout)
	if e != nil {
		return nil, e
	}
	verified := map[string]int64{}
	if len(list) > 0 && s.VerifySupplier == nil {
		return nil, fault.New(503, "请先配置并核对原供应商订单，暂不能结算。")
	}
	for _, item := range list {
		id := item["id"].(string)
		if e = s.VerifySupplier(ctx, id); e != nil {
			return nil, e
		}
		var version int64
		e = s.Store.DB.QueryRow(ctx, `SELECT version FROM orders WHERE id=$1`, id).Scan(&version)
		if e != nil {
			return nil, e
		}
		verified[id] = version
	}
	return verified, nil
}

type PayoutInput struct {
	AccountVersion *int64 `json:"accountVersion"`
	Key            string `json:"key"`
}

func (s Service) RequestPayout(ctx context.Context, token string, in PayoutInput) (bool, error) {
	if !security.ValidID(in.Key) {
		return false, fault.New(400, "请求编号无效。")
	}
	p, e := s.Auth.Require(ctx, s.Store.DB, "partner", token)
	if e != nil {
		return false, e
	}
	// A successful retry does not depend on supplier availability.
	var recipient string
	e = s.Store.DB.QueryRow(ctx, `SELECT recipient FROM partner_payouts WHERE partner_id=$1 AND request_key=$2`, p.Subject, in.Key).Scan(&recipient)
	if e == nil {
		return false, nil
	}
	if e != pgx.ErrNoRows {
		return false, e
	}
	if _, e = payoutRecipient(ctx, s.Store.DB, p.Subject, in.AccountVersion, false); e != nil {
		return false, e
	}
	verified, e := s.verifyPayout(ctx, p.Subject, "")
	if e != nil {
		return false, e
	}
	created := false
	e = s.write(ctx, "partner", token, func(tx pgx.Tx, p auth.Principal) error {
		if e := financialLock(ctx, tx); e != nil {
			return e
		}
		var recipient string
		e := tx.QueryRow(ctx, `SELECT recipient FROM partner_payouts WHERE partner_id=$1 AND request_key=$2`, p.Subject, in.Key).Scan(&recipient)
		if e == nil {
			return nil
		}
		if e != pgx.ErrNoRows {
			return e
		}
		recipient, e = payoutRecipient(ctx, tx, p.Subject, in.AccountVersion, true)
		if e != nil {
			return e
		}
		var blocked bool
		now := time.Now().UnixMilli()
		e = tx.QueryRow(ctx, `SELECT `+paidRefundHold+` OR EXISTS(SELECT 1 FROM partner_payouts WHERE partner_id=$1 AND (status='pending' OR (cooldown_exempt=0 AND created_at>$2)))`, p.Subject, now-86400000).Scan(&blocked)
		if e != nil {
			return e
		}
		if blocked {
			return fault.New(409, "存在待处理提现、24 小时冷却或已结算订单的售后冻结。")
		}
		candidates, e := rows(ctx, tx, `SELECT o.id,o.version,(`+finance.Profit("o")+`) profit_cents,EXISTS(SELECT 1 FROM recharge_orders t WHERE t.order_id=o.id) supplier FROM partner_earning_orders o JOIN orders locked ON locked.id=o.id WHERE o.partner_id=$1 AND o.payout_id IS NULL AND `+finance.Eligible("o")+` ORDER BY o.id FOR UPDATE OF locked`, p.Subject)
		if e != nil {
			return e
		}
		var gross int64
		ids := []string{}
		for _, item := range candidates {
			id := item["id"].(string)
			if item["supplier"] == true {
				v, ok := verified[id]
				if !ok || v != item["version"].(int64) {
					return conflict()
				}
			}
			gross += item["profit_cents"].(int64)
			ids = append(ids, id)
		}
		debts, e := rows(ctx, tx, `SELECT id,amount_cents FROM partner_debits WHERE partner_id=$1 AND payout_id IS NULL ORDER BY id FOR UPDATE`, p.Subject)
		if e != nil {
			return e
		}
		var deduction int64
		for _, d := range debts {
			deduction += d["amount_cents"].(int64)
		}
		if gross-deduction < 1000 {
			return fault.New(409, "可提现净额需满 ¥10。")
		}
		id := security.ID()
		_, e = tx.Exec(ctx, `INSERT INTO partner_payouts(id,partner_id,amount_cents,gross_cents,deduction_cents,recipient,created_at,request_key) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, p.Subject, gross-deduction, gross, deduction, recipient, now, in.Key)
		if e != nil {
			return e
		}
		for _, o := range candidates {
			_, e = tx.Exec(ctx, `INSERT INTO partner_payout_items(payout_id,order_id,profit_cents) VALUES($1,$2,$3)`, id, o["id"], o["profit_cents"])
			if e != nil {
				return e
			}
		}
		if _, e = tx.Exec(ctx, `UPDATE orders SET payout_id=CASE WHEN partner_id=$3 THEN $1 ELSE payout_id END,upstream_payout_id=CASE WHEN upstream_partner_id=$3 THEN $1 ELSE upstream_payout_id END WHERE id=ANY($2::text[])`, id, ids, p.Subject); e != nil {
			return e
		}
		for _, d := range debts {
			if _, e = tx.Exec(ctx, `INSERT INTO partner_payout_debit_items(payout_id,debit_id,amount_cents) VALUES($1,$2,$3)`, id, d["id"], d["amount_cents"]); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `UPDATE partner_debits SET payout_id=$1 WHERE id=$2`, id, d["id"]); e != nil {
				return e
			}
		}
		if _, e = tx.Exec(ctx, `INSERT INTO payout_notifications(payout_id,created_at) VALUES($1,$2)`, id, now); e != nil {
			return e
		}
		created = true
		return audit(ctx, tx, p, "payout-request", p.Subject, map[string]any{"payout": id, "grossCents": gross, "deductionCents": deduction, "amountCents": gross - deduction, "orderIds": ids})
	})
	return created, e
}
func (s Service) PayoutList(ctx context.Context, page int) (map[string]any, error) {
	if page < 1 || page > 10000 {
		return nil, fault.New(400, "页码无效。")
	}
	tx, e := s.Store.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	list, e := rows(ctx, tx, `SELECT ps.*,p.name partner_name FROM partner_payouts ps JOIN partners p ON p.id=ps.partner_id ORDER BY CASE WHEN ps.status='pending' THEN 0 ELSE 1 END,ps.created_at DESC,ps.id DESC LIMIT 20 OFFSET $1`, (page-1)*20)
	if e != nil {
		return nil, e
	}
	var total int64
	e = tx.QueryRow(ctx, `SELECT count(*) FROM partner_payouts`).Scan(&total)
	if e != nil {
		return nil, e
	}
	return map[string]any{"rows": list, "total": total, "page": page}, tx.Commit(ctx)
}
func (s Service) PayoutDetail(ctx context.Context, id string, page int) (map[string]any, error) {
	if page < 1 || page > 10000 {
		return nil, fault.New(400, "页码无效。")
	}
	tx, e := s.Store.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	p, e := rows(ctx, tx, `SELECT * FROM partner_payouts WHERE id=$1`, id)
	if e != nil {
		return nil, e
	}
	if len(p) == 0 {
		return nil, fault.New(404, "提现不存在。")
	}
	items, e := rows(ctx, tx, `SELECT i.order_id,i.profit_cents,o.plan_name FROM partner_payout_items i JOIN partner_payouts ps ON ps.id=i.payout_id JOIN partner_earning_orders o ON o.id=i.order_id AND o.partner_id=ps.partner_id JOIN orders locked ON locked.id=o.id WHERE i.payout_id=$1 ORDER BY o.created_at,o.id LIMIT 20 OFFSET $2`, id, (page-1)*20)
	if e != nil {
		return nil, e
	}
	deductions, e := rows(ctx, tx, `SELECT i.amount_cents,r.order_id,dc.hostname domain_hostname FROM partner_payout_debit_items i JOIN partner_debits d ON d.id=i.debit_id LEFT JOIN refund_cases r ON r.id=d.refund_id LEFT JOIN domain_charges dc ON dc.id=d.domain_charge_id WHERE i.payout_id=$1`, id)
	if e != nil {
		return nil, e
	}
	var total int64
	e = tx.QueryRow(ctx, `SELECT count(*) FROM partner_payout_items WHERE payout_id=$1`, id).Scan(&total)
	if e != nil {
		return nil, e
	}
	return map[string]any{"payout": p[0], "items": items, "deductions": deductions, "total": total, "page": page}, tx.Commit(ctx)
}

type HandlePayoutInput struct {
	Action    string `json:"action"`
	Version   *int64 `json:"version"`
	Note      string `json:"note"`
	Reference string `json:"reference"`
}

func (s Service) HandlePayout(ctx context.Context, token, id string, in HandlePayoutInput) error {
	if in.Version == nil || *in.Version < 0 || strings.TrimSpace(in.Note) == "" || !validText(in.Note, 300) || (in.Action != "approve" && in.Action != "paid" && in.Action != "rejected") {
		return fault.New(400, "处理方式、版本或说明无效。")
	}
	if in.Action == "paid" && (strings.TrimSpace(in.Reference) == "" || !validText(in.Reference, 100)) {
		return fault.New(400, "请填写实际转账流水号。")
	}
	var verified map[string]int64
	if in.Action == "approve" {
		if _, e := s.Auth.Require(ctx, s.Store.DB, "admin", token); e != nil {
			return e
		}
		var e error
		verified, e = s.verifyPayout(ctx, "", id)
		if e != nil {
			return e
		}
	}
	return s.write(ctx, "admin", token, func(tx pgx.Tx, p auth.Principal) error {
		if e := financialLock(ctx, tx); e != nil {
			return e
		}
		before, e := rows(ctx, tx, `SELECT * FROM partner_payouts WHERE id=$1 FOR UPDATE`, id)
		if e != nil {
			return e
		}
		if len(before) != 1 || before[0]["status"] != "pending" || before[0]["version"] != *in.Version {
			return conflict()
		}
		ps := before[0]
		now := time.Now().UnixMilli()
		if in.Action == "approve" {
			if ps["reviewed_at"] != nil {
				return conflict()
			}
			items, e := rows(ctx, tx, `SELECT o.id,o.version,i.profit_cents,(`+finance.Profit("o")+`) actual_profit,(`+finance.Eligible("o")+`) eligible,o.payout_id,EXISTS(SELECT 1 FROM recharge_orders WHERE order_id=o.id) supplier FROM partner_payout_items i JOIN partner_payouts ps ON ps.id=i.payout_id JOIN partner_earning_orders o ON o.id=i.order_id AND o.partner_id=ps.partner_id JOIN orders locked ON locked.id=o.id WHERE i.payout_id=$1 ORDER BY o.id FOR UPDATE OF locked`, id)
			if e != nil {
				return e
			}
			if len(items) == 0 {
				return conflict()
			}
			for _, item := range items {
				if item["eligible"] != true || item["profit_cents"] != item["actual_profit"] || item["payout_id"] != id {
					return conflict()
				}
				if item["supplier"] == true {
					v, ok := verified[item["id"].(string)]
					if !ok || v != item["version"].(int64) {
						return conflict()
					}
				}
			}
			var held, outstandingDebits bool
			e = tx.QueryRow(ctx, `SELECT `+paidRefundHold+`, EXISTS(SELECT 1 FROM partner_debits WHERE partner_id=$1 AND payout_id IS NULL)`, ps["partner_id"]).Scan(&held, &outstandingDebits)
			if e != nil {
				return e
			}
			if held {
				return fault.New(409, "已结算订单存在售后或资金冻结，暂不能审核提现。请先核对相关订单；若已实际打款，请联系平台核对并补登记，勿重复转账。")
			}
			if outstandingDebits {
				return fault.New(409, "提现申请后新增了待抵扣款项，原申请金额需要重新核算。尚未打款请退回申请后重新申请；若已实际打款，请联系平台核对并补登记，勿重复转账。")
			}
			_, e = tx.Exec(ctx, `UPDATE partner_payouts SET review_token=$1,reviewed_at=$2,note=$3,version=version+1 WHERE id=$4`, security.ID(), now, strings.TrimSpace(in.Note), id)
		} else {
			if in.Action == "paid" && ps["reviewed_at"] == nil {
				return fault.New(409, "请先审核通过，再登记真实转账结果。")
			}
			_, e = tx.Exec(ctx, `UPDATE partner_payouts SET status=$1,note=$2,reference=$3,handled_at=$4,version=version+1 WHERE id=$5`, in.Action, strings.TrimSpace(in.Note), strings.TrimSpace(in.Reference), now, id)
			if e != nil {
				return e
			}
			// A reviewed transfer can already have happened externally. Record its actual
			// outcome even if a refund arrived meanwhile; refund execution waits for it.
			if in.Action == "rejected" {
				if _, e = tx.Exec(ctx, `UPDATE partner_debits SET payout_id=NULL WHERE payout_id=$1`, id); e != nil {
					return e
				}
				_, e = tx.Exec(ctx, `UPDATE orders SET payout_id=CASE WHEN payout_id=$1 THEN NULL ELSE payout_id END,upstream_payout_id=CASE WHEN upstream_payout_id=$1 THEN NULL ELSE upstream_payout_id END WHERE payout_id=$1 OR upstream_payout_id=$1`, id)
			}
		}
		if e != nil {
			return e
		}
		after, e := rows(ctx, tx, `SELECT * FROM partner_payouts WHERE id=$1`, id)
		if e != nil {
			return e
		}
		return audit(ctx, tx, p, "payout-"+in.Action, id, map[string]any{"note": in.Note, "before": ps, "after": after[0]})
	})
}

type ResetCooldownInput struct {
	Key  string `json:"key"`
	Note string `json:"note"`
}

func (s Service) ResetCooldown(ctx context.Context, token, partner string, in ResetCooldownInput) (map[string]any, error) {
	if !security.ValidID(in.Key) || strings.TrimSpace(in.Note) == "" || !validText(in.Note, 500) {
		return nil, fault.New(400, "请填写有效请求编号与重置说明。")
	}
	out := map[string]any{}
	e := s.write(ctx, "admin", token, func(tx pgx.Tx, p auth.Principal) error {
		if e := financialLock(ctx, tx); e != nil {
			return e
		}
		payload, _ := json.Marshal(map[string]string{"partner": partner, "note": strings.TrimSpace(in.Note)})
		digest := security.Hash(string(payload))
		var actor, action, hash string
		var saved map[string]any
		e := tx.QueryRow(ctx, `SELECT actor,action,payload_hash,result FROM operation_receipts WHERE id=$1`, in.Key).Scan(&actor, &action, &hash, &saved)
		if e == nil {
			if actor != p.Subject || action != "payout-cooldown-reset" || hash != digest {
				return conflict()
			}
			out = saved
			return nil
		}
		if e != pgx.ErrNoRows {
			return e
		}
		if partner != "" {
			var exists bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM partners WHERE id=$1)`, partner).Scan(&exists); e != nil {
				return e
			}
			if !exists {
				return fault.New(404, "代理不存在。")
			}
		}
		affected, e := rows(ctx, tx, `UPDATE partner_payouts SET cooldown_exempt=1 WHERE cooldown_exempt=0 AND ($1='' OR partner_id=$1) RETURNING id,partner_id`, partner)
		if e != nil {
			return e
		}
		partners := map[string]bool{}
		for _, r := range affected {
			partners[r["partner_id"].(string)] = true
		}
		out = map[string]any{"ok": true, "resetPayouts": len(affected), "resetPartners": len(partners)}
		result, _ := json.Marshal(out)
		if _, e = tx.Exec(ctx, `INSERT INTO operation_receipts(id,actor,action,payload_hash,result) VALUES($1,$2,'payout-cooldown-reset',$3,$4)`, in.Key, p.Subject, digest, result); e != nil {
			return e
		}
		return audit(ctx, tx, p, "payout-cooldown-reset", partner, map[string]any{"key": in.Key, "note": in.Note, "payouts": affected})
	})
	return out, e
}
