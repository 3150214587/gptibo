package httpapi

import (
	"github.com/gin-gonic/gin"
	"gptibo/storefront/internal/auth"
	"gptibo/storefront/internal/management"
)

func registerCustomers(admin, partner *gin.RouterGroup, m management.Service, token func(*gin.Context, string) string) {
	for _, scope := range []struct {
		role  string
		group *gin.RouterGroup
	}{{"admin", admin}, {"partner", partner}} {
		principal := func(c *gin.Context) auth.Principal { return c.MustGet("principal").(auth.Principal) }
		respond := func(c *gin.Context, out any, e error) {
			if e != nil {
				fail(c, e)
				return
			}
			c.JSON(200, out)
		}
		scope.group.GET("/customers", func(c *gin.Context) {
			out, e := m.Customers(c.Request.Context(), principal(c), c.Request.URL.Query(), false)
			respond(c, out, e)
		})
		scope.group.GET("/customers/export", func(c *gin.Context) {
			out, e := m.Customers(c.Request.Context(), principal(c), c.Request.URL.Query(), true)
			respond(c, out, e)
		})
		scope.group.GET("/customers/:id", func(c *gin.Context) {
			out, e := m.CustomerDetail(c.Request.Context(), principal(c), c.Param("id"))
			respond(c, out, e)
		})
		scope.group.POST("/customers", func(c *gin.Context) {
			var in management.CustomerInput
			if !read(c, &in) {
				return
			}
			out, e := m.SaveCustomer(c.Request.Context(), scope.role, token(c, scope.role), "", in)
			respond(c, out, e)
		})
		scope.group.POST("/customers/:id/edit", func(c *gin.Context) {
			var in management.CustomerInput
			if !read(c, &in) {
				return
			}
			out, e := m.SaveCustomer(c.Request.Context(), scope.role, token(c, scope.role), c.Param("id"), in)
			respond(c, out, e)
		})
		scope.group.POST("/customers/:id/accounts", func(c *gin.Context) {
			var in management.AccountInput
			if !read(c, &in) {
				return
			}
			out, e := m.SaveAccount(c.Request.Context(), scope.role, token(c, scope.role), c.Param("id"), in, true)
			respond(c, out, e)
		})
		scope.group.POST("/customers/accounts/:id/edit", func(c *gin.Context) {
			var in management.AccountInput
			if !read(c, &in) {
				return
			}
			out, e := m.SaveAccount(c.Request.Context(), scope.role, token(c, scope.role), c.Param("id"), in, false)
			respond(c, out, e)
		})
		scope.group.POST("/customers/accounts/:id/subscriptions", func(c *gin.Context) {
			var in management.SubscriptionInput
			if !read(c, &in) {
				return
			}
			out, e := m.SaveSubscription(c.Request.Context(), scope.role, token(c, scope.role), c.Param("id"), in)
			respond(c, out, e)
		})
	}
}
