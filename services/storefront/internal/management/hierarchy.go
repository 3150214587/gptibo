package management

import (
	"context"
	"github.com/jackc/pgx/v5"
	"gptibo/storefront/internal/auth"
	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/mailer"
	"strings"
)

func (s Service) hierarchyWrite(ctx context.Context, token string, fn func(pgx.Tx, auth.Principal) error) error {
	return s.writeHierarchy(ctx, "partner", token, func(tx pgx.Tx, p auth.Principal) error {
		var top bool
		if e := tx.QueryRow(ctx, `SELECT parent_id IS NULL FROM partners WHERE id=$1`, p.Subject).Scan(&top); e != nil {
			return e
		}
		if !top {
			return fault.New(403, "二级代理不能发展或管理下级。")
		}
		return fn(tx, p)
	})
}

func childExists(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, parent, id string) error {
	var ok bool
	if e := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM partners WHERE id=$1 AND parent_id=$2)`, id, parent).Scan(&ok); e != nil {
		return e
	}
	if !ok {
		return fault.New(404, "二级代理不存在。")
	}
	return nil
}

func (s Service) SetRecruit(ctx context.Context, token, id string, enabled bool, version int64) error {
	return s.writeHierarchy(ctx, "admin", token, func(tx pgx.Tx, p auth.Principal) error {
		tag, e := tx.Exec(ctx, `UPDATE partners SET can_recruit=$1,version=version+1 WHERE id=$2 AND parent_id IS NULL AND version=$3`, enabled, id, version)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return conflict()
		}
		return audit(ctx, tx, p, "partner-recruit-permission", id, map[string]any{"enabled": enabled})
	})
}

func (s Service) CreateChild(ctx context.Context, token, email string) (string, string, error) {
	email, e := auth.Email(email)
	if e != nil {
		return "", "", e
	}
	var id, slug string
	e = s.hierarchyWrite(ctx, token, func(tx pgx.Tx, p auth.Principal) error {
		var allowed bool
		if e := tx.QueryRow(ctx, `SELECT can_recruit FROM partners WHERE id=$1`, p.Subject).Scan(&allowed); e != nil {
			return e
		}
		if !allowed {
			return fault.New(403, "总后台尚未开放发展二级代理权限。")
		}
		var err error
		id, slug, err = ProvisionPartner(ctx, tx, p, email, "代理商店")
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE partners SET parent_id=$1 WHERE id=$2`, p.Subject, id); err != nil {
			return err
		}
		// Use an explicit account-level supply quote, independent of all retail stores.
		if _, err = tx.Exec(ctx, `UPDATE partner_prices child SET cost_cents=COALESCE(parent.child_cost_cents,parent.cost_cents),price_cents=COALESCE(parent.child_cost_cents,parent.cost_cents),child_cost_cents=COALESCE(parent.child_cost_cents,parent.cost_cents),enabled=parent.enabled FROM partner_prices parent WHERE child.partner_id=$1 AND parent.partner_id=$2 AND child.plan=parent.plan`, id, p.Subject); err != nil {
			return err
		}
		return audit(ctx, tx, p, "child-create", id, map[string]any{"parentId": p.Subject, "email": email})
	})
	return id, slug, e
}

type ChildInput struct {
	Email   string `json:"email"`
	Active  *bool  `json:"active"`
	Version *int64 `json:"version"`
}

func (s Service) SaveChild(ctx context.Context, token, id string, in ChildInput) error {
	email, e := auth.Email(in.Email)
	if e != nil {
		return e
	}
	if in.Active == nil || in.Version == nil || *in.Version < 0 {
		return fault.New(400, "请提供启用状态及当前版本。")
	}
	return s.hierarchyWrite(ctx, token, func(tx pgx.Tx, p auth.Principal) error {
		if e := childExists(ctx, tx, p.Subject, id); e != nil {
			return e
		}
		before, e := snapshot(ctx, tx, "partners", id)
		if e != nil {
			return e
		}
		tag, e := tx.Exec(ctx, `UPDATE partners SET email=$1,active=$2,auth_version=auth_version+CASE WHEN email IS DISTINCT FROM $1 OR active<>$2 THEN 1 ELSE 0 END,version=version+1 WHERE id=$3 AND version=$4`, email, *in.Active, id, *in.Version)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return conflict()
		}
		return audit(ctx, tx, p, "child-update", id, map[string]any{"previousEmail": before["email"], "email": email, "active": *in.Active})
	})
}

