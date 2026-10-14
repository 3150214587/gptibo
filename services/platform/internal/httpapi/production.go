package httpapi

import (
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"gptibo/platform/internal/commerce"
	"gptibo/platform/internal/security"
	"strconv"
	"time"
)

func (s *Server) orderByMerchant(c *gin.Context) {
	o, err := commerce.ScanOrder(s.Store.DB.QueryRow(c, "SELECT "+commerce.OrderColumns+" FROM orders WHERE merchant_order_no=$1 AND user_id=$2", c.Param("number"), p(c).ID))
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			bad(c, err)
			return
		}
		bad(c, commerce.Fail("order_missing", "该渠道订单号尚无受理记录，请勿换号重复提交", 404))
		return
	}
	ok(c, o)
}

func (s *Server) catalogReadiness(ctx context.Context, rows []map[string]any) error {
	provider, err := s.Commerce.Provider(ctx)
	if err != nil {
		var known *commerce.Error
		if !errors.As(err, &known) || known.Code != "provider_missing" {
			return err
		}
	}
	var worker bool
	if e := s.Store.DB.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM worker_heartbeats WHERE updated_at>now()-interval '60 seconds')").Scan(&worker); e != nil {
		return e
	}
	for _, row := range rows {
		row["available"] = false
		row["unavailable_reason"] = "商品未开放"
		if enabled, ok := row["enabled"].(bool); !ok || !enabled {
			continue
		}
		snap := commerce.Snapshot{Provider: provider}
		var exists bool
		e := s.Store.DB.QueryRow(ctx, "SELECT p.business,p.upstream_plan,p.code,p.config,COALESCE(r.enabled,false),COALESCE(r.card_id,0),COALESCE(r.auto_open,false) FROM products p LEFT JOIN LATERAL(SELECT * FROM card_rules WHERE business=p.business AND product_code IN ('',p.code) ORDER BY (product_code=p.code) DESC LIMIT 1) r ON true WHERE p.code=$1", row["code"]).Scan(&snap.Business, &snap.Plan, &snap.ProductCode, &snap.ProductConfig, &exists, &snap.Rule.CardID, &snap.Rule.AutoOpen)
		if e != nil {
			return e
		}
		snap.Rule.Enabled = exists
		if e := commerce.ExecutionReady(snap); e != nil {
			row["unavailable_reason"] = e.(*commerce.Error).Message
			continue
		}
		if !worker {
			row["unavailable_reason"] = "充值服务暂不可用"
			continue
		}
		if snap.Business == "x" {
			var account bool
			if e := s.Store.DB.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM x_accounts WHERE enabled)").Scan(&account); e != nil {
				return e
			}
			if !account {
				row["unavailable_reason"] = "暂无可用 X 账号"
				continue
			}
		}
		row["available"] = true
		row["unavailable_reason"] = ""
	}
	return nil
}

func (s *Server) allowRate(ctx context.Context, bucket string, max int, window time.Duration) error {
	var attempts int
	err := s.Store.DB.QueryRow(ctx, "INSERT INTO login_limits(id,attempts,reset_at) VALUES($1,1,now()+$2::interval) ON CONFLICT(id) DO UPDATE SET attempts=CASE WHEN login_limits.reset_at<now() THEN 1 ELSE login_limits.attempts+1 END,reset_at=CASE WHEN login_limits.reset_at<now() THEN now()+$2::interval ELSE login_limits.reset_at END RETURNING attempts", security.Hash(bucket), strconv.Itoa(int(window.Seconds()))+" seconds").Scan(&attempts)
	if err != nil {
		return err
	}
	if attempts > max {
		return commerce.Fail("rate_limited", "请求过于频繁，请稍后重试", 429)
	}
	return nil
}
func (s *Server) cardOperations(c *gin.Context) {
	limit, offset := page(c)
	s.query(c, "SELECT c.reference,c.order_id,o.business,c.operation,c.amount_minor,c.fee_minor,c.card_id,c.status,c.resolution_note,c.created_at FROM card_operations c JOIN orders o ON o.id=c.order_id WHERE ($3='' OR c.reference ILIKE '%'||$3||'%' OR o.merchant_order_no ILIKE '%'||$3||'%') ORDER BY c.created_at DESC,c.reference DESC LIMIT $1 OFFSET $2", limit, offset, c.Query("search"))
}
func (s *Server) resolveCardOperation(c *gin.Context) {
	var body commerce.CardResolution
	if !bind(c, &body) {
		return
	}
	if s.ResolveCard == nil {
		bad(c, commerce.Fail("unavailable", "原卡操作核对服务不可用", 503))
		return
	}
	if err := s.ResolveCard(c, p(c).ID, body); err != nil {
		bad(c, err)
		return
	}
	ok(c, gin.H{"saved": true})
}

func (s *Server) paymentHandoff(c *gin.Context) {
	if s.ManualCheckout == nil {
		bad(c, commerce.Fail("unavailable", "人工付款入口暂不可用", 503))
		return
	}
	link, err := s.ManualCheckout(c, p(c).ID, c.Param("id"))
	if err != nil {
		bad(c, err)
		return
	}
	ok(c, gin.H{"url": link})
}

func (s *Server) paymentRegenerate(c *gin.Context) {
	var body struct {
		ConfirmedUnpaid bool `json:"confirmed_unpaid"`
	}
	if !bind(c, &body) {
		return
	}
	if s.RegenerateCheckout == nil {
		bad(c, commerce.Fail("unavailable", "重新生成账单服务暂不可用", 503))
		return
	}
	link, err := s.RegenerateCheckout(c, p(c).ID, c.Param("id"), body.ConfirmedUnpaid)
	if err != nil {
		bad(c, err)
		return
	}
	ok(c, gin.H{"url": link})
}
func page(c *gin.Context) (int, int) {
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if err != nil || limit < 1 || limit > 100 {
		limit = 50
	}
	offset, err := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if err != nil || offset < 0 || offset > 1000000 {
		offset = 0
	}
	return limit, offset
}
