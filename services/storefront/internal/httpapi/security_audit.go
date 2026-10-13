package httpapi

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gptibo/storefront/internal/auth"
	"gptibo/storefront/internal/domains"
	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/securityaudit"
)

func securityAudit(s *securityaudit.Service, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		if !strings.HasPrefix(path, "/api/") || isCallback(path) || path == "/api/internal/domain-route" {
			c.Next()
			return
		}
		session := ""
		for _, role := range []string{"admin", "partner", "customer", "buyer"} {
			for _, prefix := range []string{"__Host-sf_", "sf_"} {
				value, _ := c.Cookie(prefix + role)
				if len(value) >= 32 && len(value) <= 256 {
					session = s.Auth.Fingerprint("session", value)
					break
				}
			}
			if session != "" {
				break
			}
		}
		if session == "" {
			for _, name := range []string{"__Host-ymx_buyer", "ymx_buyer_dev"} {
				value, _ := c.Cookie(name)
				if len(value) >= 32 && len(value) <= 256 {
					session = s.Auth.Fingerprint("session", value)
					break
				}
			}
		}
		ip := c.ClientIP()
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		blocked, err := s.Blocked(ctx, ip, session)
		cancel()
		if err != nil {
			fail(c, fault.New(503, "安全校验暂不可用，请稍后重试。"))
			return
		}
		if blocked {
			fail(c, fault.New(429, "请求异常频繁，请 15 分钟后重试。"))
			c.Header("Retry-After", "900")
			return
		}
		c.Next()
		route := c.FullPath()
		status := c.Writer.Status()
		sensitive := strings.HasPrefix(route, "/api/admin/") || strings.HasPrefix(route, "/api/partner/") || strings.HasPrefix(route, "/api/orders")
		suspicious := status == 401 || status == 403 || status == 404 || status == 405 || (status == 400 && (strings.Contains(route, "/auth/") || strings.Contains(route, "/recovery/") || strings.Contains(route, "/admin/login")))
		if !sensitive && !suspicious {
			return
		}
		if route == "" {
			route = "unmatched"
		} // Never persist attacker-controlled paths or queries.
		e := securityaudit.Event{IP: ip, Host: domains.Current(c.Request.Context()).Host, RequestID: c.Writer.Header().Get("X-Request-ID"), Route: route, Method: c.Request.Method, Status: status, Role: "guest", Session: session, Account: c.GetString("audit-account")}
		if value := c.GetString("audit-session"); value != "" {
			e.Session = value
			e.Role = c.GetString("audit-role")
		}
		if p, ok := c.Get("principal"); ok {
			if principal, ok := p.(auth.Principal); ok {
				e.Role = principal.Role
				e.Account = securityaudit.Mask(principal.Subject)
			}
		}
		if target := c.Param("id"); target != "" && len(target) <= 128 {
			e.Target = s.Auth.Fingerprint("target", target)
		}
		// The handler's short request context may already be cancelled; audit independently.
		ctx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err = s.Record(ctx, e, suspicious); err != nil {
			logger.Error("security audit unavailable", "request_id", e.RequestID)
		}
	}
}