type ChildPriceInput struct {
	CostCents int64  `json:"costCents"`
	Enabled   *bool  `json:"enabled"`
	Version   *int64 `json:"version"`
}

func (s Service) ChildPrice(ctx context.Context, token, id, plan string, in ChildPriceInput) error {
	if !validMoney(in.CostCents) || in.Enabled == nil || in.Version == nil || *in.Version < 0 {
		return fault.New(400, "供货价、开通状态或版本无效。")
	}
	return s.hierarchyWrite(ctx, token, func(tx pgx.Tx, p auth.Principal) error {
		if e := childExists(ctx, tx, p.Subject, id); e != nil {
			return e
		}
		var floor, price, version int64
		var allowed bool
		e := tx.QueryRow(ctx, `SELECT parent.cost_cents,child.price_cents,child.version,parent.enabled AND c.enabled AND c.partner_enabled FROM partner_prices child JOIN partner_prices parent ON parent.plan=child.plan AND parent.partner_id=$2 JOIN catalog_plans c ON c.id=child.plan WHERE child.partner_id=$1 AND child.plan=$3`, id, p.Subject, plan).Scan(&floor, &price, &version, &allowed)
		if e == pgx.ErrNoRows {
			return fault.New(404, "套餐不存在。")
		}
		if e != nil {
			return e
		}
		if version != *in.Version {
			return conflict()
		}
		if in.CostCents < floor || in.CostCents > price {
			return fault.New(400, "二级供货价须不低于你的拿货价，且不能超过二级当前售价。请先协调二级调价。")
		}
		if *in.Enabled && !allowed {
			return fault.New(409, "平台未开放该套餐。")
		}
		if e = checkAccountFloor(ctx, tx, id, plan, in.CostCents); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE partner_prices SET cost_cents=$1,child_cost_cents=greatest(child_cost_cents,$1),enabled=$2,version=version+1 WHERE partner_id=$3 AND plan=$4`, in.CostCents, *in.Enabled, id, plan); e != nil {
			return e
		}
		return audit(ctx, tx, p, "child-price", id, map[string]any{"plan": plan, "costCents": in.CostCents, "enabled": *in.Enabled})
	})
}

func (s Service) Children(ctx context.Context, parent string, page int) (map[string]any, error) {
	if page < 1 || page > 10000 {
		return nil, fault.New(400, "页码无效。")
	}
	list, e := rows(ctx, s.Store.DB, `SELECT id,slug,name,email,active::int,version FROM partners WHERE parent_id=$1 ORDER BY created_at DESC,id LIMIT 20 OFFSET $2`, parent, (page-1)*20)
	if e != nil {
		return nil, e
	}
	var total int64
	if e = s.Store.DB.QueryRow(ctx, `SELECT count(*) FROM partners WHERE parent_id=$1`, parent).Scan(&total); e != nil {
		return nil, e
	}
	return map[string]any{"rows": list, "total": total, "page": page}, nil
}
func (s Service) ChildDetail(ctx context.Context, parent, id string) (map[string]any, error) {
	tx, e := s.Store.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	if e = childExists(ctx, tx, parent, id); e != nil {
		return nil, e
	}
	ps, e := rows(ctx, tx, `SELECT id,slug,name,email,active::int,version FROM partners WHERE id=$1`, id)
	if e != nil {
		return nil, e
	}
	plans, e := rows(ctx, tx, `SELECT c.id,c.name,c.product_id,p.cost_cents,p.price_cents,p.version,p.enabled::int,parent.cost_cents minimum_cost_cents,(c.enabled AND c.partner_enabled AND parent.enabled) platform_enabled FROM partner_prices p JOIN catalog_plans c ON c.id=p.plan JOIN partner_prices parent ON parent.plan=p.plan AND parent.partner_id=$2 WHERE p.partner_id=$1 ORDER BY c.sort_order,c.id`, id, parent)
	if e != nil {
		return nil, e
	}
	stats, e := rows(ctx, tx, `SELECT count(*) order_count,COALESCE(sum(partner_order_profit(o.id,$1)) FILTER(WHERE o.mode='live' AND o.is_test=0 AND o.payment_status='paid' AND o.status='success' AND o.refund_hold=0 AND NOT EXISTS(SELECT 1 FROM recharge_orders t WHERE t.order_id=o.id AND (t.financial_hold=1 OR t.merchant_margin_cents IS NULL))),0)::bigint child_profit_cents,COALESCE(sum(partner_order_profit(o.id,$2)) FILTER(WHERE o.mode='live' AND o.is_test=0 AND o.payment_status='paid' AND o.status='success' AND o.refund_hold=0 AND NOT EXISTS(SELECT 1 FROM recharge_orders t WHERE t.order_id=o.id AND (t.financial_hold=1 OR t.merchant_margin_cents IS NULL))),0)::bigint upstream_profit_cents FROM orders o WHERE o.partner_id=$1`, id, parent)
	if e != nil {
		return nil, e
	}
	origin, e := s.referralOrigin(ctx, tx, id)
	if e != nil {
		return nil, e
	}
	ps[0]["referralOrigin"] = origin
	if e = s.attachPartnerLinks(ctx, tx, ps[0]); e != nil {
		return nil, e
	}
	return map[string]any{"partner": ps[0], "plans": plans, "stats": stats[0]}, tx.Commit(ctx)
}
func (s Service) ChildOrders(ctx context.Context, parent, id string, page int) (map[string]any, error) {
	if page < 1 || page > 10000 {
		return nil, fault.New(400, "页码无效。")
	}
	tx, e := s.Store.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	if e = childExists(ctx, tx, parent, id); e != nil {
		return nil, e
	}
	// Do not reuse the child workspace or order detail: those contain buyer information.
	list, e := rows(ctx, tx, `SELECT o.id,o.plan_name,o.price_cents,o.cost_cents,o.status,o.payment_status,o.mode,o.is_test,o.created_at,o.refunded_cents,greatest(o.refund_hold,COALESCE(t.financial_hold,0)) refund_hold,partner_order_profit(o.id,o.partner_id) child_profit_cents,partner_order_profit(o.id,$2) upstream_profit_cents,(SELECT status FROM refund_cases WHERE order_id=o.id ORDER BY created_at DESC,id DESC LIMIT 1) refund_status,cp.status child_payout_status,up.status upstream_payout_status FROM orders o LEFT JOIN recharge_orders t ON t.order_id=o.id LEFT JOIN partner_payouts cp ON cp.id=o.payout_id LEFT JOIN partner_payouts up ON up.id=o.upstream_payout_id WHERE o.partner_id=$1 ORDER BY o.created_at DESC,o.id DESC LIMIT 20 OFFSET $3`, id, parent, (page-1)*20)
	if e != nil {
		return nil, e
	}
	var total int64
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM orders WHERE partner_id=$1`, id).Scan(&total); e != nil {
		return nil, e
	}
	return map[string]any{"rows": list, "total": total, "page": page}, tx.Commit(ctx)
}
func (s Service) InviteChild(ctx context.Context, token, id, origin string) error {
	var brand mailer.Brand
	e := s.hierarchyWrite(ctx, token, func(tx pgx.Tx, p auth.Principal) error {
		if e := childExists(ctx, tx, p.Subject, id); e != nil {
			return e
		}
		var err error
		brand, err = (mailer.Brands{DB: tx, Origin: s.Origin}).Store(ctx, p.Subject, "")
		if err != nil {
			return err
		}
		return audit(ctx, tx, p, "child-invitation", id, map[string]any{"note": "发送工作台邀请"})
	})
	if e != nil {
		return e
	}
	_, login, e := s.partnerLinks(ctx, s.Store.DB, id, origin)
	if e != nil {
		return e
	}
	return s.Auth.Invite(mailer.WithBrand(ctx, brand), id, strings.TrimSuffix(login, "/join"))
}

// Only total administration may change the relationship; order attribution is immutable.
func (s Service) SetParent(ctx context.Context, token, id, parent string, version int64) error {
	return s.writeHierarchy(ctx, "admin", token, func(tx pgx.Tx, p auth.Principal) error {
		before, e := snapshot(ctx, tx, "partners", id)
		if e != nil {
			return e
		}
		if before == nil {
			return fault.New(404, "代理不存在。")
		}
		if id == parent {
			return fault.New(400, "不能将自己设为上级。")
		}
		if parent != "" {
			var allowed, hasChildren, invalid bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM partners WHERE id=$1 AND parent_id IS NULL AND active AND can_recruit),EXISTS(SELECT 1 FROM partners WHERE parent_id=$2),EXISTS(SELECT 1 FROM partner_prices c JOIN partner_prices p ON p.plan=c.plan WHERE c.partner_id=$2 AND p.partner_id=$1 AND c.cost_cents<p.cost_cents)`, parent, id).Scan(&allowed, &hasChildren, &invalid); e != nil {
				return e
			}
			if !allowed || hasChildren {
				return fault.New(409, "请选择已开放发展权限的一级代理；拥有下级的账号不能降为二级。")
			}
			if invalid {
				return fault.New(409, "新上级拿货价高于当前二级供货价，请先调整供货价。")
			}
		}
		tag, e := tx.Exec(ctx, `UPDATE partners SET parent_id=NULLIF($1,''),can_recruit=false,auth_version=auth_version+1,version=version+1 WHERE id=$2 AND version=$3`, parent, id, version)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return conflict()
		}
		if parent == "" {
			if _, e = tx.Exec(ctx, `UPDATE partner_prices SET enabled=true,version=version+1 WHERE partner_id=$1 AND NOT enabled`, id); e != nil {
				return e
			}
		}
		return audit(ctx, tx, p, "partner-parent", id, map[string]any{"beforeParent": before["parent_id"], "parentId": parent, "note": "仅新订单采用新归属，历史收益保留"})
	})
}
func (s Service) UpstreamOrders(ctx context.Context, parent string, page int) (map[string]any, error) {
	if page < 1 || page > 10000 {
		return nil, fault.New(400, "页码无效。")
	}
	list, e := rows(ctx, s.Store.DB, `SELECT o.id,o.partner_name,o.plan_name,o.price_cents,o.cost_cents,o.status,o.payment_status,o.mode,o.is_test,o.created_at,o.refunded_cents,greatest(o.refund_hold,COALESCE(t.financial_hold,0)) refund_hold,partner_order_profit(o.id,o.partner_id) child_profit_cents,partner_order_profit(o.id,$1) upstream_profit_cents,(SELECT status FROM refund_cases WHERE order_id=o.id ORDER BY created_at DESC,id DESC LIMIT 1) refund_status,cp.status child_payout_status,up.status upstream_payout_status FROM orders o LEFT JOIN recharge_orders t ON t.order_id=o.id LEFT JOIN partner_payouts cp ON cp.id=o.payout_id LEFT JOIN partner_payouts up ON up.id=o.upstream_payout_id WHERE o.upstream_partner_id=$1 ORDER BY o.created_at DESC,o.id DESC LIMIT 20 OFFSET $2`, parent, (page-1)*20)
	if e != nil {
		return nil, e
	}
	var total int64
	if e = s.Store.DB.QueryRow(ctx, `SELECT count(*) FROM orders WHERE upstream_partner_id=$1`, parent).Scan(&total); e != nil {
		return nil, e
	}
	return map[string]any{"rows": list, "total": total, "page": page}, nil
}
