package procurement

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"gptibo/storefront/internal/auth"
	"gptibo/storefront/internal/catalog"
	"gptibo/storefront/internal/delivery"
	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/orders"
	"gptibo/storefront/internal/security"
	"gptibo/storefront/internal/store"
	"gptibo/storefront/internal/worker"
)

type Service struct {
	Store                                               *store.Store
	Auth                                                *auth.Service
	Delivery                                            delivery.Service
	AccountSupplier                                             *AccountSupplier
	DeliverySupplier                                             *DeliverySupplier
	GmailEnabled, NYEnabled, EmailEnabled, PaymentReady bool
	GoodsID, MaxCents                                   int64
	FX                                                  float64
}
type Purchase struct {
	ID           string  `json:"order_id"`
	Goods        int64   `json:"goods_id"`
	GoodsName    string  `json:"goods_name"`
	Base         string  `json:"api_base"`
	App          string  `json:"app_id"`
	Ceiling      int64   `json:"max_cost_cents"`
	Instructions string  `json:"instructions"`
	State        string  `json:"state"`
	Supplier     *string `json:"supplier_order"`
	Cost         *int64  `json:"actual_cost_cents"`
	Cards        *string `json:"cards"`
	Attempted    *int64  `json:"attempted_at"`
	Lease        *string `json:"lease_token"`
	Until        int64   `json:"lease_until"`
	Updated      int64   `json:"updated_at"`
	Service      string  `json:"service_id"`
	Fingerprint  string  `json:"credential_hash"`
	Key          string  `json:"request_key"`
	Body         string  `json:"request_body"`
	MaxUSD       int64   `json:"max_usd_cents"`
	FX           float64 `json:"cny_per_usd"`
	USD          *int64  `json:"actual_usd_cents"`
	USDMicros    *int64  `json:"actual_usd_micros"`
	Value        *string `json:"value"`
	Next         int64   `json:"next_check_at"`
}

