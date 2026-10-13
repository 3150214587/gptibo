package httpapi

import (
	"context"
	"github.com/gin-gonic/gin"
	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/orders"
	"gptibo/storefront/internal/security"
	"gptibo/storefront/internal/recharge"
	"time"
)

func registerRecharge(r *gin.Engine, group, admin *gin.RouterGroup, o orders.Service, s *recharge.Service, token func(*gin.Context, string) string) {
	ready := func(c *gin.Context) bool {
		if s == nil {
			fail(c, fault.New(503, "充值服务未配置。"))
			return false
		}
		return true
	}
	admin.POST("/recharge/reconcile", func(c *gin.Context) {
		if !ready(c) {
			return
		}
		var in struct{}
		if !read(c, &in) {
			return
		}
		v, e := s.ReconcileReport(c.Request.Context())
		if e != nil {
			fail(c, e)
			return
		}
		c.JSON(200, v)
	})
	for _, action := range []string{"credential", "retry"} {
		admin.POST("/orders/:id/recharge-"+action, func(c *gin.Context) {
			if !ready(c) {
				return
			}
			c.Header("Cache-Control", "no-store")
			var in struct {
				AttemptID string `json:"attemptId"`
			}
			if !read(c, &in) {
				return
			}
			if !security.ValidID(in.AttemptID) {
				fail(c, orders.Conflict())
				return
			}
			p, e := o.Auth.Require(c.Request.Context(), o.Store.DB, "admin", token(c, "admin"))
			if e != nil {
				fail(c, e)
				return
			}
			if e = o.Auth.Limit(c.Request.Context(), "admin-recharge:"+p.Subject, 30, 60); e != nil {
				fail(c, e)
				return
			}
			if action == "credential" {
				v, err := s.AdminCredential(c.Request.Context(), c.Param("id"), in.AttemptID, p.Subject)
				if err != nil {
					fail(c, err)
					return
				}
				c.JSON(200, gin.H{"credential": v})
				return
			}
			if e = s.AdminRetry(c.Request.Context(), c.Param("id"), in.AttemptID, p.Subject); e != nil {
				fail(c, e)
				return
			}
			c.JSON(200, gin.H{"ok": true})
		})
	}
	r.POST("/api/recharge/webhook", func(c *gin.Context) {
		if !ready(c) {
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
		defer cancel()
		if e := s.Webhook(ctx, c.Request); e != nil {
			c.JSON(400, gin.H{"error": "通知未通过验证。"})
			return
		}
		c.JSON(200, gin.H{"ok": true})
	})
	for _, endpoint := range []string{"/orders/:id/fulfillment", "/orders/:id/session-precheck"} {
		group.POST(endpoint, func(c *gin.Context) {
			if !ready(c) {
				return
			}
			value := token(c, "buyer")
			if !security.ValidToken(value) {
				fail(c, fault.New(401, "请先建立浏览器订单会话。"))
				return
			}
			access := orders.Access{OwnerHash: buyerHash(c, value)}
			p, e := o.Auth.Require(c.Request.Context(), o.Store.DB, "customer", token(c, "customer"))
			if e == nil {
				access.Email = p.Subject
			} else if fault.Status(e) != 401 {
				fail(c, e)
				return
			}
			id := c.Param("id")
			if _, e = orders.Get(c.Request.Context(), o.Store.DB, id, &access, false); e != nil {
				fail(c, e)
				return
			}
			var in recharge.Input
			if !readLimit(c, &in, 40000) {
				return
			}
			if e = o.Auth.Limit(c.Request.Context(), "recharge:"+access.OwnerHash, 90, 60); e != nil {
				fail(c, e)
				return
			}
			if endpoint == "/orders/:id/session-precheck" {
				c.Header("Cache-Control", "no-store")
				if !security.ValidID(in.Key) {
					fail(c, orders.Conflict())
					return
				}
				if e = o.Auth.Limit(c.Request.Context(), "session-precheck:"+access.OwnerHash, 12, 60); e != nil {
					fail(c, e)
					return
				}
				result, err := s.Precheck(c.Request.Context(), id, in.Key, in.Credential)
				if err != nil {
					fail(c, err)
					return
				}
				c.JSON(200, gin.H{"precheck": result})
				return
			}
			if e = s.Action(c.Request.Context(), id, in); e != nil {
				fail(c, e)
				return
			}
			order, e := orders.Get(c.Request.Context(), o.Store.DB, id, &access, false)
			if e != nil {
				fail(c, e)
				return
			}
			v, e := o.View(c.Request.Context(), o.Store.DB, order, false)
			if e != nil {
				fail(c, e)
				return
			}
			c.JSON(200, gin.H{"order": v})
		})
	}
	admin.POST("/orders/:id/platform-status", func(c *gin.Context) {
		if !ready(c) {
			return
		}
		var in struct{}
		if !read(c, &in) {
			return
		}
		if e := s.Verify(c.Request.Context(), c.Param("id")); e != nil {
			fail(c, e)
			return
		}
		tx, e := o.Store.DB.Begin(c.Request.Context())
		if e != nil {
			fail(c, e)
			return
		}
		defer tx.Rollback(c.Request.Context())
		p, e := o.Auth.Require(c.Request.Context(), tx, "admin", token(c, "admin"))
		if e != nil {
			fail(c, e)
			return
		}
		rows, e := orders.Rows(c.Request.Context(), tx, `SELECT remote_id "remoteId",payment_status "paymentStatus",refunded_cents "refundedCents",verified_at "checkedAt",financial_hold=1 "financialHold" FROM recharge_orders WHERE order_id=$1`, c.Param("id"))
		if e != nil || len(rows) != 1 {
			fail(c, orders.Conflict())
			return
		}
		if _, e = tx.Exec(c.Request.Context(), `INSERT INTO audit_events(id,actor_role,actor_id,action,target,detail) VALUES($1,'admin',$2,'platform-order-query',$3,$4)`, security.ID(), p.Subject, c.Param("id"), recharge.Raw(rows[0])); e != nil {
			fail(c, e)
			return
		}
		if e = tx.Commit(c.Request.Context()); e != nil {
			fail(c, e)
			return
		}
		c.JSON(200, gin.H{"platformStatus": rows[0]})
	})
}
