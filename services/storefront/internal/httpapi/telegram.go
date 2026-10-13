package httpapi

import (
	"context"
	"github.com/gin-gonic/gin"
	"gptibo/storefront/internal/telegram"
	"time"
)

func registerTelegram(r *gin.Engine, s telegram.Service) {
	handler := func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 50*time.Second)
		defer cancel()
		v, e := s.Webhook(ctx, c.Request)
		if e != nil {
			fail(c, e)
			return
		}
		c.JSON(200, v)
	}
	r.GET("/api/telegram/webhook", handler)
	r.POST("/api/telegram/webhook", handler)
}
