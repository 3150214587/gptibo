package procurement

import (
	"context"
	"encoding/json"
	"errors"
	"gptibo/storefront/internal/delivery"
	"gptibo/storefront/internal/orders"
	"math"
	"net/url"
	"strings"
	"time"
)

// This supplier's Gemini claim host redirects HTTP to HTTPS. Preserve the
// original token and query bytes, while keeping all other delivery validation.
func delivery_supplierDeliveryValue(service, value string) string {
	value = strings.TrimSpace(value)
	if service != "DEMO_PRODUCT_A" || !strings.HasPrefix(value, "http://") {
		return value
	}
	u, err := url.Parse(value)
	if err == nil && u.User == nil && u.Host == "claim.example.invalid" {
		return "https://" + strings.TrimPrefix(value, "http://")
	}
	return value
}

func (s *Service) gmail(ctx context.Context, p Purchase) error {
	var e error
	if p.Attempted == nil {
		g, e := s.AccountSupplier.Goods(ctx, p.Goods)
		if e != nil {
			return e
		}
		if e = validateGoods(g, p.Ceiling); e != nil {
			return e
		}
		p, e = s.start(ctx, "gmail", p)
		if e != nil {
			return e
		}
		var created GmailOrder
		e = s.AccountSupplier.Call(ctx, "/order/create", map[string]any{"goodsId": p.Goods, "count": 1, "notifyUrl": "", "outerNumber": p.ID, "safePrice": float64(p.Ceiling) / 100, "sku": "", "attach": []any{}}, &created)
		if errors.Is(e, ErrRejected) {
			if err := s.reject(ctx, "gmail", p, "gmail-purchase-rejected", "供应商明确拒绝采购，请核对原订单售后"); err != nil {
				return err
			}
		}
		if e != nil {
			return e
		}
		if e = s.remember(ctx, "gmail", p, created.ID); e != nil {
			return e
		}
		p.Supplier = &created.ID
	}
	// AccountSupplier does not guarantee idempotent create: after any attempted write only query.
	id := ""
	if p.Supplier != nil {
		id = *p.Supplier
	}
	var result GmailOrder
	if e = s.AccountSupplier.Call(ctx, "/order/getDetail", map[string]any{"orderNumber": id, "outerNumber": p.ID}, &result); e != nil {
		return e
	}
	if !identifier.MatchString(result.ID) || result.Merchant != p.ID || (p.Supplier != nil && result.ID != *p.Supplier) {
		return errors.New("supplier order identity mismatch")
	}
	if result.Status == 4 || result.Status == 5 {
		return s.reject(ctx, "gmail", p, "gmail-purchase-rejected", "供应商已确认采购失败或退款，请通过原订单处理售后")
	}
	cost, e1 := cents(result.Money)
	refund, e2 := cents(result.Refund)
	if e1 != nil || e2 != nil || result.Count != 1 || result.Status != 3 || refund != 0 || cost > p.Ceiling || len(result.Cards) != 1 {
		return errors.New("supplier purchase amount or quantity mismatch")
	}
	card := result.Cards[0]
	if card.ID == "" || strings.TrimSpace(card.Number) == "" || card.Password == nil || card.Status == nil || *card.Status != 0 || len(card.Number)+len(*card.Password) > 8000 {
		return errors.New("supplier credentials not verified")
	}
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	_, p, e = s.fence(ctx, tx, "gmail", p)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE account_supplier_orders SET state='purchased',supplier_order=$1,actual_cost_cents=$2,cards=$3,updated_at=$4 WHERE order_id=$5`, result.ID, cost, raw(result.Cards), time.Now().UnixMilli(), p.ID)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Service) delivery_supplier(ctx context.Context, p Purchase) error {
	var e error
	if p.Attempted == nil {
		products, e := s.DeliverySupplier.Products(ctx)
		if e != nil {
			return e
		}
		if e = validateNY(products, p.Service, p.MaxUSD); e != nil {
			return e
		}
		var profile struct {
			Balance *json.Number `json:"wallet_balance"`
		}
		if e = s.DeliverySupplier.Call(ctx, "/me", "", nil, &profile); e != nil {
			return e
		}
		amount, e := usdMicros(profile.Balance)
		if e != nil || amount < p.MaxUSD*10000 {
			return errors.New("insufficient supplier balance")
		}
		p, e = s.start(ctx, "delivery_supplier", p)
		if e != nil {
			return e
		}
	}
	if p.Supplier == nil {
		var created NYOrder
		e = s.DeliverySupplier.Call(ctx, "/order", p.Key, &p.Body, &created)
		if errors.Is(e, ErrReceiptRejected) {
			if err := s.reject(ctx, "delivery_supplier", p, "delivery_supplier-receipt-rejected", "供应商明确拒绝采购编号，采购未发生，停止自动重试"); err != nil {
				return err
			}
		}
		if e != nil {
			return e
		}
		if e = s.remember(ctx, "delivery_supplier", p, created.ID); e != nil {
			return e
		}
		p.Supplier = &created.ID
	}
	var response struct {
		Order NYOrder `json:"order"`
	}
	if e = s.DeliverySupplier.Call(ctx, "/order/"+*p.Supplier, "", nil, &response); e != nil {
		return e
	}
	v := response.Order
	if v.ID != *p.Supplier || (v.Service != "" && v.Service != p.Service) || v.Requested == nil || *v.Requested != 1 {
		return errors.New("supplier order identity mismatch")
	}
	charged, e1 := usdMicros(v.Charged)
	refunded, e2 := usdMicros(v.Refunded)
	net, e3 := usdMicros(v.Net)
	if v.Status == "refunded" && e1 == nil && e2 == nil && e3 == nil && net == 0 && refunded == charged && v.Delivered != nil && *v.Delivered == 0 {
		return s.reject(ctx, "delivery_supplier", p, "delivery_supplier-refunded", "供应商已确认采购退款，请通过原订单处理客户售后")
	}
	if v.Status != "success" {
		delay := min(float64(3600), max(float64(5), v.Retry))
		_, e = s.Store.DB.Exec(ctx, `UPDATE delivery_supplier_orders SET state='pending',next_check_at=$1,updated_at=$2 WHERE order_id=$3 AND lease_token=$4 AND lease_until>$2`, time.Now().UnixMilli()+int64(delay*1000), time.Now().UnixMilli(), p.ID, p.Lease)
		return e
	}
	if e1 != nil || e2 != nil || e3 != nil || charged <= 0 || charged > p.MaxUSD*10000 || refunded != 0 || net != charged || v.Delivered == nil || *v.Delivered != 1 || v.Partial == nil || *v.Partial || len(v.Values) != 1 {
		return errors.New("supplier purchase amount or quantity mismatch")
	}
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	o, p, e := s.fence(ctx, tx, "delivery_supplier", p)
	if e != nil {
		return e
	}
	value := delivery_supplierDeliveryValue(p.Service, v.Values[0])
	if !delivery.ValidValue(orders.EmailKind(o.Plan), value) {
		return errors.New("invalid supplier delivery value")
	}
	_, e = tx.Exec(ctx, `UPDATE delivery_supplier_orders SET state='purchased',actual_usd_cents=$1,actual_cost_cents=$2,value=$3,updated_at=$4,actual_usd_micros=$6 WHERE order_id=$5`, (charged+5000)/10000, int64(math.Round(float64(charged)*p.FX/10000)), value, time.Now().UnixMilli(), p.ID, charged)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
