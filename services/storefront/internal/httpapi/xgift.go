package httpapi

import (
	"github.com/gin-gonic/gin"
	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/orders"
	"gptibo/storefront/internal/security"
	"gptibo/storefront/internal/xgift"
	"regexp"
)

func registerXGift(group, admin *gin.RouterGroup, s *xgift.Service, token func(*gin.Context, string) string) {
	group.POST("/orders/xgift/eligibility", func(c *gin.Context) {
		buyer := token(c, "buyer")
		if !security.ValidToken(buyer) {
			fail(c, fault.New(401, "请先建立浏览器订单会话。"))
			return
		}
		var in struct {
			Plan     string `json:"plan"`
			Username string `json:"username"`
		}
		if !read(c, &in) {
			return
		}
		if e := s.Auth.Limit(c.Request.Context(), "xgift-check:"+c.ClientIP(), 90, 60); e != nil {
			fail(c, e)
			return
		}
		v, e := s.Preflight(c.Request.Context(), buyerHash(c, buyer), in.Plan, in.Username)
		if e != nil {
			fail(c, e)
			return
		}
		c.JSON(200, v)
	})
	admin.GET("/xgift/status", func(c *gin.Context) {
		client := s.Clients[s.Backend]
		if client == nil {
			c.JSON(200, gin.H{"configured": false})
			return
		}
		balance, e := client.Balance(c.Request.Context())
		if e != nil {
			fail(c, e)
			return
		}
		products, e := client.Products(c.Request.Context())
		if e != nil {
			fail(c, e)
			return
		}
		console := "https://x-api.shop.example.invalid"
		if s.Backend == "platform" {
			console = "https://api.shop.example.invalid"
		}
		c.JSON(200, gin.H{"configured": true, "balance": balance, "products": products, "consoleUrl": console})
	})
	admin.GET("/xgift/order", func(c *gin.Context) {
		merchant := c.Query("merchant_order_no")
		if !regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`).MatchString(merchant) {
			fail(c, fault.New(400, "商户订单号无效。"))
			return
		}
		backend := s.Backend
		rows, e := orders.Rows(c.Request.Context(), s.Store.DB, `SELECT supplier_backend,COALESCE(supplier_merchant_order_no,order_id) merchant FROM xgift_purchases WHERE order_id=$1`, merchant)
		if e != nil {
			fail(c, e)
			return
		}
		if len(rows) > 0 {
			backend = rows[0]["supplier_backend"].(string)
			merchant = rows[0]["merchant"].(string)
		}
		client := s.Clients[backend]
		if client == nil {
			fail(c, fault.New(503, "原赠送供应商未配置。"))
			return
		}
		v, e := client.Query(c.Request.Context(), merchant)
		if e != nil {
			fail(c, e)
			return
		}
		c.JSON(200, v)
	})
}
