package management

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"gptibo/storefront/internal/auth"
	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/security"
)

var publicPath = regexp.MustCompile(`^/(?:orders|product/[a-z0-9-]+)?$`)
var shopPath = regexp.MustCompile(`^/(?:r|s)/([^/]+)(?:/(?:orders|product/[a-z0-9-]+))?$`)

type VisitorInput struct {
	VisitorID string `json:"visitorId"`
	Path      string `json:"path"`
}

func chinaDay(t time.Time) string { return t.In(time.FixedZone("CST", 8*3600)).Format("2006-01-02") }
func (s Service) RecordVisitor(ctx context.Context, in VisitorInput) error {
	if !security.ValidID(in.VisitorID) || len(in.Path) > 120 {
		return fault.New(400, "访客标识或页面路径无效。")
	}
	path := strings.TrimSuffix(in.Path, "/")
	if path == "" {
		path = "/"
	}
	owner := "platform"
	if match := shopPath.FindStringSubmatch(path); match != nil {
		if e := s.Store.DB.QueryRow(ctx, `SELECT s.id FROM partner_stores s JOIN partners p ON p.id=s.partner_id WHERE s.slug=$1 AND s.active AND p.active`, match[1]).Scan(&owner); e != nil {
			if e == pgx.ErrNoRows {
				return fault.New(404, "代理商店不可用。")
			}
			return e
		}
	} else if !publicPath.MatchString(path) {
		return fault.New(400, "页面路径无效。")
	}
	_, e := s.Store.DB.Exec(ctx, `INSERT INTO visitor_days(day,shop_id,visitor_hash,page_views) VALUES($1,$2,$3,1) ON CONFLICT(day,shop_id,visitor_hash) DO UPDATE SET page_views=visitor_days.page_views+1`, chinaDay(time.Now()), owner, security.Hash(in.VisitorID))
	return e
}
func (s Service) Visitors(ctx context.Context, days int, shop string, admin bool) (map[string]any, error) {
	if days != 7 && days != 30 && days != 90 {
		return nil, fault.New(400, "仅支持最近 7、30 或 90 天。")
	}
	now := time.Now()
	since := chinaDay(now.AddDate(0, 0, -days+1))
	tx, e := s.Store.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	list, e := rows(ctx, tx, `SELECT day,sum(page_views)::bigint "pageViews",count(DISTINCT visitor_hash) visitors FROM visitor_days WHERE day>=$1 AND ($2='' OR shop_id=$2 OR shop_id IN (SELECT id FROM partner_stores WHERE partner_id=$2)) GROUP BY day ORDER BY day`, since, shop)
	if e != nil {
		return nil, e
	}
	total, e := rows(ctx, tx, `SELECT COALESCE(sum(page_views),0)::bigint "pageViews",count(DISTINCT visitor_hash) visitors FROM visitor_days WHERE day>=$1 AND ($2='' OR shop_id=$2 OR shop_id IN (SELECT id FROM partner_stores WHERE partner_id=$2))`, since, shop)
	if e != nil {
		return nil, e
	}
	byDay := map[string]map[string]any{}
	for _, r := range list {
		byDay[r["day"].(string)] = r
	}
	trend := []map[string]any{}
	for i := 0; i < days; i++ {
		day := chinaDay(now.AddDate(0, 0, -days+1+i))
		r := byDay[day]
		if r == nil {
			r = map[string]any{"day": day, "pageViews": int64(0), "visitors": int64(0)}
		}
		trend = append(trend, r)
	}
	out := total[0]
	out["days"] = days
	out["trend"] = trend
	if admin {
		platform, e := rows(ctx, tx, `SELECT COALESCE(sum(page_views),0)::bigint "pageViews",count(DISTINCT visitor_hash) visitors FROM visitor_days WHERE day>=$1 AND shop_id='platform'`, since)
		if e != nil {
			return nil, e
		}
		out["platform"] = platform[0]
		// Aggregate traffic once before joining partner details. A correlated IN in
		// the outer join scans every visitor row again for every partner.
		// Deduplicate at partner level so visits across its stores count only once.
		partners, e := rows(ctx, tx, `WITH traffic AS (
 SELECT s.partner_id,sum(v.page_views)::bigint page_views,count(DISTINCT v.visitor_hash) visitors
 FROM partner_stores s JOIN visitor_days v ON v.shop_id=s.id
 WHERE v.day>=$1 GROUP BY s.partner_id
) SELECT p.id,p.name,p.slug,p.active::int,COALESCE(t.page_views,0)::bigint "pageViews",COALESCE(t.visitors,0)::bigint visitors
 FROM partners p LEFT JOIN traffic t ON t.partner_id=p.id
 ORDER BY visitors DESC,"pageViews" DESC,p.created_at DESC`, since)
		if e != nil {
			return nil, e
		}
		out["partners"] = partners
	}
	return out, tx.Commit(ctx)
}
func (s Service) Ranking(ctx context.Context, days, page int) (map[string]any, error) {
	if (days != 1 && days != 7 && days != 30) || page < 1 || page > 10000 {
		return nil, fault.New(400, "时间范围或页码无效。")
	}
	now := time.Now().UnixMilli()
	since := (now+28800000)/86400000*86400000 - 28800000 - int64(days-1)*86400000
	tx, e := s.Store.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	base := `WITH sales AS (SELECT partner_id,count(*) order_count FROM orders WHERE partner_id IS NOT NULL AND mode='live' AND is_test=0 AND payment_status='paid' AND status='success' AND refunded_cents<price_cents AND created_at BETWEEN $1 AND $2 GROUP BY partner_id) `
	list, e := rows(ctx, tx, base+`SELECT p.id,p.name,p.email,p.active::int,COALESCE(c.wechat,'') wechat,COALESCE(s.order_count,0) "orderCount" FROM partners p LEFT JOIN sales s ON s.partner_id=p.id LEFT JOIN support_contacts c ON c.owner=p.id ORDER BY "orderCount" DESC,p.created_at,p.id LIMIT 20 OFFSET $3`, since, now, (page-1)*20)
	if e != nil {
		return nil, e
	}
	for i, r := range list {
		r["rank"] = (page-1)*20 + i + 1
	}
	totals, e := rows(ctx, tx, base+`SELECT count(*) total,COALESCE(sum(s.order_count),0)::bigint "orderCount",count(s.partner_id) "sellingPartners" FROM partners p LEFT JOIN sales s ON s.partner_id=p.id`, since, now)
	if e != nil {
		return nil, e
	}
	out := totals[0]
	out["days"] = days
	out["page"] = page
	out["since"] = since
	out["until"] = now
	out["rows"] = list
	return out, tx.Commit(ctx)
}

