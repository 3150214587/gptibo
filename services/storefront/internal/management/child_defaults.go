package management

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"gptibo/storefront/internal/auth"
	"gptibo/storefront/internal/fault"
)

// Defaults share the existing account supply quotes, including their revisions.
func (s Service) ChildDefaults(ctx context.Context, parent string) (map[string]any, error) {
	tx, e := s.Store.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	var top bool
	if e = tx.QueryRow(ctx, `SELECT parent_id IS NULL FROM partners WHERE id=$1`, parent).Scan(&top); e != nil {
		return nil, e
	}
	if !top {
		return nil, fault.New(403, "二级代理不能管理下级拿货价。")
	}
	plans, e := rows(ctx, tx, `SELECT c.id,c.product_id,c.name,p.cost_cents minimum_cost_cents,COALESCE(p.child_cost_cents,p.cost_cents) cost_cents,p.version FROM partner_prices p JOIN catalog_plans c ON c.id=p.plan WHERE p.partner_id=$1 ORDER BY c.sort_order,c.id`, parent)
	if e != nil {
		return nil, e
	}
	return map[string]any{"plans": plans}, tx.Commit(ctx)
}

type ChildDefaultsInput struct {
	Prices []SupplyQuoteInput `json:"prices"`
}

// Both operations validate the complete displayed quote set in one transaction.
// Applying quotes only changes costs; retail prices and availability stay independent.
func (s Service) SaveChildDefaults(ctx context.Context, token string, in ChildDefaultsInput, apply bool) (int, error) {
	seen := make(map[string]bool)
	if len(in.Prices) == 0 {
		return 0, fault.New(400, "请填写所有套餐的默认拿货价。")
	}
	for _, r := range in.Prices {
		if r.Plan == "" || seen[r.Plan] || !validMoney(r.CostCents) || r.Version == nil || *r.Version < 0 {
			return 0, fault.New(400, "套餐、拿货价或版本无效，请刷新后重试。")
		}
		seen[r.Plan] = true
	}
	updated := 0
	e := s.hierarchyWrite(ctx, token, func(tx pgx.Tx, p auth.Principal) error {
		var count int
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM partner_prices WHERE partner_id=$1`, p.Subject).Scan(&count); e != nil {
			return e
		}
		if count != len(in.Prices) {
			return conflict()
		}
		for _, r := range in.Prices {
			var floor, cost, version int64
			e := tx.QueryRow(ctx, `SELECT cost_cents,COALESCE(child_cost_cents,cost_cents),version FROM partner_prices WHERE partner_id=$1 AND plan=$2`, p.Subject, r.Plan).Scan(&floor, &cost, &version)
			if e == pgx.ErrNoRows {
				return conflict()
			}
			if e != nil {
				return e
			}
			if version != *r.Version {
				return conflict()
			}
			if r.CostCents < floor {
				return fault.New(400, "默认拿货价不能低于你的拿货价。")
			}
			if apply && cost != r.CostCents {
				return fault.New(409, "默认拿货价尚未保存或已更新，请刷新后重试。")
			}
		}
		if apply {
			children, e := rows(ctx, tx, `SELECT id,name FROM partners WHERE parent_id=$1 ORDER BY id`, p.Subject)
			if e != nil {
				return e
			}
			for _, child := range children {
				id := child["id"].(string)
				for _, r := range in.Prices {
					var price, old int64
					if e = tx.QueryRow(ctx, `SELECT price_cents,cost_cents FROM partner_prices WHERE partner_id=$1 AND plan=$2`, id, r.Plan).Scan(&price, &old); e != nil {
						return e
					}
					if old == r.CostCents {
						continue
					}
					if r.CostCents > price {
						return fault.New(409, fmt.Sprintf("%s 的 %s 拿货价高于当前售价，请先协调二级调价；本次未应用任何价格。", child["name"], r.Plan))
					}
					if e = checkAccountFloor(ctx, tx, id, r.Plan, r.CostCents); e != nil {
						return fault.New(fault.Status(e), fmt.Sprintf("%s · %s：%s 本次未应用任何价格。", child["name"], r.Plan, fault.Message(e)))
					}
					if _, e = tx.Exec(ctx, `UPDATE partner_prices SET cost_cents=$1,child_cost_cents=greatest(child_cost_cents,$1),version=version+1 WHERE partner_id=$2 AND plan=$3`, r.CostCents, id, r.Plan); e != nil {
						return e
					}
				}
			}
			updated = len(children)
			return audit(ctx, tx, p, "child-default-prices-apply", p.Subject, map[string]any{"prices": in.Prices, "children": children})
		}
		for _, r := range in.Prices {
			if _, e := tx.Exec(ctx, `UPDATE partner_prices SET child_cost_cents=$1,version=version+1 WHERE partner_id=$2 AND plan=$3`, r.CostCents, p.Subject, r.Plan); e != nil {
				return e
			}
		}
		return audit(ctx, tx, p, "child-default-prices-save", p.Subject, in)
	})
	if e != nil {
		return 0, e
	}
	return updated, nil
}
