package httpapi

import (
	"github.com/gin-gonic/gin"
	"gptibo/storefront/internal/auth"
	"gptibo/storefront/internal/domainbilling"
	"gptibo/storefront/internal/management"
	"gptibo/storefront/internal/payments"
)

func registerDomainBilling(r *gin.Engine, admin, partner *gin.RouterGroup, s domainbilling.Service, m management.Service, token func(*gin.Context, string) string) {
	r.GET("/api/domain-billing/notify/:provider", func(c *gin.Context) {
		if s.Notify(c.Request, c.Param("provider")) {
			c.String(200, "success")
		} else {
			c.String(400, "failure")
		}
	})
	partner.POST("/domains/:id/payment", func(c *gin.Context) {
		var in struct {
			Action  string `json:"action"`
			Version int64  `json:"version"`
		}
		if !read(c, &in) {
			return
		}
		p := c.MustGet("principal").(auth.Principal)
		if e := m.Auth.Limit(c.Request.Context(), "domain-payment:"+p.Subject, 20, 60); e != nil {
			fail(c, e)
			return
		}
		out, e := s.Payment(payments.WithClientIP(c.Request.Context(), c.ClientIP()), p.Subject, c.Param("id"), in.Action, in.Version)
		if e != nil {
			fail(c, e)
			return
		}
		c.JSON(200, out)
	})
	admin.POST("/partners/:id/domains/:domainID", func(c *gin.Context) {
		var in management.DomainBillingInput
		if !read(c, &in) {
			return
		}
		if e := m.AdjustDomainBilling(c.Request.Context(), token(c, "admin"), c.Param("id"), c.Param("domainID"), in); e != nil {
			fail(c, e)
			return
		}
		c.JSON(200, gin.H{"ok": true})
	})
}
