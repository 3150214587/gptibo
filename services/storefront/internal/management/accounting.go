package management

import (
	"context"
	"github.com/jackc/pgx/v5"
	"gptibo/storefront/internal/auth"
	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/security"
	"time"
)

const accountingTotals = `count(*) FILTER(WHERE kind='order') order_count,count(*) FILTER(WHERE kind='refund') adjustment_count,COALESCE(sum(sale_cents),0)::bigint sale_cents,COALESCE(sum(cost_cents),0)::bigint cost_cents,COALESCE(sum(refund_cents),0)::bigint refund_cents,COALESCE(sum(sale_cents-cost_cents-refund_cents),0)::bigint profit_cents,count(*) FILTER(WHERE cost_cents IS NULL) missing_cost_count`

func (s Service) Accounting(ctx context.Context, page int) (map[string]any, error) {
	if page < 1 || page > 10000 {
		return nil, fault.New(400, "页码无效。")
	}
	tx, e := s.Store.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	var version int64
	var closed *int64
	e = tx.QueryRow(ctx, `SELECT s.version,a.closed_at FROM accounting_state s LEFT JOIN accounting_settlements a ON a.id=s.last_settlement_id WHERE s.id=1`).Scan(&version, &closed)
	if e != nil {
		return nil, e
	}
	totals, e := rows(ctx, tx, `SELECT `+accountingTotals+`,min(created_at) first_at FROM accounting_candidates`)
	if e != nil {
		return nil, e
	}
	groups, e := rows(ctx, tx, `SELECT plan,max(plan_name) plan_name,`+accountingTotals+` FROM accounting_candidates GROUP BY plan ORDER BY plan`)
	if e != nil {
		return nil, e
	}
	costs, e := rows(ctx, tx, `SELECT p.id plan,p.name,c.cost_cents FROM catalog_plans p LEFT JOIN accounting_costs c ON c.plan=p.id ORDER BY p.sort_order,p.id`)
	if e != nil {
		return nil, e
	}
	history, e := rows(ctx, tx, `SELECT * FROM accounting_settlements ORDER BY closed_at DESC,base_version DESC LIMIT 10 OFFSET $1`, (page-1)*10)
	if e != nil {
		return nil, e
	}
	var total, held int64
	e = tx.QueryRow(ctx, `SELECT count(*) FROM accounting_settlements`).Scan(&total)
	if e != nil {
		return nil, e
	}
	e = tx.QueryRow(ctx, `SELECT count(*) FROM orders o LEFT JOIN recharge_orders t ON t.order_id=o.id WHERE o.mode='live' AND o.is_test=0 AND o.payment_status='paid' AND (o.refund_hold=1 OR COALESCE(t.financial_hold,0)=1) AND (o.status='success' OR EXISTS(SELECT 1 FROM accounting_items i WHERE i.order_id=o.id))`).Scan(&held)
	if e != nil {
		return nil, e
	}
	var from any = closed
	if closed == nil {
		from = totals[0]["first_at"]
	}
	return map[string]any{"version": version, "fromAt": from, "asOf": time.Now().UnixMilli(), "totals": totals[0], "groups": groups, "costs": costs, "history": history, "historyTotal": total, "heldCount": held, "page": page}, tx.Commit(ctx)
}

type AccountingCost struct {
	Plan string `json:"plan"`
	Cost *int64 `json:"cost_cents"`
}
type AccountingCostsInput struct {
	Version *int64           `json:"version"`
	Costs   []AccountingCost `json:"costs"`
}

func (s Service) SaveAccountingCosts(ctx context.Context, token string, in AccountingCostsInput) error {
	if in.Version == nil || *in.Version < 0 || in.Costs == nil {
		return fault.New(400, "成本设置无效。")
	}
	return s.writeHierarchy(ctx, "admin", token, func(tx pgx.Tx, p auth.Principal) error {
		if e := financialLock(ctx, tx); e != nil {
			return e
		}
		var version int64
		if e := tx.QueryRow(ctx, `SELECT version FROM accounting_state WHERE id=1 FOR UPDATE`).Scan(&version); e != nil {
			return e
		}
		if version != *in.Version {
			return conflict()
		}
		plans, e := rows(ctx, tx, `SELECT id FROM catalog_plans`)
		if e != nil {
			return e
		}
		valid := map[string]bool{}
		for _, r := range plans {
			valid[r["id"].(string)] = true
		}
		if len(in.Costs) != len(valid) {
			return fault.New(400, "请提交所有套餐的采购成本。")
		}
		for _, c := range in.Costs {
			if !valid[c.Plan] || (c.Cost != nil && (*c.Cost < 0 || *c.Cost > 10000000)) {
				return fault.New(400, "采购成本或套餐无效。")
			}
			delete(valid, c.Plan)
		}
		before, e := rows(ctx, tx, `SELECT plan,cost_cents FROM accounting_costs ORDER BY plan`)
		if e != nil {
			return e
		}
		for _, c := range in.Costs {
			if _, e = tx.Exec(ctx, `INSERT INTO accounting_costs(plan,cost_cents) VALUES($1,$2) ON CONFLICT(plan) DO UPDATE SET cost_cents=excluded.cost_cents`, c.Plan, c.Cost); e != nil {
				return e
			}
		}
		if _, e = tx.Exec(ctx, `UPDATE accounting_state SET version=version+1 WHERE id=1`); e != nil {
			return e
		}
		return audit(ctx, tx, p, "accounting-costs", "accounting", map[string]any{"before": before, "after": in.Costs})
	})
}

