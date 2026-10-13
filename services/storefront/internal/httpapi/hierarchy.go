package httpapi

import (
	"github.com/gin-gonic/gin"
	"gptibo/storefront/internal/auth"
	"gptibo/storefront/internal/management"
)

func registerHierarchy(admin, partner *gin.RouterGroup, m management.Service, token func(*gin.Context, string) string, origin string) {
	own := func(c *gin.Context) string { return c.MustGet("principal").(auth.Principal).Subject }
	reply := func(c *gin.Context, v any, e error) {
		if e != nil {
			fail(c, e)
			return
		}
		c.JSON(200, v)
	}
	partner.GET("/upstream-orders", func(c *gin.Context) { v, e := m.UpstreamOrders(c.Request.Context(), own(c), page(c)); reply(c, v, e) })
	admin.POST("/partners/:id/parent", func(c *gin.Context) {
		var in struct {
			ParentID *string `json:"parentId"`
			Version  *int64  `json:"version"`
		}
		if !read(c, &in) {
			return
		}
		if in.ParentID == nil || in.Version == nil || *in.Version < 0 {
			c.JSON(400, gin.H{"error": "请提供上级与版本。"})
			return
		}
		reply(c, gin.H{"ok": true}, m.SetParent(c.Request.Context(), token(c, "admin"), c.Param("id"), *in.ParentID, *in.Version))
	})
	admin.POST("/partners/:id/recruit-permission", func(c *gin.Context) {
		var in struct {
			Enabled *bool  `json:"enabled"`
			Version *int64 `json:"version"`
		}
		if !read(c, &in) {
			return
		}
		if in.Enabled == nil || in.Version == nil || *in.Version < 0 {
			c.JSON(400, gin.H{"error": "请提供开放状态与版本。"})
			return
		}
		reply(c, gin.H{"ok": true}, m.SetRecruit(c.Request.Context(), token(c, "admin"), c.Param("id"), *in.Enabled, *in.Version))
	})
	partner.GET("/children", func(c *gin.Context) { v, e := m.Children(c.Request.Context(), own(c), page(c)); reply(c, v, e) })
	partner.GET("/child-default-prices", func(c *gin.Context) {
		v, e := m.ChildDefaults(c.Request.Context(), own(c))
		reply(c, v, e)
	})
	for _, apply := range []bool{false, true} {
		path := "/child-default-prices"
		if apply {
			path += "/apply"
		}
		partner.POST(path, func(c *gin.Context) {
			var in management.ChildDefaultsInput
			if !read(c, &in) {
				return
			}
			count, e := m.SaveChildDefaults(c.Request.Context(), token(c, "partner"), in, apply)
			reply(c, gin.H{"ok": true, "children": count}, e)
		})
	}
	partner.POST("/children", func(c *gin.Context) {
		var in struct {
			Email string `json:"email"`
		}
		if !read(c, &in) {
			return
		}
		id, slug, e := m.CreateChild(c.Request.Context(), token(c, "partner"), in.Email)
		if e != nil {
			fail(c, e)
			return
		}
		sent := m.InviteChild(c.Request.Context(), token(c, "partner"), id, origin) == nil
		message := "二级代理已创建，邀请邮件发送未确认，可重发邀请。"
		if sent {
			message = "二级代理已创建，邀请邮件已提交发送。"
		}
		reply(c, gin.H{"id": id, "slug": slug, "invitationSent": sent, "message": message}, nil)
	})
	partner.GET("/children/:id", func(c *gin.Context) {
		v, e := m.ChildDetail(c.Request.Context(), own(c), c.Param("id"))
		reply(c, v, e)
	})
	partner.POST("/children/:id", func(c *gin.Context) {
		var in management.ChildInput
		if !read(c, &in) {
			return
		}
		reply(c, gin.H{"ok": true}, m.SaveChild(c.Request.Context(), token(c, "partner"), c.Param("id"), in))
	})
	partner.POST("/children/:id/prices/:plan", func(c *gin.Context) {
		var in management.ChildPriceInput
		if !read(c, &in) {
			return
		}
		reply(c, gin.H{"ok": true}, m.ChildPrice(c.Request.Context(), token(c, "partner"), c.Param("id"), c.Param("plan"), in))
	})
	partner.GET("/children/:id/orders", func(c *gin.Context) {
		v, e := m.ChildOrders(c.Request.Context(), own(c), c.Param("id"), page(c))
		reply(c, v, e)
	})
	partner.POST("/children/:id/invitation", func(c *gin.Context) {
		var in struct{}
		if !read(c, &in) {
			return
		}
		reply(c, gin.H{"ok": true}, m.InviteChild(c.Request.Context(), token(c, "partner"), c.Param("id"), origin))
	})
}
