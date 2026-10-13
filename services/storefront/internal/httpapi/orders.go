package httpapi

import (
	"github.com/gin-gonic/gin"

	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/orders"
	"gptibo/storefront/internal/security"
)

func registerOrders(group, admin *gin.RouterGroup, s orders.Service, token func(*gin.Context, string) string) {
	buyer := func(c *gin.Context) (*orders.Access, bool) {
		value := token(c, "buyer")
		if !security.ValidToken(value) {
			fail(c, fault.New(401, "请先建立浏览器订单会话。"))
			return nil, false
		}
		access := &orders.Access{OwnerHash: buyerHash(c, value), ClientIP: trafficIP(c.ClientIP())}
		p, e := s.Auth.Require(c.Request.Context(), s.Store.DB, "customer", token(c, "customer"))
		if e == nil {
			access.Email = p.Subject
		} else if fault.Status(e) != 401 {
			fail(c, e)
			return nil, false
		}
		return access, true
	}
	reply := func(c *gin.Context, out any, e error) {
		if e != nil {
			fail(c, e)
			return
		}
		c.JSON(200, out)
	}
	group.POST("/orders/coupon-quote", func(c *gin.Context) {
		var in orders.CouponQuoteInput
		if !read(c, &in) {
			return
		}
		if e := s.Auth.Limit(c.Request.Context(), "coupon-quote:"+trafficIP(c.ClientIP()), 30, 60); e != nil {
			fail(c, e)
			return
		}
		in.Partner = storePartner(c, in.Partner)
		out, e := s.QuoteCoupon(c.Request.Context(), in)
		reply(c, out, e)
	})
	group.GET("/orders", func(c *gin.Context) {
		a, ok := buyer(c)
		if !ok {
			return
		}
		v, e := s.CustomerList(c.Request.Context(), *a)
		reply(c, v, e)
	})
	group.POST("/orders", func(c *gin.Context) {
		a, ok := buyer(c)
		if !ok {
			return
		}
		var in orders.CreateInput
		if !read(c, &in) {
			return
		}
		maxAttempts := 90
		if s.ProtectCreation {
			maxAttempts = 12
		}
		if e := s.Auth.Limit(c.Request.Context(), "order-create:"+trafficIP(c.ClientIP()), maxAttempts, 60); e != nil {
			fail(c, e)
			return
		}
		in.Partner = storePartner(c, in.Partner)
		o, created, e := s.Create(c.Request.Context(), *a, in)
		if e != nil {
			fail(c, e)
			return
		}
		v, e := s.View(c.Request.Context(), s.Store.DB, o, false)
		if e != nil {
			fail(c, e)
			return
		}
		status := 200
		if created {
			status = 201
		}
		c.JSON(status, gin.H{"order": v})
	})
	group.GET("/orders/:id", func(c *gin.Context) {
		a, ok := buyer(c)
		if !ok {
			return
		}
		v, e := s.Detail(c.Request.Context(), c.Param("id"), a)
		reply(c, v, e)
	})
	group.POST("/orders/:id/demo-actions", func(c *gin.Context) {
		a, ok := buyer(c)
		if !ok {
			return
		}
		var in orders.ActionInput
		if !read(c, &in) {
			return
		}
		o, e := s.Action(c.Request.Context(), c.Param("id"), a, "", in)
		if e != nil {
			fail(c, e)
			return
		}
		v, e := s.View(c.Request.Context(), s.Store.DB, o, false)
		reply(c, gin.H{"order": v}, e)
	})
	admin.GET("/overview", func(c *gin.Context) { v, e := s.Overview(c.Request.Context()); reply(c, v, e) })
	admin.GET("/orders", func(c *gin.Context) {
		v, e := s.AdminList(c.Request.Context(), page(c), c.Query("q"), c.Query("status"), c.Query("plan"))
		reply(c, v, e)
	})
	admin.GET("/orders/:id", func(c *gin.Context) { v, e := s.Detail(c.Request.Context(), c.Param("id"), nil); reply(c, v, e) })
	admin.POST("/orders/:id/actions", func(c *gin.Context) {
		var in orders.ActionInput
		if !read(c, &in) {
			return
		}
		if in.Action == "xgift-recovery" && s.RecoverGift != nil {
			if in.Version == nil {
				fail(c, orders.Conflict())
				return
			}
			if e := s.RecoverGift(c.Request.Context(), c.Param("id"), token(c, "admin"), in.Note, *in.Version); e != nil {
				fail(c, e)
				return
			}
			o, e := orders.Get(c.Request.Context(), s.Store.DB, c.Param("id"), nil, false)
			if e != nil {
				fail(c, e)
				return
			}
			v, e := s.View(c.Request.Context(), s.Store.DB, o, true)
			reply(c, gin.H{"order": v}, e)
			return
		}
		o, e := s.Action(c.Request.Context(), c.Param("id"), nil, token(c, "admin"), in)
		if e != nil {
			fail(c, e)
			return
		}
		v, e := s.View(c.Request.Context(), s.Store.DB, o, true)
		reply(c, gin.H{"order": v}, e)
	})
}
