package management

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"gptibo/storefront/internal/auth"
	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/security"
)

type StoreInput struct {
	ID       string        `json:"id"`
	Name     string        `json:"name"`
	Active   bool          `json:"active"`
	Version  int64         `json:"version"`
	Settings StoreSettings `json:"settings"`
}
type StoreSettings struct {
	BrandName             string `json:"brandName"`
	Logo                  string `json:"logo"`
	PageTitle             string `json:"pageTitle"`
	Headline              string `json:"headline"`
	Description           string `json:"description"`
	Wechat                string `json:"wechat"`
	QQ                    string `json:"qq"`
	Email                 string `json:"email"`
	Announcement          string `json:"announcement"`
	AnnouncementTitle     string `json:"announcementTitle"`
	AnnouncementPlacement string `json:"announcementPlacement"`
}

func (s Service) Stores(ctx context.Context, partner string) (map[string]any, error) {
	tx, e := s.Store.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	shops, e := rows(ctx, tx, `SELECT s.id,s.partner_id,s.slot,s.slug,s.name,s.active,s.version,s.created_at,CASE WHEN s.slot=1 THEN jsonb_build_object('brandName',COALESCE(b.name,''),'logo',COALESCE(b.logo,''),'pageTitle',COALESCE(b.page_title,''),'headline',COALESCE(b.headline,''),'description',COALESCE(b.description,''),'wechat',COALESCE(sc.wechat,''),'qq',COALESCE(sc.qq,''),'email',COALESCE(sc.email,''),'announcement',CASE WHEN a.enabled THEN a.content ELSE '' END,'announcementTitle',COALESCE(a.title,''),'announcementPlacement',COALESCE(a.placement,'both'))||s.settings ELSE s.settings END settings, (SELECT count(*) FROM orders o WHERE o.partner_id=s.partner_id AND (o.store_id=s.id OR (o.store_id IS NULL AND s.slot=1))) order_count,(SELECT COALESCE(sum(partner_order_profit(o.id,s.partner_id)),0)::bigint FROM orders o WHERE o.partner_id=s.partner_id AND (o.store_id=s.id OR (o.store_id IS NULL AND s.slot=1)) AND o.mode='live' AND o.is_test=0 AND o.payment_status='paid' AND o.status='success' AND o.refund_hold=0 AND NOT EXISTS(SELECT 1 FROM recharge_orders t WHERE t.order_id=o.id AND (t.financial_hold=1 OR t.merchant_margin_cents IS NULL))) earned_cents,(SELECT count(DISTINCT visitor_hash) FROM visitor_days WHERE shop_id=s.id AND day>=to_char(now() AT TIME ZONE 'Asia/Shanghai'-interval '29 days','YYYY-MM-DD')) visitors FROM partner_stores s LEFT JOIN partner_branding b ON b.partner_id=s.partner_id LEFT JOIN support_contacts sc ON sc.owner=s.partner_id LEFT JOIN partner_announcements a ON a.partner_id=s.partner_id WHERE s.partner_id=$1 ORDER BY slot`, partner)
	if e != nil {
		return nil, e
	}
	prices, e := rows(ctx, tx, `SELECT s.id store_id,c.id plan,c.name,c.product_id,p.cost_cents,p.child_cost_cents,p.version supply_version,COALESCE(sp.price_cents,p.price_cents) price_cents,COALESCE(sp.enabled,true) enabled,COALESCE(sp.version,0) version FROM partner_stores s JOIN partner_prices p ON p.partner_id=s.partner_id JOIN catalog_plans c ON c.id=p.plan LEFT JOIN store_prices sp ON sp.store_id=s.id AND sp.plan=p.plan WHERE s.partner_id=$1 ORDER BY s.slot,c.sort_order,c.id`, partner)
	if e != nil {
		return nil, e
	}
	coupons, e := rows(ctx, tx, `SELECT c.*,COALESCE((SELECT jsonb_agg(jsonb_build_object('storeId',t.store_id,'plan',t.plan)) FROM coupon_targets t WHERE t.coupon_id=c.id),'[]') targets,(SELECT count(*) FROM orders o WHERE o.coupon_id=c.id AND coupon_reserved(o)) uses FROM partner_coupons c WHERE partner_id=$1 ORDER BY code`, partner)
	if e != nil {
		return nil, e
	}
	charges, e := rows(ctx, tx, `SELECT id,domain_id,hostname,amount_cents,source,state,created_at,paid_at FROM domain_charges WHERE partner_id=$1 ORDER BY created_at DESC LIMIT 100`, partner)
	if e != nil {
		return nil, e
	}
	origin, e := s.referralOrigin(ctx, tx, partner)
	if e != nil {
		return nil, e
	}
	return map[string]any{"referralOrigin": origin, "stores": shops, "prices": prices, "coupons": coupons, "charges": charges}, tx.Commit(ctx)
}
func (s Service) SaveStore(ctx context.Context, token string, in StoreInput) error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || !validText(in.Name, 32) || in.Version < 0 {
		return fault.New(400, "商店名称或版本无效。")
	}
	if in.Settings.Email != "" {
		email, e := auth.Email(in.Settings.Email)
		if e != nil {
			return e
		}
		in.Settings.Email = email
	}
	if in.Settings.AnnouncementPlacement == "" {
		in.Settings.AnnouncementPlacement = "both"
	}
	if !validText(in.Settings.AnnouncementTitle, 100) || (in.Settings.AnnouncementPlacement != "home" && in.Settings.AnnouncementPlacement != "product" && in.Settings.AnnouncementPlacement != "both") {
		return fault.New(400, "公告标题或展示位置无效。")
	}
	raw, e := json.Marshal(in.Settings)
	if e != nil {
		return e
	}
	if len(raw) > 16000 || !validText(in.Settings.Description, 500) || !validText(in.Settings.Announcement, 2000) || !validText(in.Settings.BrandName, 32) || !validText(in.Settings.Headline, 120) || !validText(in.Settings.PageTitle, 120) || !validText(in.Settings.Wechat, 100) || !validText(in.Settings.QQ, 100) || !validText(in.Settings.Email, 320) {
		return fault.New(400, "商店设置过长。")
	}
	if in.Settings.Logo != "" && !logoPath.MatchString(in.Settings.Logo) {
		return fault.New(400, "请使用已上传的商店 Logo。")
	}
	return s.writeHierarchy(ctx, "partner", token, func(tx pgx.Tx, p auth.Principal) error {
		if in.Settings.Logo != "" && !strings.HasPrefix(in.Settings.Logo, "/store-logos/"+security.Hash(p.Subject)+"/") {
			return fault.New(400, "请上传属于当前代理的 Logo。")
		}

		if in.ID == "" {
			var slot int
			if e := tx.QueryRow(ctx, `SELECT COALESCE(min(n),0) FROM generate_series(1,5) n WHERE NOT EXISTS(SELECT 1 FROM partner_stores WHERE partner_id=$1 AND slot=n)`, p.Subject).Scan(&slot); e != nil {
				return e
			}
			if slot == 0 {
				return fault.New(409, "每个代理最多拥有五家商店。")
			}
			in.ID = security.ID()
			created := false
			for range 8 {
				slug := strings.ToUpper(security.Token()[:6])
				tag, e := tx.Exec(ctx, `INSERT INTO partner_stores(id,partner_id,slot,slug,name,active,settings) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(slug) DO NOTHING`, in.ID, p.Subject, slot, slug, in.Name, in.Active, raw)
				if e != nil {
					return e
				}
				if tag.RowsAffected() == 1 {
					created = true
					break
				}
			}
			if !created {
				return fault.New(503, "推广码生成暂时失败，请重试。")
			}
			// Copy current default retail prices once. Later changes are independent.
			if _, e := tx.Exec(ctx, `INSERT INTO store_prices(store_id,plan,price_cents) SELECT $1,plan,price_cents FROM partner_prices WHERE partner_id=$2`, in.ID, p.Subject); e != nil {
				return e
			}
		} else {
			tag, e := tx.Exec(ctx, `UPDATE partner_stores SET name=$1,active=$2,settings=$3,version=version+1 WHERE id=$4 AND partner_id=$5 AND version=$6`, in.Name, in.Active, raw, in.ID, p.Subject, in.Version)
			if e != nil {
				return e
			}
			if tag.RowsAffected() != 1 {
				return conflict()
			}
		}
		return audit(ctx, tx, p, "store-save", in.ID, in)
	})
}

