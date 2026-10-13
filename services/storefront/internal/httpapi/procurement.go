package httpapi

import (
	"github.com/gin-gonic/gin"
	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/procurement"
	"strconv"
)

func registerProcurement(admin *gin.RouterGroup, s *procurement.Service, token func(*gin.Context, string) string) {
	if s == nil {
		return
	}
	reply := func(c *gin.Context, v any, e error) {
		if e != nil {
			fail(c, e)
			return
		}
		c.JSON(200, v)
	}
	admin.GET("/account_supplier", func(c *gin.Context) {
		var goods, ceiling any
		if s.GoodsID > 0 {
			goods = s.GoodsID
		}
		if s.MaxCents > 0 {
			ceiling = s.MaxCents
		}
		c.JSON(200, gin.H{"configured": s.AccountSupplier != nil, "checkoutEnabled": s.Enabled("gmail-account"), "goodsId": goods, "maxCostCents": ceiling})
	})
	admin.GET("/account_supplier/goods", func(c *gin.Context) {
		v, e := s.Goods(c.Request.Context(), page(c), c.DefaultQuery("q", "gmail"), 0)
		reply(c, v, e)
	})
	admin.GET("/account_supplier/goods/:id", func(c *gin.Context) {
		id, e := strconv.ParseInt(c.Param("id"), 10, 64)
		if e != nil || id <= 0 {
			fail(c, fault.New(400, "商品编号无效。"))
			return
		}
		v, e := s.Goods(c.Request.Context(), 1, "", id)
		reply(c, v, e)
	})
	for suffix, kind := range map[string]string{"account_supplier": "gmail", "delivery_supplier": "delivery_supplier"} {
		admin.GET("/orders/:id/"+suffix, func(c *gin.Context) { v, e := s.View(c.Request.Context(), kind, c.Param("id")); reply(c, v, e) })
		admin.POST("/orders/:id/"+suffix, func(c *gin.Context) {
			var in procurement.AdminInput
			if !read(c, &in) {
				return
			}
			if e := s.Act(c.Request.Context(), kind, c.Param("id"), token(c, "admin"), in); e != nil {
				fail(c, e)
				return
			}
			v, e := s.View(c.Request.Context(), kind, c.Param("id"))
			reply(c, v, e)
		})
	}
}
