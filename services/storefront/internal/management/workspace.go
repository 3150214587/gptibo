package management

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"gptibo/storefront/internal/auth"
	"gptibo/storefront/internal/catalog"
	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/finance"
	"gptibo/storefront/internal/mailer"
	"gptibo/storefront/internal/orders"
)

func (s Service) Invite(ctx context.Context, token, id, origin string) error {
	if _, e := s.Auth.Require(ctx, s.Store.DB, "admin", token); e != nil {
		return e
	}
	_, login, e := s.partnerLinks(ctx, s.Store.DB, id, origin)
	if e != nil {
		return e
	}
	if e := s.Auth.Invite(mailer.WithBrand(ctx, mailer.PlatformBrand(s.Origin)), id, strings.TrimSuffix(login, "/join")); e != nil {
		return e
	}
	return s.write(ctx, "admin", token, func(tx pgx.Tx, p auth.Principal) error {
		return audit(ctx, tx, p, "partner-invitation", id, map[string]any{"note": "发送代理工作台邀请邮件"})
	})
}

func (s Service) Dashboard(ctx context.Context, id string, admin bool) (map[string]any, error) {
	tx, e := s.Store.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	partners, e := rows(ctx, tx, `SELECT id,slug,name,email,active::int,version,parent_id,can_recruit,EXISTS(SELECT 1 FROM partners child WHERE child.parent_id=partners.id) has_children,EXISTS(SELECT 1 FROM orders o WHERE o.upstream_partner_id=partners.id) has_upstream_earnings FROM partners WHERE id=$1`, id)
	if e != nil {
		return nil, e
	}
	if len(partners) == 0 {
		return nil, fault.New(404, "代理不存在。")
	}
	origin, e := s.referralOrigin(ctx, tx, id)
	if e != nil {
		return nil, e
	}
	partners[0]["referralOrigin"] = origin
	plans, e := rows(ctx, tx, `SELECT c.id,c.product_id,c.name,c.renewal_only::int,(c.enabled AND c.partner_enabled AND p.enabled)::int enabled,p.cost_cents,p.price_cents,p.version FROM catalog_plans c JOIN partner_prices p ON p.plan=c.id WHERE p.partner_id=$1 ORDER BY c.sort_order,c.id`, id)
	if e != nil {
		return nil, e
	}
	stats, e := rows(ctx, tx, `SELECT count(*) total,COALESCE(sum(CASE WHEN o.mode='demo' AND o.status='success' THEN (`+finance.Profit("o")+`) ELSE 0 END),0)::bigint demo_profit,COALESCE(sum(CASE WHEN `+finance.Eligible("o")+` AND o.payout_id IS NULL THEN (`+finance.Profit("o")+`) ELSE 0 END),0)::bigint available FROM partner_earning_orders o WHERE o.partner_id=$1`, id)
	if e != nil {
		return nil, e
	}
	st := stats[0]
	var debt int64
	var held bool
	var last *int64
	if e = tx.QueryRow(ctx, `SELECT COALESCE(sum(amount_cents),0)::bigint FROM partner_debits WHERE partner_id=$1 AND payout_id IS NULL`, id).Scan(&debt); e != nil {
		return nil, e
	}
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM partner_earning_orders o JOIN partner_payouts p ON p.id=o.payout_id LEFT JOIN recharge_orders t ON t.order_id=o.id WHERE o.partner_id=$1 AND p.status='paid' AND (o.refund_hold=1 OR (o.payment_status='paid' AND t.financial_hold=1)))`, id).Scan(&held); e != nil {
		return nil, e
	}
	if e = tx.QueryRow(ctx, `SELECT max(created_at) FROM partner_payouts WHERE partner_id=$1 AND cooldown_exempt=0`, id).Scan(&last); e != nil {
		return nil, e
	}
	st["available"] = max(int64(0), st["available"].(int64)-debt)
	st["debt"] = debt
	st["withdrawalHeld"] = held
	st["minimum"] = int64(1000)
	st["nextPayoutAt"] = nil
	st["payoutCoolingDown"] = false
	if last != nil {
		next := *last + 86400000
		st["nextPayoutAt"] = next
		st["payoutCoolingDown"] = time.Now().UnixMilli() < next
	}
	payouts, e := rows(ctx, tx, `SELECT * FROM partner_payouts WHERE partner_id=$1 ORDER BY created_at DESC,id DESC LIMIT 20`, id)
	if e != nil {
		return nil, e
	}
	totals, e := rows(ctx, tx, `SELECT COALESCE(sum(CASE WHEN status='pending' THEN amount_cents ELSE 0 END),0)::bigint pending,COALESCE(sum(CASE WHEN status='paid' THEN amount_cents ELSE 0 END),0)::bigint paid FROM partner_payouts WHERE partner_id=$1`, id)
	if e != nil {
		return nil, e
	}
	var payoutAccount PayoutAccount
	if e = tx.QueryRow(ctx, `SELECT COALESCE(a.phone,''),COALESCE(a.name,''),COALESCE(a.version,0) FROM partners p LEFT JOIN partner_payout_accounts a ON a.partner_id=p.id WHERE p.id=$1`, id).Scan(&payoutAccount.Phone, &payoutAccount.Name, &payoutAccount.Version); e != nil {
		return nil, e
	}
	if e = s.attachPartnerLinks(ctx, tx, partners[0]); e != nil {
		return nil, e
	}
	out := map[string]any{"partner": partners[0], "plans": plans, "stats": st, "payouts": payouts, "totals": totals[0], "mailReady": s.Auth.Ready(), "payoutAccount": payoutAccount}
	if admin {
		history, e := rows(ctx, tx, `SELECT id::text,action,detail,COALESCE(detail->>'note',action) note,(extract(epoch FROM created_at)*1000)::bigint created_at FROM audit_events WHERE target=$1 ORDER BY created_at DESC,id DESC LIMIT 20`, id)
		if e != nil {
			return nil, e
		}
		out["audit"] = history
		applications, e := rows(ctx, tx, `SELECT user_id,group_id,wechat,qq,email,created_at,mail_status FROM telegram_partner_applications WHERE partner_id=$1`, id)
		if e != nil {
			return nil, e
		}
		out["telegramApplication"] = nil
		if len(applications) > 0 {
			out["telegramApplication"] = applications[0]
		}
	}
	return out, tx.Commit(ctx)
}

// Explicit projection keeps customer credentials, delivery codes and internal notes private.
var partnerOrderSelect = `SELECT o.id,o.store_id,o.store_name,o.coupon_code,o.list_price_cents,o.discount_cents,o.customer_email,o.plan,o.plan_name,o.price_cents,o.cost_cents,o.refunded_cents,t.payment_status platform_payment_status,t.refunded_cents platform_refunded_cents,t.verified_at platform_checked_at,greatest(o.refund_hold,CASE WHEN o.payment_status='paid' THEN COALESCE(t.financial_hold,0) ELSE 0 END) refund_hold,(` + finance.Profit("o") + `) profit_cents,o.mode,o.payment_status,o.status,CASE WHEN o.status<>'success' THEN (SELECT a.status FROM recharge_redemptions a WHERE a.order_id=o.id ORDER BY a.attempt_no DESC LIMIT 1) END recharge_status,o.created_at,o.updated_at,p.status payout_status,p.reviewed_at FROM orders o LEFT JOIN partner_payouts p ON p.id=o.payout_id LEFT JOIN recharge_orders t ON t.order_id=o.id`

func (s Service) PartnerOrders(ctx context.Context, id string, page int, filters ...string) (map[string]any, error) {
	query, status := "", ""
	shop := ""
	if len(filters) > 2 {
		shop = filters[2]
	}
	if len(filters) > 0 {
		query = strings.TrimSpace(filters[0])
	}
	if len(filters) > 1 {
		status = filters[1]
	}
	if page < 1 || page > 10000 || !validText(query, 320) || (status != "" && status != "pending" && status != "session" && status != "processing" && status != "success" && status != "failed" && status != "cancelled") {
		return nil, fault.New(400, "搜索条件或页码无效。")
	}
	tx, e := s.Store.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	where := ` WHERE o.partner_id=$1 AND ($2='' OR strpos(lower(o.id),lower($2))>0 OR strpos(lower(COALESCE(o.customer_email,'')),lower($2))>0 OR strpos(lower(o.plan_name),lower($2))>0 OR strpos(lower(o.plan),lower($2))>0) AND ($3='' OR o.status=$3) AND ($4='' OR o.store_id=$4 OR (o.store_id IS NULL AND o.partner_id=$4))`
	list, e := rows(ctx, tx, partnerOrderSelect+where+` ORDER BY o.created_at DESC,o.id DESC LIMIT 20 OFFSET $5`, id, query, status, shop, (page-1)*20)
	if e != nil {
		return nil, e
	}
	var total int64
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM orders o`+where, id, query, status, shop).Scan(&total); e != nil {
		return nil, e
	}
	return map[string]any{"rows": list, "total": total, "page": page}, tx.Commit(ctx)
}

