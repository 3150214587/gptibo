// Package domainbilling keeps domain fees separate from retail orders and their margins.
package domainbilling

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/finance"
	"gptibo/storefront/internal/orders"
	"gptibo/storefront/internal/payments"
	"gptibo/storefront/internal/security"
	"gptibo/storefront/internal/store"
)

type Service struct {
	MonthlyFeeCents int64
	Store           *store.Store
	Payments        payments.Service
	VerifySupplier  func(context.Context, string) error
	RenewEnabled    func() bool
}

func lock(ctx context.Context, tx pgx.Tx) error {
	_, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(903500)`)
	return e
}
func (s Service) binding(ctx context.Context, tx pgx.Tx, partner, id string) (map[string]any, error) {
	r, e := orders.Rows(ctx, tx, `SELECT * FROM partner_domains WHERE id=$1 AND partner_id=$2 AND state<>'deleting' FOR UPDATE`, id, partner)
	if e != nil {
		return nil, e
	}
	if len(r) != 1 {
		return nil, fault.New(404, "域名绑定不存在。")
	}
	return r[0], nil
}
func extend(ctx context.Context, tx pgx.Tx, id string, period int64) error {
	tag, e := tx.Exec(ctx, `UPDATE partner_domains SET expires_at=(CASE WHEN expires_at IS NULL OR expires_at+interval '3 days'<=now() THEN now() ELSE expires_at END)+interval '1 month',billing_version=billing_version+1,billing_checked_at=now(),checked_at=CASE WHEN state='active' AND expires_at+interval '3 days'>now() THEN checked_at ELSE NULL END WHERE id=$1 AND billing_version=$2 AND NOT free`, id, period)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return fault.New(409, "域名计费周期已变化，请核对付款记录。")
	}
	return nil
}
func (s Service) renewBalance(ctx context.Context, partner, id string, activationVersion ...int64) error {
	if s.MonthlyFeeCents == 0 {
		return s.renewFree(ctx, partner, id, activationVersion...)
	}
	initial := len(activationVersion) > 0
	// Verify upstream balances before acquiring any finance or order locks.
	candidates, e := orders.Rows(ctx, s.Store.DB, `SELECT o.id FROM partner_earning_orders o JOIN recharge_orders t ON t.order_id=o.id WHERE o.partner_id=$1 AND o.payout_id IS NULL AND `+finance.Eligible("o"), partner)
	if e != nil {
		return e
	}
	verified := map[string]int64{}
	for _, r := range candidates {
		if s.VerifySupplier == nil {
			if initial {
				return fault.New(503, "供应商收益暂时无法核验，请稍后重试。")
			}
			return nil
		}
		key := r["id"].(string)
		if e = s.VerifySupplier(ctx, key); e != nil {
			return e
		}
		var v int64
		if e = s.Store.DB.QueryRow(ctx, `SELECT version FROM orders WHERE id=$1`, key).Scan(&v); e != nil {
			return e
		}
		verified[key] = v
	}
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = lock(ctx, tx); e != nil {
		return e
	}
	d, e := s.binding(ctx, tx, partner, id)
	if e != nil {
		return e
	}
	if initial {
		var paid bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM domain_charges WHERE domain_id=$1 AND partner_id=$2 AND period=$3 AND source='balance' AND state='paid')`, id, partner, activationVersion[0]).Scan(&paid); e != nil {
			return e
		}
		if paid {
			return nil
		}
		if d["free"] == true || d["expires_at"] != nil || d["billing_version"].(int64) != activationVersion[0] {
			return fault.New(409, "域名已开通、无需付费或计费版本已变化，请刷新。")
		}
	} else {
		if d["free"] == true || d["expires_at"] == nil {
			return nil
		}
		if d["expires_at"].(time.Time).After(time.Now()) {
			return nil
		}
	}
	period := d["billing_version"].(int64)
	var pending, held bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM domain_charges WHERE domain_id=$1 AND period=$2 AND state<>'closed'),EXISTS(SELECT 1 FROM partner_earning_orders o JOIN partner_payouts p ON p.id=o.payout_id LEFT JOIN recharge_orders t ON t.order_id=o.id WHERE o.partner_id=$3 AND p.status='paid' AND (o.refund_hold=1 OR (o.payment_status='paid' AND t.financial_hold=1)))`, id, period, partner).Scan(&pending, &held); e != nil {
		return e
	}
	if pending || held {
		if initial {
			return fault.New(409, "存在待确认付款或收益冻结，暂不能余额抵扣；已生成的卓付付款请先核对原单。")
		}
		return nil
	}
	profits, e := orders.Rows(ctx, tx, `SELECT o.id,locked.version,(`+finance.Profit("o")+`) profit,EXISTS(SELECT 1 FROM recharge_orders t WHERE t.order_id=o.id) supplier FROM partner_earning_orders o JOIN orders locked ON locked.id=o.id WHERE o.partner_id=$1 AND o.payout_id IS NULL AND `+finance.Eligible("o")+` ORDER BY o.id FOR UPDATE OF locked`, partner)
	if e != nil {
		return e
	}
	var amount int64
	for _, r := range profits {
		if r["supplier"] == true {
			v, ok := verified[r["id"].(string)]
			if !ok || v != r["version"].(int64) {
				if initial {
					return fault.New(409, "收益已变化，请刷新后重试。")
				}
				return nil
			}
		}
		amount += r["profit"].(int64)
	}
	var debt int64
	if e = tx.QueryRow(ctx, `SELECT COALESCE(sum(amount_cents),0)::bigint FROM partner_debits WHERE partner_id=$1 AND payout_id IS NULL`, partner).Scan(&debt); e != nil {
		return e
	}
	if amount-debt < s.MonthlyFeeCents {
		if initial {
			return fault.New(409, fmt.Sprintf("可提现余额不足 ¥%.2f，可选择卓付支付首月费用。", float64(s.MonthlyFeeCents)/100))
		}
		_, e = tx.Exec(ctx, `UPDATE partner_domains SET billing_checked_at=now() WHERE id=$1`, id)
		if e != nil {
			return e
		}
		return tx.Commit(ctx)
	}
	charge := security.ID()
	if _, e = tx.Exec(ctx, `INSERT INTO domain_charges(id,domain_id,partner_id,hostname,period,source,state,paid_at,amount_cents) VALUES($1,$2,$3,$4,$5,'balance','paid',now(),$6)`, charge, id, partner, d["hostname"], period, s.MonthlyFeeCents); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO partner_debits(id,partner_id,domain_charge_id,amount_cents,created_at) VALUES($1,$2,$3,$4,$5)`, security.ID(), partner, charge, s.MonthlyFeeCents, time.Now().UnixMilli()); e != nil {
		return e
	}
	if e = extend(ctx, tx, id, period); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s Service) selected(ctx context.Context, tx pgx.Tx) (payments.Provider, error) {
	if !s.Payments.Checkout {
		return nil, fault.New(503, "域名支付暂未开放。")
	}
	var ids []string
	if e := tx.QueryRow(ctx, `SELECT enabled_providers FROM payment_settings WHERE id=true FOR SHARE`).Scan(&ids); e != nil {
		return nil, e
	}
	for _, id := range ids {
		if strings.HasPrefix(id, "gatewayb-") {
			if p := s.Payments.Lookup(id); p != nil {
				return p, nil
			}
		}
	}
	return nil, fault.New(503, "请在总后台启用卓付支付渠道。")
}
func (s Service) Payment(ctx context.Context, partner, id, action string, versions ...int64) (map[string]any, error) {
	// An old page may still send "create" during the free promotion. Reuse any
	// existing receipt, but never create a zero-value external cashier.
	if action == "create" && s.MonthlyFeeCents == 0 && len(versions) == 1 {
		var existing bool
		if e := s.Store.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM domain_charges WHERE domain_id=$1 AND partner_id=$2 AND period=$3)`, id, partner, versions[0]).Scan(&existing); e != nil {
			return nil, e
		}
		if !existing {
			action = "balance"
		}
	}
	if action == "balance" {
		if len(versions) != 1 || versions[0] < 0 {
			return nil, fault.New(400, "请提供当前计费版本。")
		}
		if s.RenewEnabled != nil && !s.RenewEnabled() {
			return nil, fault.New(503, "域名服务暂未开放。")
		}
		receipt := func() ([]map[string]any, error) {
			return orders.Rows(ctx, s.Store.DB, `SELECT id,state,qr,amount_cents,hostname,created_at,paid_at,source FROM domain_charges WHERE domain_id=$1 AND partner_id=$2 AND period=$3 AND source IN ('balance','promotion') AND state='paid'`, id, partner, versions[0])
		}
		paid, e := receipt()
		if e != nil {
			return nil, e
		}
		if len(paid) > 0 {
			return map[string]any{"payment": paid[0]}, nil
		}
		if e = s.renewBalance(ctx, partner, id, versions[0]); e != nil {
			return nil, e
		}
		paid, e = receipt()
		if e != nil {
			return nil, e
		}
		if len(paid) != 1 {
			return nil, fault.New(409, "域名扣款结果待核对，请刷新。")
		}
		return map[string]any{"payment": paid[0]}, nil
	}
	if action != "create" && action != "query" {
		return nil, fault.New(400, "支付操作无效。")
	}
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	if e = lock(ctx, tx); e != nil {
		return nil, e
	}
	d, e := s.binding(ctx, tx, partner, id)
	if e != nil {
		return nil, e
	}
	if d["free"] == true {
		return nil, fault.New(400, "该域名免费，无需付费。")
	}
	period := d["billing_version"].(int64)
	if action == "create" {
		if len(versions) != 1 || versions[0] < 0 {
			return nil, fault.New(400, "请提供当前计费版本。")
		}
		period = versions[0]
	}

	// Query returns the latest receipt, including after its cycle was applied.
	query := `SELECT * FROM domain_charges WHERE domain_id=$1 AND partner_id=$2 ORDER BY created_at DESC LIMIT 1`
	if action == "create" {
		query = `SELECT * FROM domain_charges WHERE domain_id=$1 AND partner_id=$2 AND period=` + "$3"
	}
	args := []any{id, partner}
	if action == "create" {
		args = append(args, period)
	}
	list, e := orders.Rows(ctx, tx, query, args...)
	if e != nil {
		return nil, e
	}
	if len(list) == 0 && action == "create" {
		// Only initial activation uses GATEWAYB. A second external cashier can stay
		// pending indefinitely (GATEWAYB has no confirmed-close API), which would
		// otherwise block the required balance renewal when this domain expires.
		if d["expires_at"] != nil {
			return nil, fault.New(409, "该域名已开通，到期将自动从可提现余额扣款续费，无需另开付款单。")
		}
		if period != d["billing_version"].(int64) {
			return nil, fault.New(409, "计费周期已变化，请刷新后重试。")
		}
		if s.MonthlyFeeCents == 0 {
			return nil, fault.New(409, "当前免费，请刷新后免费开通。")
		}
		u, e := url.Parse(s.Payments.NotifyOrigin)
		if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Path != "" {
			return nil, fault.New(503, "支付回调来源未配置。")
		}
		p, e := s.selected(ctx, tx)
		if e != nil {
			return nil, e
		}
		list, e = orders.Rows(ctx, tx, `INSERT INTO domain_charges(id,domain_id,partner_id,hostname,period,source,provider,account,amount_cents) VALUES($1,$2,$3,$4,$5,'gatewayb',$6,$7,$8) RETURNING *`, "DOM"+strings.ReplaceAll(security.ID(), "-", ""), id, partner, d["hostname"], period, p.ID(), p.Account(), s.MonthlyFeeCents)
		if e != nil {
			return nil, e
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	if len(list) == 0 {
		return map[string]any{"payment": nil}, nil
	}
	c := list[0]
	if c["state"] == "pending" {
		lease := security.ID()
		tag, e := s.Store.DB.Exec(ctx, `UPDATE domain_charges SET lease_token=$1,lease_until=now()+interval '1 minute' WHERE id=$2 AND (lease_until IS NULL OR lease_until<now())`, lease, c["id"])
		if e != nil {
			return nil, e
		}
		if tag.RowsAffected() != 1 {
			return nil, fault.New(409, "付款正在确认，请稍后重试。")
		}
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, _ = s.Store.DB.Exec(ctx, `UPDATE domain_charges SET lease_token=NULL,lease_until=NULL WHERE id=$1 AND lease_token=$2`, c["id"], lease)
		}()
		p, e := s.Payments.Resolve(c["provider"].(string), c["account"].(string))
		if e != nil {
			return nil, e
		}
		o := payments.Order{ID: c["id"].(string), Amount: c["amount_cents"].(int64), Subject: "商店域名接入 · 一个月", Expires: time.Now().Add(15 * time.Minute).UnixMilli()}
		if action == "create" && c["qr"] == nil {
			qr, e := p.Create(ctx, o, s.Payments.NotifyOrigin+"/api/domain-billing/notify/"+p.ID())
			if e != nil {
				return nil, e
			}
			if _, e = s.Store.DB.Exec(ctx, `UPDATE domain_charges SET qr=$1,updated_at=now() WHERE id=$2 AND state='pending' AND lease_token=$3`, qr, o.ID, lease); e != nil {
				return nil, e
			}
		} else {
			r, e := p.Query(ctx, o)
			if e != nil {
				return nil, e
			}
			if e = s.Apply(ctx, p, r); e != nil {
				return nil, e
			}
		}
	}
	result, e := orders.Rows(ctx, s.Store.DB, `SELECT id,state,qr,amount_cents,hostname,created_at,paid_at,source FROM domain_charges WHERE id=$1`, c["id"])
	if e != nil {
		return nil, e
	}
	return map[string]any{"payment": result[0]}, nil
}
func (s Service) Apply(ctx context.Context, p payments.Provider, r payments.Result) error {
	if r.Status != "paid" {
		return nil
	}
	if r.Amount <= 0 || r.TradeNo == "" {
		return fault.New(409, "域名付款金额或流水不匹配。")
	}
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = lock(ctx, tx); e != nil {
		return e
	}
	list, e := orders.Rows(ctx, tx, `SELECT * FROM domain_charges WHERE id=$1 AND source='gatewayb' FOR UPDATE`, r.OrderID)
	if e != nil {
		return e
	}
	if len(list) != 1 {
		return fault.New(404, "域名付款单不存在。")
	}
	c := list[0]
	if c["provider"] != p.ID() || c["account"] != p.Account() || c["amount_cents"] != r.Amount || (c["trade_no"] != nil && c["trade_no"] != r.TradeNo) {
		return fault.New(409, "域名付款身份不匹配。")
	}
	if c["state"] == "paid" {
		return tx.Commit(ctx)
	}
	if e = extend(ctx, tx, c["domain_id"].(string), c["period"].(int64)); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE domain_charges SET state='paid',trade_no=$1,paid_at=now(),updated_at=now(),qr=NULL WHERE id=$2`, r.TradeNo, r.OrderID); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s Service) Notify(req *http.Request, id string) bool {
	p := s.Payments.Lookup(id)
	if p == nil || !strings.HasPrefix(id, "gatewayb-") {
		return false
	}
	r, e := p.Notification(req)
	return e == nil && s.Apply(req.Context(), p, r) == nil
}
func (s Service) Reconcile(ctx context.Context) error {
	pending, e := orders.Rows(ctx, s.Store.DB, `SELECT * FROM domain_charges WHERE state='pending' AND source='gatewayb' AND (lease_until IS NULL OR lease_until<now()) ORDER BY updated_at LIMIT 20`)
	if e != nil {
		return e
	}
	var first error
	for _, c := range pending {
		p, err := s.Payments.Resolve(c["provider"].(string), c["account"].(string))
		if err == nil {
			r, x := p.Query(ctx, payments.Order{ID: c["id"].(string), Amount: c["amount_cents"].(int64)})
			err = x
			if err == nil {
				err = s.Apply(ctx, p, r)
			}
		}
		if err != nil && first == nil {
			first = err
		}
		_, _ = s.Store.DB.Exec(ctx, `UPDATE domain_charges SET updated_at=now() WHERE id=$1`, c["id"])
	}
	if s.RenewEnabled != nil && !s.RenewEnabled() {
		return first
	}
	due, e := orders.Rows(ctx, s.Store.DB, `SELECT id,partner_id FROM partner_domains WHERE NOT free AND expires_at<=now() AND state<>'deleting' ORDER BY billing_checked_at NULLS FIRST LIMIT 100`)
	if e != nil {
		return e
	}
	for _, d := range due {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if e = s.renewBalance(ctx, d["partner_id"].(string), d["id"].(string)); e != nil && first == nil {
			first = e
		}
		// Rotate every attempted binding, including those held by unresolved cashiers,
		// so a large group of blocked domains cannot starve later renewals.
		_, _ = s.Store.DB.Exec(ctx, `UPDATE partner_domains SET billing_checked_at=now() WHERE id=$1`, d["id"])
	}
	return first
}
