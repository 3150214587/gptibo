package httpapi

import (
	"log/slog"
	"net"
	"net/netip"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

var callbackPath = regexp.MustCompile(`^/api/(payments|invoices|domain-billing)/notify/[a-z0-9_-]+$`)

func isCallback(path string) bool {
	return callbackPath.MatchString(path) || path == "/api/recharge/webhook" || path == "/api/telegram/webhook"
}

type trafficBucket struct {
	tokens  float64
	updated time.Time
}
type trafficGate struct {
	mu                 sync.Mutex
	buckets            map[string]trafficBucket
	callbackBuckets    map[string]trafficBucket
	hosts              map[string]int
	nextSweep          time.Time
	nextReport         time.Time
	rejected           int
	general, callbacks int
	maxKeys            int
	now                func() time.Time
}

func newTrafficGate() *trafficGate {
	return &trafficGate{buckets: make(map[string]trafficBucket), callbackBuckets: make(map[string]trafficBucket), hosts: make(map[string]int), maxKeys: 32768, now: time.Now}
}

// Called under mu. Refuse new keys at capacity instead of growing memory or evicting active budgets.
func (g *trafficGate) token(buckets map[string]trafficBucket, key string, rate, burst float64, now time.Time) bool {
	b, exists := buckets[key]
	if !exists {
		if len(buckets) >= g.maxKeys {
			return false
		}
		b = trafficBucket{tokens: burst, updated: now}
	}
	b.tokens = min(burst, b.tokens+max(0, now.Sub(b.updated).Seconds())*rate)
	b.updated = now
	allowed := b.tokens >= 1
	if allowed {
		b.tokens--
	}
	buckets[key] = b
	return allowed
}
func (g *trafficGate) acquire(ip, host string, callback bool) (func(), int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	if !now.Before(g.nextSweep) {
		for k, b := range g.buckets {
			if now.Sub(b.updated) > 2*time.Minute {
				delete(g.buckets, k)
			}
		}
		for k, b := range g.callbackBuckets {
			if now.Sub(b.updated) > 2*time.Minute {
				delete(g.callbackBuckets, k)
			}
		}
		g.nextSweep = now.Add(time.Minute)
	}
	group := "web:"
	buckets := g.buckets
	if callback {
		group = "callback:"
		buckets = g.callbackBuckets
	}
	if !g.token(buckets, group+"ip:"+ip, 2, 30, now) {
		return nil, 429
	}
	if callback {
		if g.callbacks >= 4 {
			return nil, 503
		}
		g.callbacks++
	} else {
		rate, burst, concurrent := 10.0, 30.0, 8
		if host == "" {
			rate, burst, concurrent = 30, 60, 16
		}
		if !g.token(buckets, group+"host:"+host, rate, burst, now) {
			return nil, 429
		}
		if g.general >= 24 || g.hosts[host] >= concurrent {
			return nil, 503
		}
		g.general++
		g.hosts[host]++
	}
	return func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		if callback {
			g.callbacks--
		} else {
			g.general--
			g.hosts[host]--
			if g.hosts[host] == 0 {
				delete(g.hosts, host)
			}
		}
	}, 0
}
func trafficIP(raw string) string {
	ip, err := netip.ParseAddr(raw)
	if err != nil {
		return "unknown"
	}
	ip = ip.Unmap()
	if ip.Is6() {
		return netip.PrefixFrom(ip, 64).Masked().String()
	}
	return ip.String()
}
func trafficProtection(opts Options, loggers ...*slog.Logger) gin.HandlerFunc {
	gate := newTrafficGate()
	networks := []*net.IPNet{}
	for _, cidr := range opts.TrustedProxies {
		if _, n, err := net.ParseCIDR(cidr); err == nil {
			networks = append(networks, n)
		}
	}
	return func(c *gin.Context) {
		if !strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.Next()
			return
		}
		host := c.GetHeader("X-Storefront-Host")
		if host != "" {
			remote, _, _ := net.SplitHostPort(c.Request.RemoteAddr)
			trusted := false
			for _, network := range networks {
				if network.Contains(net.ParseIP(remote)) {
					trusted = true
					break
				}
			}
			if !trusted || len(host) > 253 {
				c.AbortWithStatus(404)
				return
			}
		}
		release, status := gate.acquire(trafficIP(c.ClientIP()), host, host == "" && isCallback(c.Request.URL.Path))
		if status != 0 {
			// Emit aggregate warnings at most once per 30s, never one log per attacking request.
			gate.mu.Lock()
			gate.rejected++
			if len(loggers) > 0 && loggers[0] != nil && !gate.now().Before(gate.nextReport) {
				loggers[0].Warn("traffic protection rejected requests", "rejected", gate.rejected, "status", status)
				gate.rejected = 0
				gate.nextReport = gate.now().Add(30 * time.Second)
			}
			gate.mu.Unlock()
			c.Header("Retry-After", "10")
			c.Header("Cache-Control", "no-store")
			c.Header("X-Traffic-Protection", "origin")
			c.AbortWithStatusJSON(status, gin.H{"error": "访问过于频繁或服务繁忙，请稍后重试。"})
			return
		}
		defer release()
		c.Next()
	}
}