func (s Service) PartnerOrderDetail(ctx context.Context, partner, id string) (map[string]any, error) {
	tx, e := s.Store.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	list, e := rows(ctx, tx, partnerOrderSelect+` WHERE o.partner_id=$1 AND o.id=$2`, partner, id)
	if e != nil {
		return nil, e
	}
	if len(list) == 0 {
		return nil, fault.New(404, "订单不存在。")
	}
	o, e := orders.Get(ctx, tx, id, nil, false)
	if e != nil {
		return nil, e
	}
	view, e := (orders.Service{SupplierReady: s.VerifySupplier != nil}).View(ctx, tx, o, false)
	if e != nil {
		return nil, e
	}
	detail := list[0]
	detail["fulfillment"] = nil
	if f, ok := view["fulfillment"].(map[string]any); ok {
		public := map[string]any{}
		for _, key := range []string{"rechargeStatus", "failureCode", "errorCategory", "progressStage", "progressUpdatedAt", "attemptNo", "canSubmit", "message", "failureReason", "accountEmailMasked", "finishedAt", "recoveryAction"} {
			public[key] = f[key]
		}
		detail["fulfillment"] = public
	}
	refs, e := rows(ctx, tx, `SELECT t.remote_id order_id,a.remote_id redemption_id FROM recharge_orders t LEFT JOIN LATERAL (SELECT remote_id FROM recharge_redemptions WHERE order_id=t.order_id ORDER BY attempt_no DESC LIMIT 1) a ON true WHERE t.order_id=$1`, id)
	if e != nil {
		return nil, e
	}
	if len(refs) > 0 {
		detail["rechargeReference"] = refs[0]
	}
	if delivery, ok := view["delivery"].(map[string]any); ok {
		detail["delivery"] = map[string]any{"status": delivery["status"], "sentAt": delivery["sentAt"]}
	}
	if gift, ok := view["xgift"].(map[string]any); ok {
		detail["serviceMessage"] = gift["message"]
	}
	// Ownership was checked in this read-only snapshot. Internal notes remain
	// admin-only, and actors expose roles rather than administrator identities.
	events, e := rows(ctx, tx, `SELECT id,CASE WHEN actor='customer' THEN 'customer' WHEN actor IN ('system','payment') THEN 'system' ELSE 'admin' END actor,action,to_status,note,created_at FROM order_events WHERE order_id=$1 AND action<>'note' ORDER BY order_version`, id)
	if e != nil {
		return nil, e
	}
	return map[string]any{"order": detail, "events": events}, tx.Commit(ctx)
}

