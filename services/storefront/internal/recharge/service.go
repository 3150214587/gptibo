package recharge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/orders"
	"gptibo/storefront/internal/security"
	"gptibo/storefront/internal/store"
)

type Service struct {
	Store                 *store.Store
	Client                *Client
	Checkout              bool
	Products              map[string]string
	Notify, WebhookSecret string
	CredentialSecret      string
	Checker               SessionChecker
}
type Row struct {
	OrderID        string  `json:"order_id"`
	Account        string  `json:"account"`
	Product        string  `json:"product_code"`
	Collection     string  `json:"collection_mode"`
	Body           string  `json:"create_body"`
	Remote         *string `json:"remote_id"`
	Payment        string  `json:"payment_status"`
	URL            *string `json:"payment_url"`
	QR             *string `json:"payment_qr"`
	Expires        *int64  `json:"expires_at"`
	Attempted      *int64  `json:"attempted_at"`
	Updated        int64   `json:"updated_at"`
	Lease          *string `json:"lease_token"`
	Until          int64   `json:"lease_until"`
	Refunded       int64   `json:"refunded_cents"`
	Hold           int     `json:"financial_hold"`
	NotifyVersion  int64   `json:"notify_version"`
	CheckedVersion int64   `json:"checked_notify_version"`
}

func Find(ctx context.Context, q orders.Query, id string) (Row, error) {
	var r Row
	var raw []byte
	e := q.QueryRow(ctx, `SELECT to_jsonb(t) FROM recharge_orders t WHERE order_id=$1`, id).Scan(&raw)
	if e != nil {
		return r, e
	}
	e = json.Unmarshal(raw, &r)
	return r, e
}
func Raw(v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return strings.NewReplacer(`\u2028`, "\u2028", `\u2029`, "\u2029").Replace(strings.TrimSuffix(b.String(), "\n"))
}

type Snapshot struct{ Account, Product, Notify string }

