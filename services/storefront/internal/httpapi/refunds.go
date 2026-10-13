package httpapi

import (
	"github.com/gin-gonic/gin"
	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/orders"
	"gptibo/storefront/internal/refunds"
	"gptibo/storefront/internal/security"
)

func registerRefunds(group, admin *gin.RouterGroup, s refunds.Service, token func(*gin.Context, string) string) {
	buyer := func(c *gin.Context) (*orders.Access, bool) {
		value := token(c, "buyer")
		if !security.ValidToken(value) {
			fail(c, fault.New(401, "请先建立浏览器订单会话。"))
			return nil, false
		}
		a := &orders.Access{OwnerHash: buyerHash(c, value)}
		p, e := s.Auth.Require(c.Request.Context(), s.Store.DB, "customer", token(c, "customer"))
		if e == nil {
			a.Email = p.Subject
		} else if fault.Status(e) != 401 {
			fail(c, e)
			return nil, false
		}
		return a, true
	}
	reply := func(c *gin.Context, v any, e error) {
		if e != nil {
			fail(c, e)
			return
		}
		c.JSON(200, v)
	}
	group.GET("/orders/:id/refunds", func(c *gin.Context) {
		a, ok := buyer(c)
		if !ok {
			return
		}
		v, e := s.CustomerRead(c.Request.Context(), c.Param("id"), a)
		reply(c, v, e)
	})
	group.POST("/orders/:id/refunds", func(c *gin.Context) {
		a, ok := buyer(c)
		if !ok {
			return
		}
		var in refunds.CustomerInput
		if !read(c, &in) {
			return
		}
		if e := s.Auth.Limit(c.Request.Context(), "refund:"+a.OwnerHash, 90, 60); e != nil {
			fail(c, e)
			return
		}
		created, e := s.CustomerWrite(c.Request.Context(), c.Param("id"), a, in)
		if e != nil {
			fail(c, e)
			return
		}
		status := 200
		if created {
			status = 201
		}
		c.JSON(status, gin.H{"ok": true})
	})
	admin.GET("/refunds", func(c *gin.Context) {
		v, e := s.AdminList(c.Request.Context(), page(c), c.Query("status"))
		reply(c, v, e)
	})
	admin.GET("/refunds/notifications", func(c *gin.Context) { v, e := s.AdminNotifications(c.Request.Context()); reply(c, v, e) })
	admin.GET("/refunds/:id", func(c *gin.Context) { v, e := s.AdminDetail(c.Request.Context(), c.Param("id")); reply(c, v, e) })
	admin.POST("/refunds/:id", func(c *gin.Context) {
		var in refunds.AdminInput
		if !read(c, &in) {
			return
		}
		e := s.AdminWrite(c.Request.Context(), token(c, "admin"), c.Param("id"), in)
		reply(c, gin.H{"ok": true}, e)
	})
}
