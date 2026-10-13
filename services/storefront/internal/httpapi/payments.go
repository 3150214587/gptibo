package httpapi

import (
	"context"
	"github.com/gin-gonic/gin"
	"gptibo/storefront/internal/auth"
	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/orders"
	"gptibo/storefront/internal/payments"
	"gptibo/storefront/internal/security"
	"time"
)

func registerPayments(r *gin.Engine, group, admin *gin.RouterGroup, s payments.Service, a *auth.Service, token func(*gin.Context, string) string) {
	notify := func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
		defer cancel()
		req := c.Request.WithContext(ctx)
		if s.Notify(req, c.Param("provider")) {
			c.String(200, "success")
		} else {
			c.String(400, "failure")
		}
	}
	r.POST("/api/payments/notify/:provider", notify)
	r.GET("/api/payments/notify/:provider", notify)
	group.POST("/orders/:id/payment", func(c *gin.Context) {
		buyer := token(c, "buyer")
		if !security.ValidToken(buyer) {
			fail(c, fault.New(401, "请先建立浏览器订单会话。"))
			return
		}
		access := orders.Access{OwnerHash: buyerHash(c, buyer)}
		p, e := a.Require(c.Request.Context(), s.Store.DB, "customer", token(c, "customer"))
		if e == nil {
			access.Email = p.Subject
		} else if fault.Status(e) != 401 {
			fail(c, e)
			return
		}
		var in struct {
			ExpectedFeeBPS *int64 `json:"expectedFeeBps"`
			Action         string `json:"action"`
			Method         string `json:"method"`
		}
		if !read(c, &in) {
			return
		}
		if e = a.Limit(c.Request.Context(), "payment:"+access.OwnerHash, 90, 60); e != nil {
			fail(c, e)
			return
		}
		out, e := s.Action(payments.WithFeeQuote(c.Request.Context(), in.ExpectedFeeBPS), c.Param("id"), &access, in.Action, false, in.Method)
		if e != nil {
			fail(c, e)
			return
		}
		c.JSON(200, out)
	})
	admin.GET("/payments/settings", func(c *gin.Context) {
		out, e := s.Settings(c.Request.Context())
		if e != nil {
			fail(c, e)
			return
		}
		c.JSON(200, out)
	})
	admin.POST("/payments/settings", func(c *gin.Context) {
		var in struct {
			FeeRatesBPS map[string]int64 `json:"feeRatesBps"`
			Provider    string           `json:"provider"`
			Providers   []string         `json:"providers"`
			Version     *int64           `json:"version"`
		}
		if !read(c, &in) {
			return
		}
		if in.Version == nil {
			fail(c, fault.New(400, "请刷新支付设置后重试。"))
			return
		}
		if in.Provider != "" && in.Providers != nil {
			fail(c, fault.New(400, "请只提交一种渠道配置格式。"))
			return
		}
		ids := in.Providers
		if ids == nil {
			ids = []string{in.Provider}
		}
		if e := s.SetChannels(c.Request.Context(), a, token(c, "admin"), ids, *in.Version, in.FeeRatesBPS); e != nil {
			fail(c, e)
			return
		}
		out, e := s.Settings(c.Request.Context())
		if e != nil {
			fail(c, e)
			return
		}
		c.JSON(200, out)
	})
	admin.POST("/payments/reconcile", func(c *gin.Context) {
		var in struct{}
		if !read(c, &in) {
			return
		}
		out, e := s.Reconcile(c.Request.Context())
		if e != nil {
			fail(c, e)
			return
		}
		c.JSON(200, out)
	})
}