type StorePriceInput struct {
	StoreID    string `json:"storeId"`
	Plan       string `json:"plan"`
	PriceCents int64  `json:"priceCents"`
	Enabled    bool   `json:"enabled"`
	Version    int64  `json:"version"`
}

func (s Service) SaveStorePrice(ctx context.Context, token string, in StorePriceInput) error {
	if !validMoney(in.PriceCents) || in.Version < 0 {
		return fault.New(400, "价格或版本无效。")
	}
	return s.writeHierarchy(ctx, "partner", token, func(tx pgx.Tx, p auth.Principal) error {
		var cost, version int64
		var slot int
		e := tx.QueryRow(ctx, `SELECT pp.cost_cents,COALESCE(sp.version,0),s.slot FROM partner_stores s JOIN partner_prices pp ON pp.partner_id=s.partner_id AND pp.plan=$3 LEFT JOIN store_prices sp ON sp.store_id=s.id AND sp.plan=pp.plan WHERE s.id=$1 AND s.partner_id=$2`, in.StoreID, p.Subject, in.Plan).Scan(&cost, &version, &slot)
		if e == pgx.ErrNoRows {
			return fault.New(404, "商店或商品不存在。")
		}
		if e != nil {
			return e
		}
		if version != in.Version {
			return conflict()
		}
		if e = checkStoreFloor(ctx, tx, p.Subject, in.StoreID, in.Plan, in.PriceCents, cost); e != nil {
			return e
		}
		if slot == 1 { // Keep legacy workspace pricing in sync with the default store.
			if _, e = tx.Exec(ctx, `UPDATE partner_prices SET price_cents=$1,version=version+1 WHERE partner_id=$2 AND plan=$3`, in.PriceCents, p.Subject, in.Plan); e != nil {
				return e
			}
		}
		_, e = tx.Exec(ctx, `INSERT INTO store_prices(store_id,plan,price_cents,enabled,version) VALUES($1,$2,$3,$4,1) ON CONFLICT(store_id,plan) DO UPDATE SET price_cents=$3,enabled=$4,version=store_prices.version+1`, in.StoreID, in.Plan, in.PriceCents, in.Enabled)
		if e != nil {
			return e
		}
		return audit(ctx, tx, p, "store-price", in.StoreID, in)
	})
}

