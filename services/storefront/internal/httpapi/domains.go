package httpapi

import (
	"context"
	"github.com/gin-gonic/gin"
	"gptibo/storefront/internal/auth"
	"gptibo/storefront/internal/domains"
	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/security"
	"net"
	"net/http"
	"strings"
	"time"
)

func domainOrigin(c *gin.Context, fallback string) string {
	if scope := domains.Current(c.Request.Context()); scope.Host != "" {
		return "https://" + scope.Host
	}
	return fallback
}
func buyerHash(c *gin.Context, token string) string {
	if scope := domains.Current(c.Request.Context()); scope.Host != "" {
		return security.Hash(token + ":" + scope.AuthScope("customer"))
	}
	return security.Hash(token)
}
func storePartner(c *gin.Context, requested string) string {
	if scope := domains.Current(c.Request.Context()); scope.Host != "" {
		return scope.Slug
	}
	return requested
}
func customStorePath(path string) bool {
	return strings.HasPrefix(path, "/api/partner/") || path == "/api/catalog" || path == "/api/visitors" || path == "/api/interest" || path == "/api/interest/cancel" || path == "/api/orders" || strings.HasPrefix(path, "/api/orders/") || path == "/api/internal/domain-route"
}
func domainIngress(opts Options) gin.HandlerFunc {
	networks := []*net.IPNet{}
	for _, cidr := range opts.TrustedProxies {
		if _, n, e := net.ParseCIDR(cidr); e == nil {
			networks = append(networks, n)
		}
	}
	return func(c *gin.Context) {
		host := c.GetHeader("X-Storefront-Host")
		internal := c.Request.URL.Path == "/api/internal/domain-route"
		if host == "" && !internal {
			c.Next()
			return
		}
		remote, _, _ := net.SplitHostPort(c.Request.RemoteAddr)
		ip := net.ParseIP(remote)
		trusted := false
		for _, n := range networks {
			if n.Contains(ip) {
				trusted = true
			}
		}
		if !trusted || host == "" || opts.Domains == nil || !customStorePath(c.Request.URL.Path) {
			c.AbortWithStatus(404)
			return
		}
		normalized, e := domains.Normalize(host, opts.Domains.Zone)
		if e != nil || normalized != host {
			c.AbortWithStatus(404)
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
		scope, e := opts.Domains.Resolve(ctx, host)
		if e == nil && !strings.HasPrefix(c.Request.URL.Path, "/api/partner/") {
			slug := c.GetHeader("X-Storefront-Shop")
			if slug == "" && (c.Request.URL.Path == "/api/catalog" || internal) {
				slug = c.Query("partner")
			}
			scope, e = opts.Domains.SelectShop(ctx, scope, slug)
		}
		cancel()
		if e != nil {
			fail(c, e)
			return
		}
		c.Set("domainService", opts.Domains)
		c.Request = c.Request.WithContext(domains.WithScope(c.Request.Context(), scope))
		c.Next()
	}
}
func registerDomains(admin, partner *gin.RouterGroup, opts Options) {
	service := opts.Domains
	if service == nil {
		service = &domains.Service{Store: opts.Auth.Store}
	}
	own := func(c *gin.Context) string { return c.MustGet("principal").(auth.Principal).Subject }
	respond := func(c *gin.Context, id string) {
		v, e := service.Read(c.Request.Context(), id)
		if e != nil {
			fail(c, e)
			return
		}
		c.JSON(200, v)
	}
	partner.GET("/domain", func(c *gin.Context) { respond(c, own(c)) })
	admin.GET("/partners/:id/domain", func(c *gin.Context) { respond(c, c.Param("id")) })
	partner.POST("/domain", func(c *gin.Context) {
		var in struct {
			Hostname string `json:"hostname"`
			StoreID  string `json:"storeId"`
		}
		if !read(c, &in) {
			return
		}
		if e := opts.Auth.Limit(c.Request.Context(), "domain-bind:"+own(c), 10, 3600); e != nil {
			fail(c, e)
			return
		}
		if e := service.Bind(c.Request.Context(), own(c), in.Hostname, in.StoreID); e != nil {
			fail(c, e)
			return
		}
		respond(c, own(c))
	})
	partner.POST("/domain/check", func(c *gin.Context) {
		var in struct {
			ID string `json:"id"`
		}
		if !read(c, &in) {
			return
		}
		if e := opts.Auth.Limit(c.Request.Context(), "domain-check:"+own(c), 1, 30); e != nil {
			fail(c, e)
			return
		}
		if e := service.Check(c.Request.Context(), own(c), in.ID); e != nil {
			fail(c, e)
			return
		}
		respond(c, own(c))
	})
	partner.POST("/domain/remove", func(c *gin.Context) {
		var in struct {
			Hostname string `json:"hostname"`
		}
		if !read(c, &in) {
			return
		}
		if in.Hostname == "" {
			fail(c, fault.New(http.StatusBadRequest, "请确认要解除绑定的域名。"))
			return
		}
		if e := service.Remove(c.Request.Context(), own(c), in.Hostname); e != nil {
			fail(c, e)
			return
		}
		respond(c, own(c))
	})
}
