package httpapi

import (
	"context"
	"github.com/gin-gonic/gin"
	"gptibo/storefront/internal/auth"
	"gptibo/storefront/internal/invoices"
	"time"
)

func registerInvoices(r *gin.Engine, admin, partner *gin.RouterGroup, s invoices.Service, token func(*gin.Context, string) string) {
	notify := func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
		defer cancel()
		if s.Notify(c.Request.WithContext(ctx), c.Param("provider")) {
			c.String(200, "success")
		} else {
			c.String(400, "failure")
		}
	}
	r.POST("/api/invoices/notify/:provider", notify)
	r.GET("/api/invoices/notify/:provider", notify)
	reply := func(c *gin.Context, v any, e error) {
		if e != nil {
			fail(c, e)
			return
		}
		c.JSON(200, v)
	}
	for role, g := range map[string]*gin.RouterGroup{"admin": admin, "partner": partner} {
		owner := func(c *gin.Context) string {
			if role == "partner" {
				return c.MustGet("principal").(auth.Principal).Subject
			}
			return ""
		}
		g.GET("/invoices", func(c *gin.Context) {
			v, e := s.List(c.Request.Context(), owner(c), page(c), c.Query("status"), c.Query("q"))
			reply(c, v, e)
		})
		g.GET("/invoices/:id", func(c *gin.Context) {
			v, e := s.Detail(c.Request.Context(), c.Param("id"), owner(c), role == "admin")
			reply(c, v, e)
		})
		g.POST("/invoices/:id/payment", func(c *gin.Context) {
			var in struct {
				Action string `json:"action"`
				Method string `json:"method"`
			}
			if !read(c, &in) {
				return
			}
			v, e := s.PaymentAction(c.Request.Context(), role, token(c, role), c.Param("id"), in.Action, in.Method)
			reply(c, v, e)
		})
	}
	partner.POST("/invoices", func(c *gin.Context) {
		var in invoices.Input
		if !read(c, &in) {
			return
		}
		v, e := s.Create(c.Request.Context(), token(c, "partner"), in)
		if e != nil {
			fail(c, e)
			return
		}
		c.JSON(201, v)
	})
	for _, submit := range []bool{true, false} {
		path := "/invoices/:id/complete"
		if submit {
			path = "/invoices/:id/submit"
		}
		admin.POST(path, func(c *gin.Context) {
			var in invoices.ActionInput
			if !read(c, &in) {
				return
			}
			e := s.Advance(c.Request.Context(), token(c, "admin"), c.Param("id"), submit, in)
			reply(c, gin.H{"ok": true}, e)
		})
	}
}