type CloseAccountingInput struct {
	Version *int64 `json:"version"`
	Key     string `json:"key"`
}

func (s Service) CloseAccounting(ctx context.Context, token string, in CloseAccountingInput) (map[string]any, bool, error) {
	if in.Version == nil || *in.Version < 0 || !security.ValidID(in.Key) {
		return nil, false, fault.New(400, "清账请求无效。")
	}
	var result map[string]any
	created := false
	e := s.write(ctx, "admin", token, func(tx pgx.Tx, p auth.Principal) error {
		if e := financialLock(ctx, tx); e != nil {
			return e
		}
		existing, e := rows(ctx, tx, `SELECT * FROM accounting_settlements WHERE id=$1`, in.Key)
		if e != nil {
			return e
		}
		if len(existing) > 0 {
			if existing[0]["base_version"] != *in.Version {
				return conflict()
			}
			result = existing[0]
			return nil
		}
		var version int64
		var previous *string
		var from *int64
		e = tx.QueryRow(ctx, `SELECT s.version,s.last_settlement_id,a.closed_at FROM accounting_state s LEFT JOIN accounting_settlements a ON a.id=s.last_settlement_id WHERE s.id=1 FOR UPDATE OF s`).Scan(&version, &previous, &from)
		if e != nil {
			return e
		}
		if version != *in.Version {
			return conflict()
		}
		// Materialize the candidate snapshot exactly once. Later receipts and refunds
		// remain candidates for the next close; no second SELECT can expand this batch.
		candidates, e := rows(ctx, tx, `SELECT * FROM accounting_candidates ORDER BY order_id`)
		if e != nil {
			return e
		}
		if len(candidates) == 0 {
			return fault.New(409, "暂无可清账订单。")
		}
		var count, adjust, sales, costs, refunds int64
		first := time.Now().UnixMilli()
		for _, r := range candidates {
			if r["cost_cents"] == nil {
				return fault.New(409, "存在未填写的采购成本，请核对后清账。")
			}
			if r["kind"] == "order" {
				count++
			} else {
				adjust++
			}
			sales += r["sale_cents"].(int64)
			costs += r["cost_cents"].(int64)
			refunds += r["refund_cents"].(int64)
			first = min(first, r["created_at"].(int64))
		}
		if from == nil {
			from = &first
		}
		now := time.Now().UnixMilli()
		if *from > now {
			return conflict()
		}
		_, e = tx.Exec(ctx, `INSERT INTO accounting_settlements(id,base_version,previous_id,from_at,closed_at,order_count,adjustment_count,sale_cents,cost_cents,refund_cents,profit_cents) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, in.Key, version, previous, from, now, count, adjust, sales, costs, refunds, sales-costs-refunds)
		if e != nil {
			return e
		}
		for _, r := range candidates {
			_, e = tx.Exec(ctx, `INSERT INTO accounting_items(settlement_id,order_id,plan,plan_name,kind,sale_cents,cost_cents,refund_cents,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, in.Key, r["order_id"], r["plan"], r["plan_name"], r["kind"], r["sale_cents"], r["cost_cents"], r["refund_cents"], r["created_at"])
			if e != nil {
				return e
			}
		}
		if _, e = tx.Exec(ctx, `UPDATE accounting_state SET version=version+1,last_settlement_id=$1 WHERE id=1`, in.Key); e != nil {
			return e
		}
		list, e := rows(ctx, tx, `SELECT * FROM accounting_settlements WHERE id=$1`, in.Key)
		if e != nil {
			return e
		}
		result = list[0]
		created = true
		return audit(ctx, tx, p, "accounting-close", in.Key, result)
	})
	return map[string]any{"settlement": result}, created, e
}
func (s Service) AccountingDetail(ctx context.Context, id string, page int) (map[string]any, error) {
	if page < 1 || page > 10000 {
		return nil, fault.New(400, "页码无效。")
	}
	tx, e := s.Store.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	list, e := rows(ctx, tx, `SELECT * FROM accounting_settlements WHERE id=$1`, id)
	if e != nil {
		return nil, e
	}
	if len(list) == 0 {
		return nil, fault.New(404, "未找到清账记录。")
	}
	groups, e := rows(ctx, tx, `SELECT plan,max(plan_name) plan_name,`+accountingTotals+` FROM accounting_items WHERE settlement_id=$1 GROUP BY plan ORDER BY plan`, id)
	if e != nil {
		return nil, e
	}
	items, e := rows(ctx, tx, `SELECT * FROM accounting_items WHERE settlement_id=$1 ORDER BY created_at,order_id LIMIT 50 OFFSET $2`, id, (page-1)*50)
	if e != nil {
		return nil, e
	}
	return map[string]any{"settlement": list[0], "groups": groups, "items": items, "total": list[0]["order_count"].(int64) + list[0]["adjustment_count"].(int64), "page": page}, tx.Commit(ctx)
}
