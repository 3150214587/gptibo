package httpapi

import (
	"context"
	"github.com/gin-gonic/gin"
	"gptibo/storefront/internal/auth"
	"gptibo/storefront/internal/security"
	"strings"
	"time"
)

func apiLimit(a *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		if !strings.HasPrefix(path, "/api/") {
			c.Next()
			return
		}
		source := security.Hash(c.ClientIP())
		bucket := "api:"
		if path == "/api/telegram/webhook" || path == "/api/recharge/webhook" || strings.HasPrefix(path, "/api/invoices/notify/") || strings.HasPrefix(path, "/api/payments/notify/") || strings.HasPrefix(path, "/api/domain-billing/notify/") {
			bucket = "callback:"
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
		e := a.Limit(ctx, bucket+source, 120, 60)
		if e == nil {
			route := c.FullPath()
			expensive := strings.HasPrefix(path, "/api/admin/account_supplier") || (c.Request.Method == "GET" && (route == "/api/catalog" || route == "/api/orders" || route == "/api/orders/:id"))
			if c.Request.Method == "POST" {
				for _, suffix := range []string{"/payment", "/fulfillment", "/session-precheck", "/platform-status", "/delivery", "/account_supplier", "/delivery_supplier", "/reconcile"} {
					if strings.HasSuffix(route, suffix) {
						expensive = true
					}
				}
			}
			if expensive {
				e = a.Limit(ctx, "expensive:"+source, 90, 60)
			}
		}
		cancel()
		if e != nil {
			fail(c, e)
			return
		}
		c.Next()
	}
}
