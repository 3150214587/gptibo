package orders

import (
	"context"
	"github.com/jackc/pgx/v5"
	"gptibo/storefront/internal/auth"
	"gptibo/storefront/internal/fault"
	"strings"
)

type CouponQuoteInput struct {
	Partner string `json:"partner"`
	Plan    string `json:"plan"`
	Email   string `json:"email"`
	Code    string `json:"code"`
}
type CouponQuote struct {
	Code      string `json:"code"`
	ListPrice int64  `json:"listPriceCents"`
	Discount  int64  `json:"discountCents"`
	Price     int64  `json:"priceCents"`
}

// The coupon row lock serializes reservations across all targeted shops and emails.
// Orders remain reserved until the payment channel has confirmed closure. A timeout alone never releases a use.
func couponPrice(ctx context.Context, tx pgx.Tx, partner, shop, plan, email, code string, price, cost int64) (string, int64, error) {
	var id string
	var discount int64
	var limit int
	var once bool
	code = strings.ToUpper(strings.TrimSpace(code))
	e := tx.QueryRow(ctx, `SELECT c.id,c.discount_cents,c.max_uses,c.once_per_email FROM partner_coupons c WHERE c.partner_id=$1 AND c.code=$2 AND c.active AND c.starts_at<=now() AND c.ends_at>now() AND EXISTS(SELECT 1 FROM coupon_targets t WHERE t.coupon_id=c.id AND t.store_id=$3 AND t.plan=$4) FOR UPDATE`, partner, code, shop, plan).Scan(&id, &discount, &limit, &once)
	if e == pgx.ErrNoRows {
		return "", 0, fault.New(400, "优惠码无效、已过期或不适用于本店商品。")
	}
	if e != nil {
		return "", 0, e
	}
	if price-discount < cost || price-discount < 1 {
		return "", 0, fault.New(409, "当前价格无法使用此优惠码，请联系商店。")
	}
	var used, byEmail int
	e = tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE lower(customer_email)=$2) FROM orders o WHERE coupon_id=$1 AND coupon_reserved(o)`, id, email).Scan(&used, &byEmail)
	if e != nil {
		return "", 0, e
	}
	if used >= limit {
		return "", 0, fault.New(409, "优惠码次数已用完或正在被待付款订单占用。")
	}
	if once && byEmail > 0 {
		return "", 0, fault.New(409, "该邮箱已使用此优惠码或有待付款订单。")
	}
	return id, discount, nil
}
func (s Service) QuoteCoupon(ctx context.Context, in CouponQuoteInput) (CouponQuote, error) {
	var out CouponQuote
	email, e := auth.Email(in.Email)
	if e != nil {
		return out, e
	}
	if len(in.Code) > 32 || in.Code == "" {
		return out, fault.New(400, "请填写优惠码。")
	}
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(903495)`); e != nil {
		return out, e
	}
	var shop, partner string
	var price, cost int64
	e = tx.QueryRow(ctx, `SELECT s.id,s.partner_id,COALESCE(sp.price_cents,p.price_cents),p.cost_cents FROM partner_stores s JOIN partners owner ON owner.id=s.partner_id JOIN partner_prices p ON p.partner_id=s.partner_id AND p.plan=$2 JOIN catalog_plans c ON c.id=p.plan LEFT JOIN store_prices sp ON sp.store_id=s.id AND sp.plan=p.plan WHERE s.slug=$1 AND s.active AND owner.active AND p.enabled AND c.enabled AND c.partner_enabled AND COALESCE(sp.enabled,true) AND (s.slot<>1 OR NOT EXISTS(SELECT 1 FROM partner_order_versions v WHERE v.partner_id=s.partner_id AND v.hidden_product_ids ? c.product_id)) AND (owner.parent_id IS NULL OR EXISTS(SELECT 1 FROM partners up JOIN partner_prices supply ON supply.partner_id=up.id AND supply.plan=p.plan WHERE up.id=owner.parent_id AND up.active AND supply.enabled AND supply.cost_cents<=p.cost_cents))`, in.Partner, in.Plan).Scan(&shop, &partner, &price, &cost)
	if e == pgx.ErrNoRows {
		return out, fault.New(404, "商店或商品不可用。")
	}
	if e != nil {
		return out, e
	}
	_, discount, e := couponPrice(ctx, tx, partner, shop, in.Plan, email, in.Code, price, cost)
	if e != nil {
		return out, e
	}
	out = CouponQuote{Code: strings.ToUpper(strings.TrimSpace(in.Code)), ListPrice: price, Discount: discount, Price: price - discount}
	return out, tx.Commit(ctx)
}
