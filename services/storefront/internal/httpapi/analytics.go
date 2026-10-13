package httpapi

import (
	"github.com/gin-gonic/gin"
	"gptibo/storefront/internal/auth"
	"gptibo/storefront/internal/domains"
	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/management"
	"strconv"
	"strings"
)

func registerAnalytics(group, admin, partner *gin.RouterGroup, m management.Service, a *auth.Service) {
	number := func(c *gin.Context, key, value string) int {
		n, e := strconv.Atoi(c.DefaultQuery(key, value))
		if e != nil {
			return -1
		}
		return n
	}
	reply := func(c *gin.Context, out any, e error) {
		if e != nil {
			fail(c, e)
			return
		}
		c.JSON(200, out)
	}
	own := func(c *gin.Context) string { return c.MustGet("principal").(auth.Principal).Subject }
	group.POST("/visitors", func(c *gin.Context) {
		var in management.VisitorInput
		if !read(c, &in) {
			return
		}
		if e := a.Limit(c.Request.Context(), "visitors:"+c.ClientIP(), 120, 60); e != nil {
			fail(c, e)
			return
		}
		if scope := domains.Current(c.Request.Context()); scope.Host != "" {
			if !strings.HasPrefix(in.Path, "/") {
				fail(c, fault.New(400, "页面路径无效。"))
				return
			}
			if strings.HasPrefix(in.Path, "/r/") || strings.HasPrefix(in.Path, "/s/") {
				parts := strings.Split(in.Path, "/")
				if len(parts) < 3 || parts[2] != scope.Slug {
					fail(c, fault.New(404, "商店不可用。"))
					return
				}
			} else {
				in.Path = "/r/" + scope.Slug + in.Path
			}

		}
		e := m.RecordVisitor(c.Request.Context(), in)
		reply(c, gin.H{"ok": true}, e)
	})
	partner.GET("/visitors", func(c *gin.Context) {
		out, e := m.Visitors(c.Request.Context(), number(c, "days", "7"), own(c), false)
		reply(c, out, e)
	})
	admin.GET("/visitors", func(c *gin.Context) {
		out, e := m.Visitors(c.Request.Context(), number(c, "days", "7"), "", true)
		reply(c, out, e)
	})
	admin.GET("/partners/:id/visitors", func(c *gin.Context) {
		if _, e := m.Partner(c.Request.Context(), c.Param("id")); e != nil {
			fail(c, e)
			return
		}
		out, e := m.Visitors(c.Request.Context(), number(c, "days", "7"), c.Param("id"), false)
		reply(c, out, e)
	})
	admin.GET("/partners/ranking", func(c *gin.Context) {
		out, e := m.Ranking(c.Request.Context(), number(c, "days", "1"), page(c))
		reply(c, out, e)
	})
	for _, cancel := range []bool{false, true} {
		path := "/interest"
		if cancel {
			path += "/cancel"
		}
		group.POST(path, func(c *gin.Context) {
			var in management.InterestInput
			if !read(c, &in) {
				return
			}
			if e := a.Limit(c.Request.Context(), "interest:"+c.ClientIP(), 30, 60); e != nil {
				fail(c, e)
				return
			}
			out, e := m.Interest(c.Request.Context(), in, cancel)
			reply(c, out, e)
		})
	}
	admin.GET("/interest", func(c *gin.Context) { out, e := m.InterestList(c.Request.Context(), page(c)); reply(c, out, e) })
}