type InterestInput struct {
	Token   string `json:"token"`
	Email   string `json:"email"`
	Product string `json:"product"`
	Consent bool   `json:"consent"`
}

func (s Service) Interest(ctx context.Context, in InterestInput, cancel bool) (map[string]any, error) {
	if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(in.Token) {
		return nil, fault.New(400, "登记凭据无效。")
	}
	hash := security.Hash(in.Token)
	if cancel {
		_, e := s.Store.DB.Exec(ctx, `DELETE FROM launch_interest WHERE token_hash=$1`, hash)
		return map[string]any{"ok": true}, e
	}
	email, e := auth.Email(in.Email)
	if e != nil {
		return nil, e
	}
	if !in.Consent || (in.Product != "claude" && in.Product != "grok" && in.Product != "gmail") {
		return nil, fault.New(400, "请核对商品并同意邮箱使用说明。")
	}
	if _, e = s.Store.DB.Exec(ctx, `INSERT INTO launch_interest(id,product,email,token_hash,consent_version) VALUES($1,$2,$3,$4,'launch-interest-v1') ON CONFLICT DO NOTHING`, security.ID(), in.Product, email, hash); e != nil {
		return nil, e
	}
	var manageable bool
	e = s.Store.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM launch_interest WHERE product=$1 AND email=$2 AND token_hash=$3)`, in.Product, email, hash).Scan(&manageable)
	return map[string]any{"ok": true, "manageable": manageable, "deliveryEnabled": false}, e
}
func (s Service) InterestList(ctx context.Context, page int) (map[string]any, error) {
	if page < 1 || page > 10000 {
		return nil, fault.New(400, "页码无效。")
	}
	list, e := rows(ctx, s.Store.DB, `SELECT id,product,email,consent_version,created_at FROM launch_interest ORDER BY created_at DESC,id DESC LIMIT 20 OFFSET $1`, (page-1)*20)
	if e != nil {
		return nil, e
	}
	var total int64
	e = s.Store.DB.QueryRow(ctx, `SELECT count(*) FROM launch_interest`).Scan(&total)
	return map[string]any{"rows": list, "total": total, "page": page, "deliveryEnabled": false}, e
}
