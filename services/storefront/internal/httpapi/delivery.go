package httpapi

import (
	"github.com/gin-gonic/gin"
	"gptibo/storefront/internal/delivery"
)

func registerDelivery(admin *gin.RouterGroup, s delivery.Service, token func(*gin.Context, string) string) {
	reply := func(c *gin.Context, v any, e error) {
		if e != nil {
			fail(c, e)
			return
		}
		c.JSON(200, v)
	}
	admin.GET("/orders/:id/delivery", func(c *gin.Context) { v, e := s.View(c.Request.Context(), c.Param("id")); reply(c, v, e) })
	admin.POST("/orders/:id/delivery", func(c *gin.Context) {
		var in delivery.Input
		if !read(c, &in) {
			return
		}
		if e := s.Act(c.Request.Context(), c.Param("id"), token(c, "admin"), in, false); e != nil {
			fail(c, e)
			return
		}
		v, e := s.View(c.Request.Context(), c.Param("id"))
		reply(c, v, e)
	})
	admin.POST("/membership-delivery/preview", func(c *gin.Context) {
		var in struct {
			Plan string `json:"plan"`
			Key  string `json:"key"`
		}
		if !read(c, &in) {
			return
		}
		v, e := s.PreviewMembership(c.Request.Context(), in.Plan, in.Key)
		reply(c, v, e)
	})
	admin.POST("/recharge-reminders/preview", func(c *gin.Context) {
		var in struct {
			PartnerID string `json:"partnerId"`
		}
		if !read(c, &in) {
			return
		}
		v, e := s.PreviewReminder(c.Request.Context(), in.PartnerID)
		reply(c, v, e)
	})
}
