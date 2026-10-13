package httpapi

import (
	"github.com/gin-gonic/gin"
	"gptibo/storefront/internal/auth"
	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/orders"
	"gptibo/storefront/internal/payments"
	"gptibo/storefront/internal/refunds"
	"gptibo/storefront/internal/security"
)

func registerPaymentTests(admin *gin.RouterGroup, s payments.Service, a *auth.Service, r refunds.Service, enabled bool, token func(*gin.Context, string) string) {
	view := func(c *gin.Context, id string, status int) {
		v, e := s.TestView(c.Request.Context(), id)
		if e != nil {
			fail(c, e)
			return
		}
		c.JSON(status, gin.H{"order": v})
	}
	admin.GET("/payments/test", func(c *gin.Context) {
		rows, e := orders.List(c.Request.Context(), s.Store.DB, `SELECT to_jsonb(o) FROM orders o WHERE is_test=1 ORDER BY created_at DESC LIMIT 10`)
		if e != nil {
			fail(c, e)
			return
		}
		views := []map[string]any{}
		for _, o := range rows {
			v, e := s.TestView(c.Request.Context(), o.ID)
			if e != nil {
				fail(c, e)
				return
			}
			views = append(views, v)
		}
		c.JSON(200, gin.H{"enabled": enabled && s.Provider != nil, "orders": views})
	})
	admin.POST("/payments/test", func(c *gin.Context) {
		var in struct {
			Key   string `json:"key"`
			Email string `json:"email"`
		}
		if !read(c, &in) {
			return
		}
		id, created, e := s.TestCreate(c.Request.Context(), a, token(c, "admin"), in.Key, in.Email, enabled)
		if e != nil {
			fail(c, e)
			return
		}
		status := 200
		if created {
			status = 201
		}
		view(c, id, status)
	})
	admin.GET("/payments/test/:id", func(c *gin.Context) { view(c, c.Param("id"), 200) })
	admin.POST("/payments/test/:id", func(c *gin.Context) {
		var in struct {
			Action string `json:"action"`
			Method string `json:"method"`
		}
		if !read(c, &in) {
			return
		}
		id := c.Param("id")
		ctx := c.Request.Context()
		o, e := orders.Get(ctx, s.Store.DB, id, nil, false)
		if e != nil {
			fail(c, e)
			return
		}
		if !payments.TestOrder(o) {
			fail(c, orders.Conflict())
			return
		}
		if in.Action == "refund" {
			if o.Refunded == 1 {
				view(c, id, 200)
				return
			}
			if o.Payment != "paid" {
				fail(c, orders.Conflict())
				return
			}
			cases, e := orders.Rows(ctx, s.Store.DB, `SELECT id,status,version FROM refund_cases WHERE order_id=$1 AND status IN ('pending','approved') ORDER BY created_at DESC LIMIT 1`, id)
			if e != nil {
				fail(c, e)
				return
			}
			if len(cases) == 0 {
				_, e = r.CustomerWrite(ctx, id, nil, refunds.CustomerInput{Key: security.ID(), Reason: "支付联调测试原路退回 ¥0.01", Version: &o.Version})
				if e != nil {
					fail(c, e)
					return
				}
				cases, e = orders.Rows(ctx, s.Store.DB, `SELECT id,status,version FROM refund_cases WHERE order_id=$1 AND status='pending'`, id)
				if e != nil || len(cases) != 1 {
					fail(c, orders.Conflict())
					return
				}
			}
			item := cases[0]
			version := item["version"].(int64)
			caseID := item["id"].(string)
			if item["status"] == "pending" {
				e = r.AdminWrite(ctx, token(c, "admin"), caseID, refunds.AdminInput{Action: "approve", Version: &version, Amount: 1, Note: "管理员确认退回支付联调测试款"})
				if e != nil {
					fail(c, e)
					return
				}
				version++
			}
			e = r.AdminWrite(ctx, token(c, "admin"), caseID, refunds.AdminInput{Action: "execute-refund", Version: &version, Note: "支付联调测试原路退款"})
			if e != nil {
				fail(c, e)
				return
			}
		} else {
			if in.Action == "create" && !enabled {
				fail(c, fault.New(503, "支付联调尚未启用。"))
				return
			}
			if in.Action == "close" {
				closed, e := s.TestCloseUncreated(ctx, id)
				if e != nil {
					fail(c, e)
					return
				}
				if closed {
					view(c, id, 200)
					return
				}
			}
			if _, e = s.Action(ctx, id, nil, in.Action, enabled, in.Method); e != nil {
				fail(c, e)
				return
			}
		}
		view(c, id, 200)
	})
}
