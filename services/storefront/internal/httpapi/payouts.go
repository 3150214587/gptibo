package httpapi

import (
	"github.com/gin-gonic/gin"
	"gptibo/storefront/internal/management"
)

func registerPayouts(admin, partner *gin.RouterGroup, s management.Service, token func(*gin.Context, string) string) {
	reply := func(c *gin.Context, v any, e error) {
		if e != nil {
			fail(c, e)
			return
		}
		c.JSON(200, v)
	}
	partner.POST("/payouts", func(c *gin.Context) {
		var in management.PayoutInput
		if !read(c, &in) {
			return
		}
		created, e := s.RequestPayout(c.Request.Context(), token(c, "partner"), in)
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
	admin.GET("/partner-payouts", func(c *gin.Context) { v, e := s.PayoutList(c.Request.Context(), page(c)); reply(c, v, e) })
	admin.GET("/partner-payouts/:id", func(c *gin.Context) {
		v, e := s.PayoutDetail(c.Request.Context(), c.Param("id"), page(c))
		reply(c, v, e)
	})
	admin.POST("/partner-payouts/:id", func(c *gin.Context) {
		var in management.HandlePayoutInput
		if !read(c, &in) {
			return
		}
		e := s.HandlePayout(c.Request.Context(), token(c, "admin"), c.Param("id"), in)
		reply(c, gin.H{"ok": true}, e)
	})
	reset := func(c *gin.Context) {
		var in management.ResetCooldownInput
		if !read(c, &in) {
			return
		}
		v, e := s.ResetCooldown(c.Request.Context(), token(c, "admin"), c.Param("id"), in)
		reply(c, v, e)
	}
	admin.POST("/partners/payout-cooldown/reset", reset)
	admin.POST("/partners/:id/payout-cooldown/reset", reset)
}