func (s Service) OrderSummary(ctx context.Context, id, period string) (map[string]any, error) {
	now := time.Now().UnixMilli()
	var since any
	start := int64(0)
	if period != "all" {
		days := int64(0)
		switch period {
		case "today":
			days = 1
		case "7":
			days = 7
		case "30":
			days = 30
		default:
			return nil, fault.New(400, "时间范围无效。")
		}
		start = (now+28800000)/86400000*86400000 - 28800000 - (days-1)*86400000
		since = start
	}
	products, e := rows(ctx, s.Store.DB, `SELECT COALESCE(c.product_id,'other') "productId",count(*) "orderCount",count(*) FILTER(WHERE o.payment_status='paid' AND o.status='success') "completedCount",COALESCE(sum(CASE WHEN `+finance.Eligible("o")+` THEN (`+finance.Profit("o")+`) ELSE 0 END),0)::bigint "profitCents",count(*) FILTER(WHERE o.payment_status='paid' AND o.status='success' AND (o.refund_hold=1 OR o.cost_cents IS NULL OR EXISTS(SELECT 1 FROM recharge_orders t WHERE t.order_id=o.id AND(t.financial_hold=1 OR t.merchant_margin_cents IS NULL)))) "heldCount" FROM partner_earning_orders o LEFT JOIN catalog_plans c ON c.id=o.plan WHERE o.partner_id=$1 AND o.mode='live' AND o.is_test=0 AND o.created_at BETWEEN $2 AND $3 GROUP BY c.product_id ORDER BY "profitCents" DESC,"orderCount" DESC,"productId"`, id, start, now)
	if e != nil {
		return nil, e
	}
	totals := map[string]int64{"orderCount": 0, "completedCount": 0, "profitCents": 0, "heldCount": 0}
	for _, p := range products {
		for k := range totals {
			totals[k] += p[k].(int64)
		}
	}
	return map[string]any{"period": period, "since": since, "asOf": now, "totals": totals, "products": products}, nil
}