func (s *Service) Quote(ctx context.Context, plan string, price int64, cost *int64) (Snapshot, error) {
	var snap Snapshot
	if !s.Checkout || s.Client == nil || s.Products[plan] == "" {
		return snap, fault.New(409, "该服务暂未开放购买。")
	}
	var products []Product
	if e := s.Client.Request(ctx, "/products", nil, "", &products); e != nil {
		return snap, e
	}
	ok := false
	for _, p := range products {
		if p.Code != s.Products[plan] {
			continue
		}
		supply, e1 := Money(p.Supply)
		maxSale, e2 := Money(p.MaxSale)
		ok = e1 == nil && e2 == nil && p.Available && p.Currency == "CNY" && p.Quantity >= 1 && slices.Contains(p.Modes, "auto_recharge") && price >= supply && price <= maxSale && (cost == nil || *cost >= supply)
	}
	if !ok {
		return snap, fault.New(409, "套餐暂不可售或价格已更新，请刷新商品。")
	}
	var methods []struct {
		Code string `json:"code"`
	}
	if e := s.Client.Request(ctx, "/payment-methods", nil, "", &methods); e != nil {
		return snap, e
	}
	ok = false
	for _, m := range methods {
		if m.Code == "alipay" {
			ok = true
		}
	}
	if !ok {
		return snap, fault.New(409, "支付宝通道暂不可用。")
	}
	return Snapshot{s.Client.Account(), s.Products[plan], s.Notify}, nil
}
func (s *Service) Snapshot(ctx context.Context, tx pgx.Tx, o orders.Order, in orders.CreateInput) error {
	supplyCost, e := orders.SupplyCost(ctx, tx, o.ID)
	if e != nil {
		return e
	}
	snap, e := s.Quote(ctx, o.Plan, o.Price, supplyCost)
	if e != nil {
		return e
	}
	body := Raw(struct {
		Merchant   string `json:"merchant_order_no"`
		Product    string `json:"product_code"`
		Quantity   int    `json:"quantity"`
		Sale       string `json:"sale_amount"`
		Collection string `json:"collection_mode"`
		Delivery   string `json:"delivery_mode"`
		Channel    string `json:"payment_channel"`
		Notify     string `json:"notify_url"`
	}{o.ID, snap.Product, 1, Amount(o.Price), "platform_collect", "auto_recharge", "alipay", snap.Notify})
	_, e = tx.Exec(ctx, `INSERT INTO recharge_orders(order_id,account,product_code,collection_mode,create_body,updated_at) VALUES($1,$2,$3,'platform_collect',$4,$5)`, o.ID, snap.Account, snap.Product, body, time.Now().UnixMilli())
	return e
}
func (s *Service) locked(ctx context.Context, id string, run func(Row) error) error {
	if s.Client == nil {
		return fault.New(503, "充值服务未配置。")
	}
	// Match the order -> supplier lock order used by refunds and administrative actions.
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	o, e := orders.Get(ctx, tx, id, nil, true)
	if e != nil {
		return e
	}
	r, e := Find(ctx, tx, id)
	if e != nil {
		return e
	}
	if o.Mode != "live" || o.Test != 0 || r.Account != s.Client.Account() {
		return fault.New(503, "请恢复原充值服务账号后处理。")
	}
	now, lease := time.Now().UnixMilli(), security.ID()
	tag, e := tx.Exec(ctx, `UPDATE recharge_orders SET lease_token=$1,lease_until=$2 WHERE order_id=$3 AND lease_until<=$4`, lease, now+120000, id, now)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return orders.Conflict()
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	r.Lease = &lease
	r.Until = now + 120000
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = s.Store.DB.Exec(c, `UPDATE recharge_orders SET lease_token=NULL,lease_until=0,updated_at=CASE WHEN notify_version>checked_notify_version THEN 0 ELSE $1::bigint END WHERE order_id=$2 AND lease_token=$3`, time.Now().UnixMilli(), id, lease)
	}()
	return run(r)
}
func (s *Service) fence(ctx context.Context, tx pgx.Tx, r Row) (Row, error) {
	fresh, e := Find(ctx, tx, r.OrderID)
	if e != nil {
		return fresh, e
	}
	if r.Lease == nil || fresh.Lease == nil || *r.Lease != *fresh.Lease || fresh.Until <= time.Now().UnixMilli() {
		return fresh, orders.Conflict()
	}
	return fresh, nil
}
func deadline(o orders.Order, r Row) int64 {
	d := o.Created + 900000
	if r.Expires != nil {
		d = min(d, *r.Expires)
	}
	return d
}
func (s *Service) expire(ctx context.Context, id string) error {
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	o, e := orders.Get(ctx, tx, id, nil, true)
	if e != nil {
		return e
	}
	r, e := Find(ctx, tx, id)
	if e != nil {
		return e
	}
	if o.Mode == "live" && o.Test == 0 && o.Status == "pending" && o.Payment == "unpaid" && r.Payment == "pending" && deadline(o, r) <= time.Now().UnixMilli() {
		next := o
		next.Status = "cancelled"
		if _, e = orders.Change(ctx, tx, o, next, "system", "payment-expired", "订单付款有效期已结束，继续核对已发生的付款"); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE recharge_orders SET payment_qr=NULL WHERE order_id=$1`, id); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}
func validOrder(r Row, o orders.Order, v RemoteOrder) error {
	var body struct {
		Channel string `json:"payment_channel"`
	}
	e := json.Unmarshal([]byte(r.Body), &body)
	sale, e2 := Money(v.Sale)
	if e != nil || e2 != nil || !remoteID.MatchString(v.ID) || len(v.ID) > 100 || (r.Remote != nil && *r.Remote != v.ID) || v.Merchant != o.ID || v.Product != r.Product || v.Quantity != 1 || v.Currency != "CNY" || v.Collection != "platform_collect" || v.Scope != "retail" || v.Delivery != "auto_recharge" || sale != o.Price || !paymentStatus(v.Payment) || body.Channel != "alipay" || (v.Channel != "" && v.Channel != "alipay") {
		return errors.New("supplier order identity mismatch")
	}
	return nil
}
func paymentAdvances(from, to string) bool {
	return from == to || from == "pending" || (slices.Contains([]string{"closed", "expired"}, from) && slices.Contains([]string{"paid", "partially_refunded", "refunded"}, to)) || (from == "paid" && slices.Contains([]string{"partially_refunded", "refunded"}, to)) || (from == "partially_refunded" && to == "refunded")
}
func (s *Service) apply(ctx context.Context, r Row, v RemoteOrder, cashier bool) error {
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	o, e := orders.Get(ctx, tx, r.OrderID, nil, true)
	if e != nil {
		return e
	}
	r, e = s.fence(ctx, tx, r)
	if e != nil {
		return e
	}
	if e = validOrder(r, o, v); e != nil {
		return e
	}
	exp, e := time.Parse(time.RFC3339Nano, v.Expires)
	if e != nil {
		return errors.New("invalid supplier expiry")
	}
	expiry := exp.UnixMilli()
	supply, e1 := Money(v.Supply)
	margin, e2 := Margin(v.Margin)
	refundValue := v.Refunded
	if refundValue == "" {
		refundValue = "0.00"
	}
	refunded, e3 := Money(refundValue)
	if e1 != nil || e2 != nil || e3 != nil || supply > o.Price || margin < -supply || margin > o.Price-refunded || refunded > o.Price || (slices.Contains([]string{"pending", "closed", "expired", "paid"}, v.Payment) && refunded > 0) || (v.Payment == "refunded" && refunded != o.Price) || (v.Payment == "partially_refunded" && (refunded == 0 || refunded == o.Price)) {
		return errors.New("supplier financial mismatch")
	}
	if !paymentAdvances(r.Payment, v.Payment) || refunded < r.Refunded {
		return tx.Commit(ctx)
	}
	var link *string
	var linkError error
	if v.Payment == "pending" {
		u, err := s.Client.PaymentLink(v.URL, v.ID)
		linkError = err
		if err == nil && u != nil {
			value := u.String()
			link = &value
		}
	}
	payable := v.Payment == "pending" && min(o.Created+900000, expiry) > time.Now().UnixMilli() && o.Status == "pending" && o.Payment == "unpaid" && o.Blocked == 0 && o.Hold == 0 && o.Refunded == 0
	// Reuse only the QR bound to this unchanged, still-payable cashier.
	// Status reconciliation must not depend on another cashier request succeeding.
	cachedQR := r.QR != nil && *r.QR != "" && r.URL != nil && link != nil && *r.URL == *link && r.Expires != nil && *r.Expires == expiry
	_, e = tx.Exec(ctx, `UPDATE recharge_orders SET remote_id=$1,payment_status=$2,payment_qr=CASE WHEN $3 AND payment_url IS NOT DISTINCT FROM $4 AND expires_at=$5 THEN payment_qr ELSE NULL END,payment_url=$4,expires_at=$5,supply_cents=$6,merchant_margin_cents=$7,reported_margin_cents=$8,refunded_cents=$9,verified_at=$10,financial_hold=CASE WHEN $9::bigint=$11::bigint THEN 0 ELSE 1 END WHERE order_id=$12`, v.ID, v.Payment, payable, link, expiry, supply, max(int64(0), margin), margin, refunded, time.Now().UnixMilli(), o.Refunded, o.ID)
	if e != nil {
		return e
	}
	next := o
	paid := slices.Contains([]string{"paid", "partially_refunded", "refunded"}, v.Payment)
	closed := v.Payment == "closed" || v.Payment == "expired"
	if o.Payment == "unpaid" && (paid || closed) {
		if paid {
			next.Payment = "paid"
			if o.Status == "pending" || o.Status == "cancelled" {
				next.Status = "session"
			}
		} else if o.Status == "pending" {
			next.Status = "cancelled"
		}
		action, note := "payment-closed", "支付渠道已确认订单关闭"
		if paid {
			action, note = "payment-confirmed", "支付渠道已核验收款，等待提交充值资料"
		}
		next, e = orders.Change(ctx, tx, o, next, "system", action, note)
		if e != nil {
			return e
		}
		o = next
	}
	if v.Fulfillment == "succeeded" && v.Payment == "paid" && o.Payment == "paid" && o.Status != "success" && o.Hold == 0 && o.Refunded == 0 && refunded == 0 {
		next = o
		next.Status = "success"
		if _, e = orders.Change(ctx, tx, o, next, "system", "platform-fulfillment-completed", "平台原订单已核验充值完成"); e != nil {
			return e
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	if linkError != nil {
		return linkError
	}
	if payable && cashier && !cachedQR {
		qr, e := s.Client.QR(ctx, v, min(o.Created+900000, expiry))
		if e != nil {
			_, _ = s.Store.DB.Exec(ctx, `UPDATE recharge_orders SET payment_qr=NULL WHERE order_id=$1 AND lease_token=$2`, o.ID, r.Lease)
			return e
		}
		_, e = s.Store.DB.Exec(ctx, `UPDATE recharge_orders SET payment_qr=$1 WHERE order_id=$2 AND remote_id=$3 AND lease_token=$4 AND lease_until>$5 AND payment_status='pending' AND expires_at>$5 AND EXISTS(SELECT 1 FROM orders WHERE id=$2 AND status='pending' AND payment_status='unpaid' AND redemption_blocked=0 AND refund_hold=0 AND refunded_cents=0 AND created_at>$6)`, qr, o.ID, v.ID, r.Lease, time.Now().UnixMilli(), time.Now().UnixMilli()-900000)
		return e
	}
	return nil
}
func (s *Service) sync(ctx context.Context, r Row, queryOnly bool) error {
	if e := s.expire(ctx, r.OrderID); e != nil {
		return e
	}
	o, e := orders.Get(ctx, s.Store.DB, r.OrderID, nil, false)
	if e != nil {
		return e
	}
	var v RemoteOrder
	if r.Remote == nil {
		if queryOnly {
			return orders.Conflict()
		}
		if r.Attempted == nil && o.Status == "cancelled" {
			return nil
		}
		recovering := r.Attempted != nil && o.Status == "cancelled" && deadline(o, r) <= time.Now().UnixMilli()
		if o.Blocked != 0 || o.Hold != 0 || o.Refunded != 0 || (o.Status != "pending" && !recovering) || (r.Attempted == nil && !s.Checkout) {
			return orders.Conflict()
		}
		tag, e := s.Store.DB.Exec(ctx, `UPDATE recharge_orders SET attempted_at=COALESCE(attempted_at,$1) WHERE order_id=$2 AND lease_token=$3 AND lease_until>$1`, time.Now().UnixMilli(), o.ID, r.Lease)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return orders.Conflict()
		}
		e = s.Client.Request(ctx, "/orders", &r.Body, "order:"+o.ID, &v)
	} else {
		e = s.Client.Request(ctx, "/orders/"+*r.Remote, nil, "", &v)
	}
	if e != nil {
		return e
	}
	if e = s.apply(ctx, r, v, !queryOnly); e != nil {
		return e
	}
	fresh, e := Find(ctx, s.Store.DB, o.ID)
	if e != nil {
		return e
	}
	attempt, e := latest(ctx, s.Store.DB, o.ID)
	if e != nil {
		return e
	}
	if e = s.syncLatest(ctx, fresh, attempt, v.Fulfillment != ""); e != nil {
		return e
	}

	if !queryOnly {
		_, e = s.Store.DB.Exec(ctx, `UPDATE recharge_orders SET checked_notify_version=greatest(checked_notify_version,$1) WHERE order_id=$2 AND lease_token=$3`, r.NotifyVersion, o.ID, r.Lease)
	}
	return e
}
func (s *Service) Sync(ctx context.Context, id string, force bool) error {
	r, e := Find(ctx, s.Store.DB, id)
	if e == pgx.ErrNoRows {
		return nil
	}
	if e != nil {
		return e
	}
	if e = s.expire(ctx, id); e != nil {
		return e
	}
	if !force {
		needsQR := r.Payment == "pending" && (r.Remote == nil || r.QR == nil)
		if r.Until > time.Now().UnixMilli() || (!needsQR && time.Now().UnixMilli()-r.Updated < 3000) {
			return nil
		}
		o, e := orders.Get(ctx, s.Store.DB, id, nil, false)
		if e != nil {
			return e
		}
		if r.Remote != nil && r.Updated != 0 && o.Payment == "paid" && (o.Status == "session" || o.Status == "failed") {
			a, err := latest(ctx, s.Store.DB, id)
			if err != nil {
				return err
			}
			if a == nil || a.Status != "unknown" {
				return nil
			}
		}
	}
	return s.locked(ctx, id, func(r Row) error { return s.sync(ctx, r, false) })
}
func (s *Service) Verify(ctx context.Context, id string) error {
	return s.locked(ctx, id, func(r Row) error { return s.sync(ctx, r, true) })
}
func (s *Service) Reconcile(ctx context.Context) error {
	result, e := s.ReconcileReport(ctx)
	if e == nil && result["failed"] > 0 {
		return errors.New("supplier reconciliation incomplete")
	}
	return e
}
func (s *Service) ReconcileReport(ctx context.Context) (map[string]int, error) {
	list, e := orders.Rows(ctx, s.Store.DB, `SELECT t.order_id FROM recharge_orders t JOIN orders o ON o.id=t.order_id WHERE t.lease_until<=$1 AND (t.notify_version>t.checked_notify_version OR (o.refunded_cents<o.price_cents AND o.status IN('pending','session','processing','failed','success')) OR (o.status='cancelled' AND (t.expires_at>=$2 OR(t.remote_id IS NULL AND t.attempted_at>=$2))) OR EXISTS(SELECT 1 FROM refund_cases r WHERE r.order_id=o.id AND r.status='approved')) ORDER BY t.updated_at LIMIT 10`, time.Now().UnixMilli(), time.Now().UnixMilli()-86400000)
	if e != nil {
		return nil, e
	}
	result := map[string]int{"checked": 0, "failed": 0}
	for _, r := range list {
		if e = s.Sync(ctx, r["order_id"].(string), true); e != nil {
			result["failed"]++
			// A failed historical row must not permanently occupy the oldest batch.
			// Use a fresh bounded context even when the supplier consumed the job timeout.
			cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_, _ = s.Store.DB.Exec(cleanup, `UPDATE recharge_orders SET updated_at=$1 WHERE order_id=$2 AND lease_until<=$1`, time.Now().UnixMilli(), r["order_id"])
			cancel()
		} else {
			result["checked"]++
		}
	}
	return result, nil
}
