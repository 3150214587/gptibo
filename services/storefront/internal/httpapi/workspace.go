package httpapi

import (
	"github.com/gin-gonic/gin"
	"gptibo/storefront/internal/auth"
	"gptibo/storefront/internal/management"
)

func registerWorkspace(admin, partner *gin.RouterGroup, m management.Service, token func(*gin.Context, string) string) {
	registerTickets(admin, partner, m, token)
	own := func(c *gin.Context) string { return c.MustGet("principal").(auth.Principal).Subject }
	respond := func(c *gin.Context, out any, e error) {
		if e != nil {
			fail(c, e)
			return
		}
		c.JSON(200, out)
	}
	partner.GET("/stores", func(c *gin.Context) { out, e := m.Stores(c.Request.Context(), own(c)); respond(c, out, e) })
	admin.GET("/partners/:id/stores", func(c *gin.Context) { out, e := m.Stores(c.Request.Context(), c.Param("id")); respond(c, out, e) })
	partner.POST("/stores", func(c *gin.Context) {
		var in management.StoreInput
		if !read(c, &in) {
			return
		}
		e := m.SaveStore(c.Request.Context(), token(c, "partner"), in)
		respond(c, gin.H{"ok": true}, e)
	})
	partner.POST("/stores/prices", func(c *gin.Context) {
		var in management.StorePriceInput
		if !read(c, &in) {
			return
		}
		e := m.SaveStorePrice(c.Request.Context(), token(c, "partner"), in)
		respond(c, gin.H{"ok": true}, e)
	})
	partner.POST("/supply-quotes", func(c *gin.Context) {
		var in management.SupplyQuoteInput
		if !read(c, &in) {
			return
		}
		e := m.SaveSupplyQuote(c.Request.Context(), token(c, "partner"), in)
		respond(c, gin.H{"ok": true}, e)
	})
	partner.POST("/coupons", func(c *gin.Context) {
		var in management.CouponInput
		if !read(c, &in) {
			return
		}
		e := m.SaveCoupon(c.Request.Context(), token(c, "partner"), in)
		respond(c, gin.H{"ok": true}, e)
	})
	partner.GET("/dashboard", func(c *gin.Context) { out, e := m.Dashboard(c.Request.Context(), own(c), false); respond(c, out, e) })
	partner.GET("/branding", func(c *gin.Context) { out, e := m.Branding(c.Request.Context(), own(c)); respond(c, out, e) })
	partner.POST("/branding/logo", func(c *gin.Context) {
		var in struct {
			Logo string `json:"logo"`
		}
		if !readLimit(c, &in, 360000) {
			return
		}
		out, e := m.PrepareLogo(c.Request.Context(), own(c), in.Logo)
		respond(c, out, e)
	})
	partner.POST("/branding", func(c *gin.Context) {
		var in management.Branding
		if !read(c, &in) {
			return
		}
		if e := m.SaveBranding(c.Request.Context(), token(c, "partner"), in); e != nil {
			fail(c, e)
			return
		}
		out, e := m.Branding(c.Request.Context(), own(c))
		respond(c, out, e)
	})
	admin.GET("/partners/:id/branding", func(c *gin.Context) { out, e := m.Branding(c.Request.Context(), c.Param("id")); respond(c, out, e) })
	partner.POST("/payout-account", func(c *gin.Context) {
		var in management.PayoutAccountInput
		if !read(c, &in) {
			return
		}
		if e := m.SavePayoutAccount(c.Request.Context(), token(c, "partner"), in); e != nil {
			fail(c, e)
			return
		}
		c.JSON(200, gin.H{"ok": true})
	})
	partner.GET("/notification-settings", func(c *gin.Context) {
		out, e := m.NotificationSettings(c.Request.Context(), own(c))
		respond(c, out, e)
	})
	partner.POST("/notification-settings", func(c *gin.Context) {
		var in management.NotificationSettingsInput
		if !read(c, &in) {
			return
		}
		if e := m.SaveNotificationSettings(c.Request.Context(), token(c, "partner"), in); e != nil {
			fail(c, e)
			return
		}
		out, e := m.NotificationSettings(c.Request.Context(), own(c))
		respond(c, out, e)
	})
	admin.GET("/partners/:id/notification-settings", func(c *gin.Context) {
		out, e := m.NotificationSettings(c.Request.Context(), c.Param("id"))
		respond(c, out, e)
	})
	admin.GET("/partners/:id/preview", func(c *gin.Context) {
		out, e := m.Dashboard(c.Request.Context(), c.Param("id"), true)
		respond(c, out, e)
	})
	partner.GET("/orders", func(c *gin.Context) {
		out, e := m.PartnerOrders(c.Request.Context(), own(c), page(c), c.Query("q"), c.Query("status"), c.Query("store"))
		respond(c, out, e)
	})
	admin.GET("/partners/:id/orders", func(c *gin.Context) {
		if _, e := m.Partner(c.Request.Context(), c.Param("id")); e != nil {
			fail(c, e)
			return
		}
		out, e := m.PartnerOrders(c.Request.Context(), c.Param("id"), page(c), c.Query("q"), c.Query("status"), c.Query("store"))
		respond(c, out, e)
	})
	partner.GET("/orders/:orderID", func(c *gin.Context) {
		out, e := m.PartnerOrderDetail(c.Request.Context(), own(c), c.Param("orderID"))
		respond(c, out, e)
	})
	admin.GET("/partners/:id/orders/:orderID", func(c *gin.Context) {
		out, e := m.PartnerOrderDetail(c.Request.Context(), c.Param("id"), c.Param("orderID"))
		respond(c, out, e)
	})
	partner.GET("/order-summary", func(c *gin.Context) {
		out, e := m.OrderSummary(c.Request.Context(), own(c), c.DefaultQuery("period", "all"))
		respond(c, out, e)
	})
	admin.GET("/partners/:id/order-summary", func(c *gin.Context) {
		if _, e := m.Partner(c.Request.Context(), c.Param("id")); e != nil {
			fail(c, e)
			return
		}
		out, e := m.OrderSummary(c.Request.Context(), c.Param("id"), c.DefaultQuery("period", "all"))
		respond(c, out, e)
	})
	partner.GET("/product-order", func(c *gin.Context) { out, e := m.ProductOrder(c.Request.Context(), own(c)); respond(c, out, e) })
	partner.POST("/product-order", func(c *gin.Context) {
		var in management.ProductOrderInput
		if !read(c, &in) {
			return
		}
		if e := m.SaveProductOrder(c.Request.Context(), token(c, "partner"), in); e != nil {
			fail(c, e)
			return
		}
		out, e := m.ProductOrder(c.Request.Context(), own(c))
		respond(c, out, e)
	})
	admin.GET("/partners/:id/product-order", func(c *gin.Context) {
		if _, e := m.Partner(c.Request.Context(), c.Param("id")); e != nil {
			fail(c, e)
			return
		}
		out, e := m.ProductOrder(c.Request.Context(), c.Param("id"))
		respond(c, out, e)
	})
	admin.GET("/partners/:id/support-contacts", func(c *gin.Context) {
		if _, e := m.Partner(c.Request.Context(), c.Param("id")); e != nil {
			fail(c, e)
			return
		}
		out, e := m.Contacts(c.Request.Context(), c.Param("id"))
		respond(c, out, e)
	})
	admin.GET("/partners/:id/announcement", func(c *gin.Context) {
		if _, e := m.Partner(c.Request.Context(), c.Param("id")); e != nil {
			fail(c, e)
			return
		}
		out, e := m.Announcement(c.Request.Context(), c.Param("id"))
		respond(c, out, e)
	})
	admin.GET("/partners/default-prices", func(c *gin.Context) { out, e := m.DefaultPrices(c.Request.Context()); respond(c, out, e) })
	admin.POST("/partners/default-prices", func(c *gin.Context) {
		var in management.DefaultPricesInput
		if !read(c, &in) {
			return
		}
		if e := m.SaveDefaultPrices(c.Request.Context(), token(c, "admin"), in); e != nil {
			fail(c, e)
			return
		}
		out, e := m.DefaultPrices(c.Request.Context())
		respond(c, out, e)
	})
}