func (s Service) ProductOrder(ctx context.Context, id string) (map[string]any, error) {
	tx, e := s.Store.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	var version int64
	hidden := []string{}
	e = tx.QueryRow(ctx, `SELECT version,hidden_product_ids FROM partner_order_versions WHERE partner_id=$1`, id).Scan(&version, &hidden)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	}
	list, e := rows(ctx, tx, `SELECT product_id FROM partner_product_order WHERE partner_id=$1 ORDER BY position`, id)
	if e != nil {
		return nil, e
	}
	saved := []string{}
	for _, row := range list {
		saved = append(saved, row["product_id"].(string))
	}
	return map[string]any{"productIds": catalog.NormalizeProductOrder(saved), "hiddenProductIds": hidden, "version": version}, tx.Commit(ctx)
}

type ProductOrderInput struct {
	HiddenProductIDs *[]string `json:"hiddenProductIds"`
	ProductIDs       []string  `json:"productIds"`
	Version          *int64    `json:"version"`
}

func (s Service) SaveProductOrder(ctx context.Context, token string, in ProductOrderInput) error {
	known := catalog.NormalizeProductOrder(nil)
	if in.Version == nil || *in.Version < 0 || len(in.ProductIDs) != len(known) {
		return fault.New(400, "商品列表或版本无效。")
	}
	allowed := map[string]bool{}
	for _, id := range known {
		allowed[id] = true
	}
	for _, id := range in.ProductIDs {
		if !allowed[id] {
			return fault.New(400, "商品列表无效或重复。")
		}
		delete(allowed, id)
	}
	var hiddenJSON []byte
	if in.HiddenProductIDs != nil {
		allowedHidden := map[string]bool{}
		for _, id := range known {
			allowedHidden[id] = true
		}
		for _, id := range *in.HiddenProductIDs {
			if !allowedHidden[id] {
				return fault.New(400, "下架商品列表无效或重复。")
			}
			delete(allowedHidden, id)
		}
		hiddenJSON, _ = json.Marshal(*in.HiddenProductIDs)
	}
	return s.write(ctx, "partner", token, func(tx pgx.Tx, p auth.Principal) error {
		if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,4))`, p.Subject); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `INSERT INTO partner_order_versions(partner_id) VALUES($1) ON CONFLICT DO NOTHING`, p.Subject); e != nil {
			return e
		}
		tag, e := tx.Exec(ctx, `UPDATE partner_order_versions SET version=version+1,hidden_product_ids=COALESCE($3::jsonb,hidden_product_ids) WHERE partner_id=$1 AND version=$2`, p.Subject, *in.Version, hiddenJSON)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return conflict()
		}
		if _, e = tx.Exec(ctx, `DELETE FROM partner_product_order WHERE partner_id=$1`, p.Subject); e != nil {
			return e
		}
		for pos, id := range in.ProductIDs {
			if _, e = tx.Exec(ctx, `INSERT INTO partner_product_order(partner_id,product_id,position) VALUES($1,$2,$3)`, p.Subject, id, pos); e != nil {
				return e
			}
		}
		return audit(ctx, tx, p, "partner-product-order", p.Subject, map[string]any{"productIds": in.ProductIDs, "hiddenProductIds": in.HiddenProductIDs, "version": *in.Version + 1})
	})
}

func (s Service) DefaultPrices(ctx context.Context) (map[string]any, error) {
	tx, e := s.Store.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	var version int64
	if e = tx.QueryRow(ctx, `SELECT version FROM default_price_settings WHERE id=1`).Scan(&version); e != nil {
		return nil, e
	}
	plans, e := rows(ctx, tx, `SELECT id,product_id,name,price_cents,renewal_only::int,version catalog_version,COALESCE(default_cost_cents,price_cents) cost_cents FROM catalog_plans ORDER BY sort_order,id`)
	if e != nil {
		return nil, e
	}
	return map[string]any{"version": version, "plans": plans}, tx.Commit(ctx)
}

type DefaultPricesInput struct {
	Version *int64 `json:"version"`
	Prices  []struct {
		Plan           string `json:"plan"`
		Cost           int64  `json:"costCents"`
		CatalogVersion *int64 `json:"catalogVersion"`
	} `json:"prices"`
}

func (s Service) SaveDefaultPrices(ctx context.Context, token string, in DefaultPricesInput) error {
	if in.Version == nil || *in.Version < 0 {
		return fault.New(400, "版本无效。")
	}
	return s.writeHierarchy(ctx, "admin", token, func(tx pgx.Tx, p auth.Principal) error {
		var count int
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM catalog_plans`).Scan(&count); e != nil {
			return e
		}
		if count != len(in.Prices) {
			return fault.New(400, "请填写全部商品的供货价。")
		}
		tag, e := tx.Exec(ctx, `UPDATE default_price_settings SET version=version+1 WHERE id=1 AND version=$1`, *in.Version)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return conflict()
		}
		before, e := rows(ctx, tx, `SELECT id,default_cost_cents FROM catalog_plans ORDER BY id`)
		if e != nil {
			return e
		}
		seen := map[string]bool{}
		for _, v := range in.Prices {
			if seen[v.Plan] || !validMoney(v.Cost) || v.CatalogVersion == nil {
				return fault.New(400, "商品或价格无效。")
			}
			seen[v.Plan] = true
			var retail, version int64
			e = tx.QueryRow(ctx, `SELECT price_cents,version FROM catalog_plans WHERE id=$1 FOR UPDATE`, v.Plan).Scan(&retail, &version)
			if errors.Is(e, pgx.ErrNoRows) {
				return fault.New(400, "商品不存在。")
			}
			if e != nil {
				return e
			}
			if version != *v.CatalogVersion {
				return conflict()
			}
			if v.Cost > retail {
				return fault.New(400, "默认供货价不能高于售价。")
			}
			if _, e = tx.Exec(ctx, `UPDATE catalog_plans SET default_cost_cents=$1 WHERE id=$2`, v.Cost, v.Plan); e != nil {
				return e
			}
		}
		return audit(ctx, tx, p, "partner-default-prices", "partner-default-prices", map[string]any{"before": before, "after": in.Prices, "version": *in.Version + 1})
	})
}