// Called while holding the hierarchy lock, shared by checkout and all price/coupon writers.
func checkStoreFloor(ctx context.Context, tx pgx.Tx, partner, shop, plan string, price, cost int64) error {
	var discount int64
	e := tx.QueryRow(ctx, `SELECT COALESCE(max(c.discount_cents),0) FROM coupon_targets t JOIN partner_coupons c ON c.id=t.coupon_id WHERE t.store_id=$1 AND t.plan=$2 AND c.active AND c.ends_at>now()`, shop, plan).Scan(&discount)
	if e != nil {
		return e
	}
	if price-discount < cost {
		return fault.New(409, "该店售价或优惠后价格低于拿货价，请先调整售价或优惠码。")
	}
	return nil
}
func checkAccountFloor(ctx context.Context, tx pgx.Tx, partner, plan string, cost int64) error {
	list, e := rows(ctx, tx, `SELECT s.id,COALESCE(sp.price_cents,p.price_cents) price FROM partner_stores s JOIN partner_prices p ON p.partner_id=s.partner_id AND p.plan=$2 LEFT JOIN store_prices sp ON sp.store_id=s.id AND sp.plan=p.plan WHERE s.partner_id=$1`, partner, plan)
	if e != nil {
		return e
	}
	for _, r := range list {
		if e = checkStoreFloor(ctx, tx, partner, r["id"].(string), plan, r["price"].(int64), cost); e != nil {
			return e
		}
	}
	return nil
}

type CouponTarget struct {
	StoreID string `json:"storeId"`
	Plan    string `json:"plan"`
}
type CouponInput struct {
	ID            string         `json:"id"`
	Code          string         `json:"code"`
	DiscountCents int64          `json:"discountCents"`
	StartsAt      time.Time      `json:"startsAt"`
	EndsAt        time.Time      `json:"endsAt"`
	MaxUses       int            `json:"maxUses"`
	OncePerEmail  bool           `json:"oncePerEmail"`
	Active        bool           `json:"active"`
	Version       int64          `json:"version"`
	Targets       []CouponTarget `json:"targets"`
}

var couponPattern = regexp.MustCompile(`^[A-Z0-9_-]{3,32}$`)

