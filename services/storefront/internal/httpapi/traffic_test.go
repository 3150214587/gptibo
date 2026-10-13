package httpapi

import (
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestTrafficRejectsBeforeHandlerAndSeparatesCallbacks(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.SetTrustedProxies(nil)
	r.Use(trafficProtection(Options{}))
	calls := 0
	r.Any("/api/orders", func(c *gin.Context) { calls++; c.Status(200) })
	r.Any("/api/payments/notify/:provider", func(c *gin.Context) { c.Status(400) }) // Real handler must still validate signature.
	request := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", path, nil)
		req.RemoteAddr = "203.0.113.1:1234"
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	for i := 0; i < 30; i++ {
		if w := request("/api/orders"); w.Code != 200 {
			t.Fatal(w.Code)
		}
	}
	if w := request("/api/orders"); w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatal(w.Code)
	}
	if calls != 30 {
		t.Fatal("rejected request reached handler", calls)
	}
	if w := request("/api/payments/notify/alipay"); w.Code != 400 {
		t.Fatal("normal attack blocked callback validation", w.Code)
	}
}
func TestTrafficConcurrencyIsolationAndRelease(t *testing.T) {
	g := newTrafficGate()
	var held []func()
	for i := 0; i < 8; i++ {
		release, status := g.acquire("a", "a.example.com", false)
		if status != 0 {
			t.Fatal(status)
		}
		held = append(held, release)
	}
	if _, status := g.acquire("a", "a.example.com", false); status != 503 {
		t.Fatal(status)
	}
	for _, host := range []string{"b.example.com", ""} {
		release, status := g.acquire("b", host, false)
		if status != 0 {
			t.Fatal("other shop starved", status)
		}
		release()
	}
	release, status := g.acquire("a", "", true)
	if status != 0 {
		t.Fatal("callback starved", status)
	}
	release()
	for _, release := range held {
		release()
	}
	release, status = g.acquire("a", "a.example.com", false)
	if status != 0 {
		t.Fatal("slots leaked")
	}
	release()
	if len(g.hosts) != 0 {
		t.Fatal("idle host entries leaked")
	}
}
func TestTrafficKeyBoundExpiryAndIPv6Grouping(t *testing.T) {
	g := newTrafficGate()
	g.maxKeys = 2
	clock := time.Now()
	g.now = func() time.Time { return clock }
	release, status := g.acquire("one", "", false)
	if status != 0 {
		t.Fatal(status)
	}
	release()
	if _, status = g.acquire("two", "", false); status != 429 {
		t.Fatal("key capacity not enforced", status)
	}
	if len(g.buckets) > 2 {
		t.Fatal("unbounded keys")
	}
	callbackRelease, callbackStatus := g.acquire("provider", "", true)
	if callbackStatus != 0 {
		t.Fatal("full visitor map denied callback", callbackStatus)
	}
	callbackRelease()
	clock = clock.Add(3 * time.Minute)
	release, status = g.acquire("two", "", false)
	if status != 0 {
		t.Fatal("expired entries not removed", status)
	}
	release()
	if len(g.callbackBuckets) != 0 {
		t.Fatal("expired callback entries not removed")
	}
	if trafficIP("2001:db8::1") != trafficIP("2001:db8::ffff") || trafficIP("::ffff:192.0.2.1") != "192.0.2.1" {
		t.Fatal("IP grouping")
	}
}
func TestTrafficConcurrentBudgetIsAtomic(t *testing.T) {
	g := newTrafficGate()
	var wg sync.WaitGroup
	admitted := make(chan func(), 50)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if release, status := g.acquire("one", "a.example.com", false); status == 0 {
				admitted <- release
			}
		}()
	}
	wg.Wait()
	close(admitted)
	if len(admitted) != 8 {
		t.Fatal("concurrent tenant cap bypassed", len(admitted))
	}
	for release := range admitted {
		release()
	}
}
func TestTrafficRejectsSpoofedTenantBeforeHandler(t *testing.T) {
	r := gin.New()
	r.SetTrustedProxies(nil)
	r.Use(trafficProtection(Options{}))
	r.GET("/api/catalog", func(c *gin.Context) { t.Fatal("spoof reached handler") })
	req := httptest.NewRequest(http.MethodGet, "/api/catalog", nil)
	req.Header.Set("X-Storefront-Host", "other.example.com")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
}
