package httpapi

import (
	"github.com/gin-gonic/gin"
	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/management"
	"strconv"
)

func registerAccounting(admin *gin.RouterGroup, m management.Service, token func(*gin.Context, string) string) {
	reply := func(c *gin.Context, v any, e error) {
		if e != nil {
			fail(c, e)
			return
		}
		c.JSON(200, v)
	}
	admin.GET("/accounting", func(c *gin.Context) { v, e := m.Accounting(c.Request.Context(), page(c)); reply(c, v, e) })
	admin.POST("/accounting/costs", func(c *gin.Context) {
		var in management.AccountingCostsInput
		if !read(c, &in) {
			return
		}
		e := m.SaveAccountingCosts(c.Request.Context(), token(c, "admin"), in)
		reply(c, gin.H{"ok": true}, e)
	})
	admin.POST("/accounting/close", func(c *gin.Context) {
		var in management.CloseAccountingInput
		if !read(c, &in) {
			return
		}
		v, created, e := m.CloseAccounting(c.Request.Context(), token(c, "admin"), in)
		if e != nil {
			fail(c, e)
			return
		}
		status := 200
		if created {
			status = 201
		}
		c.JSON(status, v)
	})
	admin.GET("/accounting/report", func(c *gin.Context) {
		dates := []*int64{nil, nil}
		for i, k := range []string{"start", "end"} {
			if raw := c.Query(k); raw != "" {
				n, e := strconv.ParseInt(raw, 10, 64)
				if e != nil || len(raw) > 14 || n < 0 || strconv.FormatInt(n, 10) != raw {
					fail(c, fault.New(400, "时间范围无效。"))
					return
				}
				dates[i] = &n
			}
		}
		v, e := m.AccountingReport(c.Request.Context(), page(c), c.DefaultQuery("status", "success"), dates[0], dates[1])
		reply(c, v, e)
	})
	admin.GET("/accounting/:id", func(c *gin.Context) {
		v, e := m.AccountingDetail(c.Request.Context(), c.Param("id"), page(c))
		reply(c, v, e)
	})
}