func (s Service) SaveCoupon(ctx context.Context, token string, in CouponInput) error {
	in.Code = strings.ToUpper(strings.TrimSpace(in.Code))
	if !couponPattern.MatchString(in.Code) || !validMoney(in.DiscountCents) || !in.EndsAt.After(in.StartsAt) || in.MaxUses < 1 || in.MaxUses > 1000000 || len(in.Targets) == 0 || len(in.Targets) > 500 || in.Version < 0 {
		return fault.New(400, "请填写有效优惠码、金额、时间、次数并选择商店商品。")
	}
	return s.writeHierarchy(ctx, "partner", token, func(tx pgx.Tx, p auth.Principal) error {
		for _, t := range in.Targets {
			var price, cost int64
			e := tx.QueryRow(ctx, `SELECT COALESCE(sp.price_cents,pp.price_cents),pp.cost_cents FROM partner_stores s JOIN partner_prices pp ON pp.partner_id=s.partner_id AND pp.plan=$3 LEFT JOIN store_prices sp ON sp.store_id=s.id AND sp.plan=pp.plan WHERE s.id=$1 AND s.partner_id=$2`, t.StoreID, p.Subject, t.Plan).Scan(&price, &cost)
			if e == pgx.ErrNoRows {
				return fault.New(400, "只能选择自己的商店和商品。")
			}
			if e != nil {
				return e
			}
			if in.Active && price-in.DiscountCents < cost {
				return fault.New(400, "所选商店商品优惠后低于拿货价。")
			}
		}
		if in.ID == "" {
			in.ID = security.ID()
			if _, e := tx.Exec(ctx, `INSERT INTO partner_coupons(id,partner_id,code,discount_cents,starts_at,ends_at,max_uses,once_per_email,active) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, in.ID, p.Subject, in.Code, in.DiscountCents, in.StartsAt, in.EndsAt, in.MaxUses, in.OncePerEmail, in.Active); e != nil {
				return e
			}
		} else {
			tag, e := tx.Exec(ctx, `UPDATE partner_coupons SET code=$1,discount_cents=$2,starts_at=$3,ends_at=$4,max_uses=$5,once_per_email=$6,active=$7,version=version+1 WHERE id=$8 AND partner_id=$9 AND version=$10`, in.Code, in.DiscountCents, in.StartsAt, in.EndsAt, in.MaxUses, in.OncePerEmail, in.Active, in.ID, p.Subject, in.Version)
			if e != nil {
				return e
			}
			if tag.RowsAffected() != 1 {
				return conflict()
			}
			if _, e = tx.Exec(ctx, `DELETE FROM coupon_targets WHERE coupon_id=$1`, in.ID); e != nil {
				return e
			}
		}
		for _, t := range in.Targets {
			if _, e := tx.Exec(ctx, `INSERT INTO coupon_targets(coupon_id,partner_id,store_id,plan) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, in.ID, p.Subject, t.StoreID, t.Plan); e != nil {
				return e
			}
		}
		return audit(ctx, tx, p, "coupon-save", in.ID, in)
	})
}

type SupplyQuoteInput struct {
	Plan      string `json:"plan"`
	CostCents int64  `json:"costCents"`
	Version   *int64 `json:"version"`
}

func (s Service) SaveSupplyQuote(ctx context.Context, token string, in SupplyQuoteInput) error {
	if !validMoney(in.CostCents) || in.Version == nil || *in.Version < 0 {
		return fault.New(400, "供货报价或版本无效。")
	}
	return s.hierarchyWrite(ctx, token, func(tx pgx.Tx, p auth.Principal) error {
		tag, e := tx.Exec(ctx, `UPDATE partner_prices SET child_cost_cents=$1,version=version+1 WHERE partner_id=$2 AND plan=$3 AND cost_cents<=$1 AND version=$4`, in.CostCents, p.Subject, in.Plan, *in.Version)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return fault.New(409, "报价不能低于自己的拿货价，且版本必须为最新，请刷新后重试。")
		}
		return audit(ctx, tx, p, "child-supply-quote", p.Subject, in)
	})
}

type DomainBillingInput struct {
	Version int64  `json:"version"`
	Months  int    `json:"months"`
	Note    string `json:"note"`
}

func (s Service) AdjustDomainBilling(ctx context.Context, token, partner, id string, in DomainBillingInput) error {
	if in.Months < 1 || in.Months > 24 || in.Version < 0 || strings.TrimSpace(in.Note) == "" || !validText(in.Note, 300) {
		return fault.New(400, "请填写延长月份（1–24）、当前版本及操作原因。")
	}
	return s.write(ctx, "admin", token, func(tx pgx.Tx, p auth.Principal) error {
		if e := financialLock(ctx, tx); e != nil {
			return e
		}
		var pending bool
		if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM domain_charges WHERE domain_id=$1 AND state='pending')`, id).Scan(&pending); e != nil {
			return e
		}
		if pending {
			return fault.New(409, "存在待确认付款，请先核对原支付单。")
		}
		tag, e := tx.Exec(ctx, `UPDATE partner_domains SET expires_at=greatest(COALESCE(expires_at,now()),now())+make_interval(months=>$1),billing_version=billing_version+1,checked_at=NULL WHERE id=$2 AND partner_id=$3 AND billing_version=$4 AND NOT free AND state<>'deleting'`, in.Months, id, partner, in.Version)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return conflict()
		}
		return audit(ctx, tx, p, "domain-fee-waiver", id, in)
	})
}
