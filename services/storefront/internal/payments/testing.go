package payments

import (
	"context"
	"github.com/jackc/pgx/v5"
	"gptibo/storefront/internal/auth"
	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/orders"
	"gptibo/storefront/internal/security"
	"strings"
	"time"
)

func TestOrder(o orders.Order) bool {
	return o.Test == 1 && o.Mode == "live" && o.Price == 1 && o.PartnerID == nil
}
func (s Service) TestCreate(ctx context.Context, a *auth.Service, token, key, email string, enabled bool) (string, bool, error) {
	if !enabled || s.Provider == nil {
		return "", false, fault.New(503, "支付联调尚未启用。")
	}
	if !security.ValidID(key) {
		return "", false, fault.New(400, "请求编号无效。")
	}
	email, e := auth.Email(email)
	if e != nil {
		return "", false, e
	}
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return "", false, e
	}
	defer tx.Rollback(ctx)
	p, e := a.Require(ctx, tx, "admin", token)
	if e != nil {
		return "", false, e
	}
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(903501)`); e != nil {
		return "", false, e
	}
	owner := security.Hash("admin-payment-test:" + p.Subject)
	var id, existingEmail string
	e = tx.QueryRow(ctx, `SELECT id,customer_email FROM orders WHERE owner_hash=$1 AND idempotency_key=$2 AND is_test=1`, owner, key).Scan(&id, &existingEmail)
	if e == nil {
		if existingEmail != email {
			return "", false, orders.Conflict()
		}
		return id, false, tx.Commit(ctx)
	}
	if e != pgx.ErrNoRows {
		return "", false, e
	}
	var active, plan bool
	e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM orders WHERE is_test=1 AND status IN ('pending','session') AND refunded_cents=0),EXISTS(SELECT 1 FROM catalog_plans WHERE id='plus')`).Scan(&active, &plan)
	if e != nil {
		return "", false, e
	}
	if active || !plan {
		return "", false, fault.New(409, "请先关闭或退回已有测试单，并确认 Plus 套餐存在。")
	}
	id = "RECHARGE-TEST-" + strings.ToUpper(strings.ReplaceAll(security.ID(), "-", ""))
	event := security.ID()
	now := time.Now().UnixMilli()
	_, e = tx.Exec(ctx, `INSERT INTO orders(id,owner_hash,idempotency_key,plan,plan_name,price_cents,mode,status,is_test,customer_email,created_at,updated_at,last_event_id) VALUES($1,$2,$3,'plus','支付联调测试',1,'live','pending',1,$4,$5,$5,$6)`, id, owner, key, email, now, event)
	if e != nil {
		return "", false, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO order_events(id,order_id,actor,action,to_status,note,order_version,created_at) VALUES($1,$2,'admin','create-test','pending','真实支付联调 ¥0.01，不交付商品、不计入营业收入或代理收益',0,$3)`, event, id, now)
	if e != nil {
		return "", false, e
	}
	return id, true, tx.Commit(ctx)
}
func (s Service) TestView(ctx context.Context, id string) (map[string]any, error) {
	o, e := orders.Get(ctx, s.Store.DB, id, nil, false)
	if e != nil {
		return nil, e
	}
	if !TestOrder(o) {
		return nil, orders.Conflict()
	}
	pay, e := orders.Rows(ctx, s.Store.DB, `SELECT provider,state status,CASE WHEN state='pending' AND expires_at>$1 THEN qr ELSE NULL END qr,expires_at "expiresAt",notified_at "notifiedAt",trade_no "tradeNo" FROM payments WHERE order_id=$2`, time.Now().UnixMilli(), id)
	if e != nil {
		return nil, e
	}
	refunds, e := orders.Rows(ctx, s.Store.DB, `SELECT id,status,version FROM refund_cases WHERE order_id=$1 ORDER BY created_at DESC LIMIT 1`, id)
	if e != nil {
		return nil, e
	}
	events, e := orders.Rows(ctx, s.Store.DB, `SELECT action,note,created_at FROM order_events WHERE order_id=$1 ORDER BY order_version`, id)
	if e != nil {
		return nil, e
	}
	var payment, refund any
	if len(pay) > 0 {
		payment = pay[0]
	}
	if len(refunds) > 0 {
		refund = refunds[0]
	}
	return map[string]any{"id": o.ID, "email": o.Email, "amountCents": o.Price, "paymentStatus": o.Payment, "status": o.Status, "refundedCents": o.Refunded, "refundHold": o.Hold != 0, "createdAt": o.Created, "payment": payment, "refund": refund, "events": events}, nil
}
func (s Service) TestCloseUncreated(ctx context.Context, id string) (bool, error) {
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return false, e
	}
	defer tx.Rollback(ctx)
	o, e := orders.Get(ctx, tx, id, nil, true)
	if e != nil {
		return false, e
	}
	if !TestOrder(o) {
		return false, orders.Conflict()
	}
	var exists bool
	e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM payments WHERE order_id=$1)`, id).Scan(&exists)
	if e != nil {
		return false, e
	}
	if exists || o.Status != "pending" || o.Payment != "unpaid" {
		return false, tx.Commit(ctx)
	}
	next := o
	next.Status = "cancelled"
	if _, e = orders.Change(ctx, tx, o, next, "admin", "test-closed", "关闭尚未向渠道发起付款的测试单"); e != nil {
		return false, e
	}
	return true, tx.Commit(ctx)
}
