package httpapi

import (
	"github.com/gin-gonic/gin"
	"gptibo/storefront/internal/auth"
	"gptibo/storefront/internal/management"
)

func registerTickets(admin, partner *gin.RouterGroup, m management.Service, token func(*gin.Context, string) string) {
	reply := func(c *gin.Context, out any, e error) {
		if e != nil {
			fail(c, e)
			return
		}
		c.JSON(200, out)
	}
	admin.GET("/tickets/notifications", func(c *gin.Context) { out, e := m.TicketNotifications(c.Request.Context()); reply(c, out, e) })
	partner.POST("/tickets", func(c *gin.Context) {
		var in management.TicketInput
		if !read(c, &in) {
			return
		}
		e := m.CreateTicket(c.Request.Context(), token(c, "partner"), in)
		reply(c, gin.H{"id": in.Key}, e)
	})
	for _, item := range []struct {
		group *gin.RouterGroup
		role  string
	}{{admin, "admin"}, {partner, "partner"}} {
		scope := func(c *gin.Context) string {
			if item.role == "partner" {
				return c.MustGet("principal").(auth.Principal).Subject
			}
			return ""
		}
		item.group.GET("/tickets", func(c *gin.Context) {
			out, e := m.Tickets(c.Request.Context(), scope(c), page(c), c.Query("q"), c.Query("status"))
			reply(c, out, e)
		})
		item.group.GET("/tickets/:ticketID", func(c *gin.Context) {
			out, e := m.TicketDetail(c.Request.Context(), scope(c), c.Param("ticketID"))
			reply(c, out, e)
		})
		item.group.POST("/tickets/:ticketID/replies", func(c *gin.Context) {
			var in management.TicketReplyInput
			if !read(c, &in) {
				return
			}
			e := m.ReplyTicket(c.Request.Context(), item.role, token(c, item.role), c.Param("ticketID"), in)
			reply(c, gin.H{"ok": true}, e)
		})
	}
}