func table(kind string) string {
	if kind == "gmail" {
		return "account_supplier_orders"
	}
	if kind == "delivery_supplier" {
		return "delivery_supplier_orders"
	}
	panic("invalid procurement kind")
}
func find(ctx context.Context, q orders.Query, kind, id string) (Purchase, error) {
	var p Purchase
	var b []byte
	e := q.QueryRow(ctx, `SELECT to_jsonb(p) FROM `+table(kind)+` p WHERE order_id=$1`, id).Scan(&b)
	if e == nil {
		e = json.Unmarshal(b, &p)
	}
	return p, e
}
func Kind(plan string) string {
	if plan == "gmail-account" {
		return "gmail"
	}
	if _, ok := mappings[plan]; ok {
		return "delivery_supplier"
	}
	return ""
}
func (s *Service) Enabled(plan string) bool {
	if !s.EmailEnabled || !s.PaymentReady || s.Delivery.Sender == nil {
		return false
	}
	switch Kind(plan) {
	case "gmail":
		return s.GmailEnabled && s.AccountSupplier != nil && s.GoodsID > 0 && s.MaxCents > 0
	case "delivery_supplier":
		return s.NYEnabled && s.DeliverySupplier != nil && s.FX > 0 && s.FX <= 100
	}
	return false
}
func (s *Service) quote(ctx context.Context, plan string, sale int64, cost *int64) (Goods, error) {
	if !s.Enabled(plan) {
		return Goods{}, fault.New(409, "自动发货暂未开放。")
	}
	ceiling := s.MaxCents
	if Kind(plan) == "delivery_supplier" {
		ceiling = int64(math.Ceil(float64(mappings[plan].Ceiling) * s.FX))
	}
	if ceiling >= sale || (cost != nil && (ceiling >= *cost || *cost > sale)) {
		return Goods{}, fault.New(409, "采购成本与售价配置不匹配。")
	}
	if Kind(plan) == "gmail" {
		g, e := s.AccountSupplier.Goods(ctx, s.GoodsID)
		if e == nil {
			e = validateGoods(g, ceiling)
		}
		return g, e
	}
	products, e := s.DeliverySupplier.Products(ctx)
	if e == nil {
		e = validateNY(products, mappings[plan].ID, mappings[plan].Ceiling)
	}
	return Goods{}, e
}
func (s *Service) Snapshot(ctx context.Context, tx pgx.Tx, o orders.Order, _ orders.CreateInput) error {
	supplyCost, e := orders.SupplyCost(ctx, tx, o.ID)
	if e != nil {
		return e
	}
	g, e := s.quote(ctx, o.Plan, o.Price, supplyCost)
	if errors.Is(e, ErrOutOfStock) {
		return fault.New(409, "商品暂时缺货，请稍后再试。")
	}
	if e != nil {
		return e
	}
	var instructions string
	if e = tx.QueryRow(ctx, `SELECT delivery_instructions FROM catalog_plans WHERE id=$1`, o.Plan).Scan(&instructions); e != nil {
		return e
	}
	if Kind(o.Plan) == "gmail" {
		_, e = tx.Exec(ctx, `INSERT INTO account_supplier_orders(order_id,goods_id,goods_name,api_base,app_id,max_cost_cents,instructions,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, o.ID, g.ID, g.Name, s.AccountSupplier.Base, s.AccountSupplier.App, s.MaxCents, instructions, time.Now().UnixMilli())
	} else {
		m := mappings[o.Plan]
		body := raw(map[string]any{"service_id": m.ID, "quantity": 1, "precheck": true, "include_proofs": true})
		_, e = tx.Exec(ctx, `INSERT INTO delivery_supplier_orders(order_id,service_id,credential_hash,request_key,request_body,max_usd_cents,cny_per_usd,instructions,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, o.ID, m.ID, security.Hash(s.DeliverySupplier.Key), "ymx_"+o.ID, body, m.Ceiling, s.FX, instructions, time.Now().UnixMilli())
	}
	return e
}
func (s *Service) Availability(_ context.Context, plans []catalog.Plan) []catalog.Plan {
	for i := range plans {
		p := &plans[i]
		if Kind(p.ID) == "" {
			continue
		}
		if !s.Enabled(p.ID) {
			p.Enabled = 0
		}
	}
	return plans
}
func eligible(o orders.Order) bool {
	return o.Mode == "live" && o.Test == 0 && o.Payment == "paid" && o.Hold == 0 && o.Refunded == 0 && o.Blocked == 0 && (o.Status == "session" || o.Status == "processing")
}
func (s *Service) account(kind string, p Purchase) error {
	if kind == "gmail" {
		if s.AccountSupplier != nil && p.Base == s.AccountSupplier.Base && p.App == s.AccountSupplier.App {
			return nil
		}
	} else if s.DeliverySupplier != nil && p.Fingerprint == security.Hash(s.DeliverySupplier.Key) {
		return nil
	}
	return fault.New(409, "采购账户已变化，请恢复原供应商凭据后核对。")
}
func (s *Service) fence(ctx context.Context, tx pgx.Tx, kind string, p Purchase) (orders.Order, Purchase, error) {
	o, e := orders.Get(ctx, tx, p.ID, nil, true)
	if e != nil {
		return o, p, e
	}
	fresh, e := find(ctx, tx, kind, p.ID)
	if e != nil {
		return o, p, e
	}
	if !eligible(o) || p.Lease == nil || fresh.Lease == nil || *p.Lease != *fresh.Lease || fresh.Until <= time.Now().UnixMilli() {
		return o, p, orders.Conflict()
	}
	return o, fresh, nil
}
func (s *Service) Fulfill(ctx context.Context, id string) error {
	o, e := orders.Get(ctx, s.Store.DB, id, nil, false)
	if e != nil {
		return e
	}
	kind := Kind(o.Plan)
	if kind == "" {
		return nil
	}
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	o, e = orders.Get(ctx, tx, id, nil, true)
	if e != nil {
		return e
	}
	p, e := find(ctx, tx, kind, id)
	if e == pgx.ErrNoRows {
		return nil
	}
	if e != nil {
		return e
	}
	if !eligible(o) || p.State == "rejected" || p.Next > time.Now().UnixMilli() {
		return nil
	}
	if p.Attempted == nil && !s.Enabled(o.Plan) {
		return nil
	}
	if e = s.account(kind, p); e != nil {
		return e
	}
	now, lease := time.Now().UnixMilli(), security.ID()
	tag, e := tx.Exec(ctx, `UPDATE `+table(kind)+` SET lease_token=$1,lease_until=$2,updated_at=$3 WHERE order_id=$4 AND lease_until<=$3`, lease, now+120000, now, id)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return nil
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	p.Lease = &lease
	p.Until = now + 120000
	var runError error
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if runError != nil {
			extra := ""
			if kind == "delivery_supplier" {
				extra = ",next_check_at=$3+60000"
			}
			_, _ = s.Store.DB.Exec(c, `UPDATE `+table(kind)+` SET state=CASE WHEN state='creating' THEN 'unknown' ELSE state END,updated_at=$3`+extra+` WHERE order_id=$1 AND lease_token=$2`, id, lease, time.Now().UnixMilli())
		}
		_, _ = s.Store.DB.Exec(c, `UPDATE `+table(kind)+` SET lease_until=0 WHERE order_id=$1 AND lease_token=$2`, id, lease)
	}()
	if p.State != "purchased" {
		if kind == "gmail" {
			runError = s.gmail(ctx, p)
		} else {
			runError = s.delivery_supplier(ctx, p)
		}
		if runError != nil {
			return runError
		}
	}
	p, e = find(ctx, s.Store.DB, kind, id)
	if e != nil {
		return e
	}
	if p.State == "purchased" {
		runError = s.prepareDelivery(ctx, kind, p)
		return runError
	}
	return nil
}
func (s *Service) start(ctx context.Context, kind string, p Purchase) (Purchase, error) {
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return p, e
	}
	defer tx.Rollback(ctx)
	o, p, e := s.fence(ctx, tx, kind, p)
	if e != nil {
		return p, e
	}
	if p.Attempted != nil {
		return p, orders.Conflict()
	}
	now := time.Now().UnixMilli()
	extra := ""
	if kind == "delivery_supplier" && p.Key == "ymx/"+p.ID && p.Supplier == nil {
		extra = ",request_key='ymx_'||order_id"
		p.Key = "ymx_" + p.ID
	}
	_, e = tx.Exec(ctx, `UPDATE `+table(kind)+` SET state='creating',attempted_at=$1,updated_at=$1`+extra+` WHERE order_id=$2`, now, p.ID)
	if e != nil {
		return p, e
	}
	next := o
	next.Status = "processing"
	if next.Started == nil {
		next.Started = &now
	}
	if _, e = orders.Change(ctx, tx, o, next, "system", kind+"-purchase-start", "付款已确认，开始自动采购"); e != nil {
		return p, e
	}
	p.Attempted = &now
	p.State = "creating"
	return p, tx.Commit(ctx)
}
func (s *Service) reject(ctx context.Context, kind string, p Purchase, action, note string) error {
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	o, p, e := s.fence(ctx, tx, kind, p)
	if e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE `+table(kind)+` SET state='rejected',updated_at=$1 WHERE order_id=$2`, time.Now().UnixMilli(), p.ID); e != nil {
		return e
	}
	next := o
	next.Status = "failed"
	if _, e = orders.Change(ctx, tx, o, next, "system", action, note); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Service) remember(ctx context.Context, kind string, p Purchase, id string) error {
	if !identifier.MatchString(id) {
		return errors.New("invalid supplier order identity")
	}
	tag, e := s.Store.DB.Exec(ctx, `UPDATE `+table(kind)+` SET supplier_order=$1 WHERE order_id=$2 AND lease_token=$3 AND lease_until>$4 AND (supplier_order IS NULL OR supplier_order=$1)`, id, p.ID, p.Lease, time.Now().UnixMilli())
	if e == nil && tag.RowsAffected() != 1 {
		return orders.Conflict()
	}
	return e
}
func (s *Service) prepareDelivery(ctx context.Context, kind string, p Purchase) error {
	if s.Delivery.Sender == nil {
		return nil
	}
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	o, p, e := s.fence(ctx, tx, kind, p)
	if e != nil {
		return e
	}
	if p.State != "purchased" {
		return orders.Conflict()
	}
	var exists bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM email_deliveries WHERE order_id=$1)`, p.ID).Scan(&exists); e != nil {
		return e
	}
	if !exists {
		value := ""
		if kind == "gmail" {
			if p.Cards == nil {
				return errors.New("missing verified cards")
			}
			var cards []Card
			if e = json.Unmarshal([]byte(*p.Cards), &cards); e != nil || len(cards) != 1 || cards[0].Password == nil {
				return errors.New("invalid verified cards")
			}
			value = cards[0].Number
			if *cards[0].Password != "" {
				value += "\n密码：" + *cards[0].Password
			}
		} else {
			if p.Value == nil {
				return errors.New("missing verified delivery")
			}
			value = *p.Value
		}
		if e = s.Delivery.SaveDraft(ctx, tx, o, value, p.Instructions); e != nil {
			return e
		}
		next := o
		next.Status = "processing"
		if _, e = orders.Change(ctx, tx, o, next, "system", kind+"-delivered", "采购已核验，准备邮件发货"); e != nil {
			return e
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	return s.Delivery.SendSaved(ctx, p.ID)
}
func (s *Service) Reconcile(ctx context.Context) error {
	var first error
	for _, kind := range []string{"gmail", "delivery_supplier"} {
		if (kind == "gmail" && s.AccountSupplier == nil) || (kind == "delivery_supplier" && s.DeliverySupplier == nil) {
			continue
		}
		extra := ""
		if kind == "delivery_supplier" {
			extra = " AND p.next_check_at<=$1"
		}
		rows, e := orders.Rows(ctx, s.Store.DB, `SELECT p.order_id FROM `+table(kind)+` p JOIN orders o ON o.id=p.order_id WHERE p.state<>'rejected' AND p.lease_until<=$1`+extra+` AND o.mode='live' AND o.is_test=0 AND o.payment_status='paid' AND o.refund_hold=0 AND o.refunded_cents=0 AND o.redemption_blocked=0 AND o.status IN('session','processing') ORDER BY p.updated_at LIMIT 10`, time.Now().UnixMilli())
		if e != nil {
			return e
		}
		ids := make([]string, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, r["order_id"].(string))
		}
		err := worker.Each(ctx, ids, func(ctx context.Context, id string) error {
			e := s.Fulfill(ctx, id)
			if e != nil {
				_, _ = s.Store.DB.Exec(ctx, `UPDATE `+table(kind)+` SET updated_at=$1 WHERE order_id=$2 AND lease_until<=$1`, time.Now().UnixMilli(), id)
			}
			return e
		})
		if first == nil {
			first = err
		}
	}
	return first
}
