package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"gptibo/storefront/internal/auth"
	"gptibo/storefront/internal/domains"
	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/httpapi"
	"gptibo/storefront/internal/security"
	"gptibo/storefront/internal/store"
)

type domainProvider struct {
	h                domains.Hostname
	creates, deletes int
	failDelete       bool
}

func (p *domainProvider) Ensure(_ context.Context, host string) (domains.Hostname, error) {
	p.creates++
	p.h.Hostname = host
	p.h.ID = "cf-domain"
	return p.h, nil
}
func (p *domainProvider) Get(context.Context, string) (domains.Hostname, error) { return p.h, nil }
func (p *domainProvider) Find(context.Context, string) (domains.Hostname, error) {
	if p.h.ID == "" {
		return p.h, domains.ErrProviderMissing
	}
	return p.h, nil
}
func (p *domainProvider) Delete(context.Context, string) error {
	p.deletes++
	if p.failDelete {
		return errors.New("unavailable")
	}
	p.h.ID = ""
	return nil
}

type domainDNS struct {
	txt, cname string
	addresses  map[string][]net.IPAddr
}

func completedDomainOrders(t *testing.T, owner *store.Store, partner string, count int) {
	t.Helper()
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents) VALUES('plus','chatgpt','Plus',10000) ON CONFLICT DO NOTHING`)
	execSQL(t, owner, `INSERT INTO orders(id,owner_hash,idempotency_key,plan,plan_name,price_cents,partner_id,mode,payment_status,status,created_at,updated_at,last_event_id)
SELECT $1||'-domain-'||n,$1,$1||'-domain-'||n,'plus','Plus',10000,$1,'live','paid','success',1,1,'fixture' FROM generate_series(1,$2::int) n`, partner, count)
}

func TestCustomDomainRequiresThreeValidOrders(t *testing.T) {
	owner, app, identity, _, mail := identityFixture(t)
	ctx := context.Background()
	execSQL(t, owner, `INSERT INTO partners(id,slug,name,email,active) VALUES('a','ABC123','A','a@example.test',true),('b','DEF456','B','b@example.test',true)`)
	p := &domainProvider{}
	svc := &domains.Service{Store: app, Provider: p, DNS: &domainDNS{cname: "shops.shop.example.invalid."}, Target: "shops.shop.example.invalid", Zone: "shop.example.invalid", Limit: 100}
	h := httpapi.Router(app, "fixture", slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.Options{Origin: testOrigin, Auth: identity, Domains: svc})
	partner := login(t, h, mail, "partner", "a@example.test")
	assertCount := func(count int, eligible bool) {
		t.Helper()
		v := object(t, request(h, "GET", "/api/partner/domain", nil, partner), 200)
		if v["completedOrders"] != float64(count) || v["eligible"] != eligible || v["requiredOrders"] != float64(3) {
			t.Fatal(v)
		}
	}
	assertCount(0, false)
	object(t, request(h, "POST", "/api/partner/domain", map[string]any{"hostname": "shop.example.com"}, partner), 403)
	completedDomainOrders(t, owner, "a", 8)
	completedDomainOrders(t, owner, "b", 30)
	execSQL(t, owner, `UPDATE orders SET is_test=1 WHERE id='a-domain-3'; UPDATE orders SET mode='demo',payment_status='simulated' WHERE id='a-domain-4'; UPDATE orders SET status='processing' WHERE id='a-domain-5'; UPDATE orders SET refunded_cents=1 WHERE id='a-domain-6'; UPDATE orders SET refund_hold=1 WHERE id='a-domain-7'; UPDATE orders SET payment_status='unpaid' WHERE id='a-domain-8'`)
	assertCount(2, false)
	object(t, request(h, "POST", "/api/partner/domain", map[string]any{"hostname": "shop.example.com"}, partner), 403)
	var bindings int
	if e := owner.DB.QueryRow(ctx, `SELECT count(*) FROM partner_domains`).Scan(&bindings); e != nil || bindings != 0 || p.creates != 0 {
		t.Fatal("ineligible binding created resources", bindings, e)
	}
	execSQL(t, owner, `UPDATE orders SET status='success' WHERE id='a-domain-5'`)
	assertCount(3, true)
	object(t, request(h, "POST", "/api/partner/domain", map[string]any{"hostname": "shop.example.com"}, partner), 200)
	// Refund before activation must not consume a provider hostname; removal still works.
	execSQL(t, owner, `UPDATE orders SET refunded_cents=10000 WHERE id='a-domain-5'`)
	assertCount(2, false)
	if e := svc.Check(ctx, "a"); e != nil {
		t.Fatal(e)
	}
	v, e := svc.Read(ctx, "a")
	if e != nil || v.Domain.State != "error" || p.creates != 0 {
		t.Fatal("pending activation bypassed eligibility", v, e)
	}
	object(t, request(h, "POST", "/api/partner/domain/remove", map[string]any{"hostname": "shop.example.com"}, partner), 200)
	if e = svc.Check(ctx, "a"); e != nil {
		t.Fatal(e)
	}
}

func TestCustomDomainPartnerExceptionOnlyBypassesOrderThreshold(t *testing.T) {
	owner, app, identity, _, mail := identityFixture(t)
	ctx := context.Background()
	execSQL(t, owner, `INSERT INTO partners(id,slug,name,email,active) VALUES('a','ABC123','A','a@example.test',true),('b','DEF456','B','b@example.test',true),('inactive','GHI789','Inactive','inactive@example.test',false)`)
	completedDomainOrders(t, owner, "a", 1)
	p := &domainProvider{}
	dns := &domainDNS{cname: "shops.shop.example.invalid."}
	svc := &domains.Service{Store: app, Provider: p, DNS: dns, Target: "shops.shop.example.invalid", Zone: "shop.example.invalid", Limit: 1, EligibilityOverrides: map[string]bool{"a": true, "inactive": true}}
	h := httpapi.Router(app, "fixture", slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.Options{Origin: testOrigin, Auth: identity, Domains: svc})
	partner := login(t, h, mail, "partner", "a@example.test")
	v := object(t, request(h, "GET", "/api/partner/domain", nil, partner), 200)
	if v["eligible"] != true || v["completedOrders"] != float64(1) || v["requiredOrders"] != float64(3) {
		t.Fatal("exception must expose permission without fabricating order count", v)
	}
	if err := svc.Bind(ctx, "b", "b.example.com"); fault.Status(err) != 403 {
		t.Fatal("another partner bypassed the threshold", err)
	}
	if err := svc.Bind(ctx, "inactive", "inactive.example.com"); fault.Status(err) != 409 {
		t.Fatal("exception enabled an inactive account", err)
	}
	object(t, request(h, "POST", "/api/partner/domain", map[string]any{"hostname": "a.example.com"}, partner), 200)
	if err := svc.Check(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	view, err := svc.Read(ctx, "a")
	if err != nil || view.Domain.State != "pending_dns" || p.creates != 0 {
		t.Fatal("exception bypassed DNS ownership", view, err)
	}
	dns.txt = view.Domain.Verification
	if err = svc.Check(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err = svc.Check(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if p.creates != 1 {
		t.Fatal("background activation ignored the exception")
	}
	if _, err = svc.Resolve(ctx, "a.example.com"); err == nil {
		t.Fatal("exception bypassed certificate activation")
	}
	svc.EligibilityOverrides["b"] = true
	if err = svc.Bind(ctx, "b", "b.example.com"); fault.Status(err) != 409 {
		t.Fatal("exception bypassed capacity", err)
	}
	delete(svc.EligibilityOverrides, "a")
	if err = svc.Check(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	view, err = svc.Read(ctx, "a")
	if err != nil || view.Eligible || view.Domain.State != "error" || p.creates != 1 {
		t.Fatal("revoked exception still qualified", view, err)
	}
	svc.EligibilityOverrides["a"] = true
	p.h.Status, p.h.SSL.Status = "active", "active"
	if err = svc.Check(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Resolve(ctx, "a.example.com"); err != nil {
		t.Fatal("approved verified hostname did not activate", err)
	}
}

func TestCustomDomainHundredPartnerCapConcurrentReservation(t *testing.T) {
	owner, app, _, _, _ := identityFixture(t)
	ctx := context.Background()
	execSQL(t, owner, `INSERT INTO partners(id,slug,name,email,active) SELECT 'cap-'||n,'CAP'||lpad(n::text,3,'0'),'Cap','cap-'||n||'@example.test',true FROM generate_series(1,99) n;
INSERT INTO partner_domains(partner_id,hostname,verification,message) SELECT id,id||'.example.com','fixture','fixture' FROM partners WHERE id LIKE 'cap-%';
INSERT INTO partners(id,slug,name,email,active) VALUES('a','ABC123','A','a@example.test',true),('b','DEF456','B','b@example.test',true)`)
	completedDomainOrders(t, owner, "a", 3)
	completedDomainOrders(t, owner, "b", 3)
	p := &domainProvider{}
	svc := &domains.Service{Store: app, Provider: p, Target: "shops.shop.example.invalid", Zone: "shop.example.invalid", Limit: 100}
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make([]error, 2)
	ids := []string{"a", "b"}
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			<-start
			results[i] = svc.Bind(ctx, id, id+".example.com")
		}(i, id)
	}
	close(start)
	wg.Wait()
	winner, loser := "", ""
	for i, err := range results {
		if err == nil {
			if winner != "" {
				t.Fatal("both applicants obtained the last slot")
			}
			winner = ids[i]
		} else {
			if fault.Status(err) != 409 {
				t.Fatal(err)
			}
			loser = ids[i]
		}
	}
	if winner == "" || loser == "" {
		t.Fatal("expected exactly one successful reservation", results)
	}
	assertFull := func() {
		t.Helper()
		var count int
		if err := owner.DB.QueryRow(ctx, `SELECT count(*) FROM partner_domains`).Scan(&count); err != nil || count != 100 {
			t.Fatal(count, err)
		}
		if err := svc.Bind(ctx, loser, loser+".example.com"); fault.Status(err) != 409 {
			t.Fatal("101st partner accepted", err)
		}
	}
	assertFull()
	if err := svc.Bind(ctx, winner, winner+".example.com"); err != nil {
		t.Fatal("existing binding retry consumed another slot", err)
	}
	if err := svc.Remove(ctx, winner, winner+".example.com"); err != nil {
		t.Fatal(err)
	}
	assertFull() // A deleting record reserves its slot until provider cleanup finishes.
	if err := svc.Check(ctx, winner); err != nil {
		t.Fatal(err)
	}
	if err := svc.Bind(ctx, loser, loser+".example.com"); err != nil {
		t.Fatal("cleaned binding did not release capacity", err)
	}
}

func (d *domainDNS) LookupTXT(context.Context, string) ([]string, error) { return []string{d.txt}, nil }
func (d *domainDNS) LookupCNAME(context.Context, string) (string, error) { return d.cname, nil }
func (d *domainDNS) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	return d.addresses[host], nil
}

func TestCustomDomainApexLifecycle(t *testing.T) {
	owner, app, identity, _, _ := identityFixture(t)
	ctx := context.Background()
	execSQL(t, owner, `INSERT INTO partners(id,slug,name,email,active) VALUES('apex','APEX09','Apex','apex@example.test',true)`)
	completedDomainOrders(t, owner, "apex", 21)
	p := &domainProvider{}
	dns := &domainDNS{cname: "example.com.", addresses: map[string][]net.IPAddr{
		"example.com":      {{IP: net.ParseIP("203.0.113.1")}},
		"shops.shop.example.invalid": {{IP: net.ParseIP("104.21.1.2")}},
	}}
	svc := &domains.Service{Store: app, Provider: p, DNS: dns, Target: "shops.shop.example.invalid", Zone: "shop.example.invalid", Limit: 100}
	if e := svc.Bind(ctx, "apex", "example.com"); e != nil {
		t.Fatal(e)
	}
	check := func() {
		t.Helper()
		if e := svc.Check(ctx, "apex"); e != nil {
			t.Fatal(e)
		}
	}
	check()
	v, e := svc.Read(ctx, "apex")
	if e != nil || !v.Domain.Apex || v.Domain.State != "pending_dns" || p.creates != 0 {
		t.Fatal(v, e)
	}
	dns.txt = v.Domain.Verification
	check()
	if p.creates != 0 {
		t.Fatal("provider created for incorrect flattened target")
	}
	dns.addresses["example.com"] = dns.addresses["shops.shop.example.invalid"]
	check()
	check()
	if p.creates != 1 {
		t.Fatal("flattened apex did not create provider hostname")
	}
	p.h.Status = "pending"
	p.h.VerificationErrors = []string{"custom hostname does not CNAME to this zone."}
	check()
	v, e = svc.Read(ctx, "apex")
	if e != nil || v.Domain.State != "pending_dns" || !strings.Contains(v.Domain.Message, "ALIAS") || v.Domain.ProviderID != "cf-domain" {
		t.Fatal("provider rejected flattened apex must show DNS action and retain binding", v, e)
	}
	if _, e := svc.Resolve(ctx, "example.com"); e == nil {
		t.Fatal("provider rejected apex routed despite matching addresses")
	}
	p.h.Status = "active"
	check()
	v, e = svc.Read(ctx, "apex")
	if e != nil || v.Domain.State != "pending_certificate" || p.creates != 1 {
		t.Fatal("approved routing must advance to certificate validation without recreating", v, e)
	}
	if _, e := svc.Resolve(ctx, "example.com"); e == nil {
		t.Fatal("unissued certificate routed")
	}
	p.h.SSL.Status = "active"
	check()
	scope, e := svc.Resolve(ctx, "example.com")
	if e != nil || scope.Slug != "APEX09" {
		t.Fatal(scope, e)
	}
	h := httpapi.Router(app, "fixture", slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.Options{Origin: testOrigin, Auth: identity, Domains: svc, TrustedProxies: []string{"127.0.0.1/32"}})
	r := httptest.NewRequest("GET", "/api/catalog", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("X-Storefront-Host", "example.com")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	result := object(t, w, 200)
	if result["partner"].(map[string]any)["slug"] != "APEX09" {
		t.Fatal("apex tenant lost")
	}
	dns.addresses["example.com"] = []net.IPAddr{{IP: net.ParseIP("203.0.113.1")}}
	check()
	if _, e := svc.Resolve(ctx, "example.com"); e == nil {
		t.Fatal("changed DNS still routed")
	}
}

func TestCustomDomainLifecycleAndIngress(t *testing.T) {
	owner, app, a, _, _ := identityFixture(t)
	ctx := context.Background()
	execSQL(t, owner, `INSERT INTO partners(id,slug,name,email,active) VALUES('a','ABC123','A','a@example.test',true),('b','DEF456','B','b@example.test',true);
 INSERT INTO catalog_plans(id,product_id,name,price_cents,enabled,partner_enabled) VALUES('plus','chatgpt','Plus',16000,true,true);
 INSERT INTO partner_prices(partner_id,plan,price_cents,cost_cents) VALUES('a','plus',18000,10000),('b','plus',19000,11000);`)
	p := &domainProvider{}
	completedDomainOrders(t, owner, "a", 21)
	completedDomainOrders(t, owner, "b", 21)
	dns := &domainDNS{cname: "shops.shop.example.invalid."}
	svc := &domains.Service{Store: app, Provider: p, DNS: dns, Target: "shops.shop.example.invalid", Zone: "shop.example.invalid", Limit: 100}
	if e := svc.Bind(ctx, "a", "shop.example.com"); e != nil {
		t.Fatal(e)
	}
	if e := svc.Bind(ctx, "b", "shop.example.com"); e == nil {
		t.Fatal("duplicate domain allowed")
	}
	if e := svc.Check(ctx, "a"); e != nil {
		t.Fatal(e)
	}
	if p.creates != 0 {
		t.Fatal("provider called before ownership verified")
	}
	view, e := svc.Read(ctx, "a")
	if e != nil {
		t.Fatal(e)
	}
	dns.txt = view.Domain.Verification
	if e = svc.Check(ctx, "a"); e != nil {
		t.Fatal(e)
	}
	if e = svc.Check(ctx, "a"); e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Resolve(ctx, "shop.example.com"); e == nil {
		t.Fatal("pending certificate routed")
	}
	p.h.Status = "active"
	p.h.SSL.Status = "active"
	if e = svc.Check(ctx, "a"); e != nil {
		t.Fatal(e)
	}
	h := httpapi.Router(app, "fixture", slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.Options{Origin: testOrigin, Auth: a, OrderTesting: true, Domains: svc, TrustedProxies: []string{"127.0.0.1/32"}})
	requestDomain := func(method, path, origin, remote string, body any, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		data, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(data))
		r.RemoteAddr = remote
		r.Header.Set("X-Storefront-Host", "shop.example.com")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		for _, cookie := range cookies {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	call := func(method, path string, body any, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		return requestDomain(method, path, "https://shop.example.com", "127.0.0.1:1234", body, cookies...)
	}
	object(t, call("GET", "/api/catalog?partner=DEF456", nil), 404)
	result := object(t, call("GET", "/api/catalog", nil), 200)
	if result["partner"].(map[string]any)["slug"] != "ABC123" {
		t.Fatal("host tenant spoofed")
	}
	for _, path := range []string{"/api/admin/session", "/api/payments/notify/alipay"} {
		if call("GET", path, nil).Code != 404 {
			t.Fatal("management/callback exposed on customer domain")
		}
	}
	if requestDomain("GET", "/api/catalog", "", "203.0.113.1:1234", nil).Code != 404 {
		t.Fatal("untrusted header accepted")
	}
	if requestDomain("POST", "/api/orders/client", testOrigin, "127.0.0.1:1234", map[string]any{}).Code != 403 {
		t.Fatal("cross-origin write accepted")
	}
	buyer := &http.Cookie{Name: "__Host-sf_buyer", Value: security.Token()}
	in := map[string]any{"plan": "plus", "email": "buyer@example.test", "key": security.ID(), "expectedPrice": 18000, "partner": "DEF456"}
	spoofed := object(t, call("POST", "/api/orders", in, buyer), 201)["order"].(map[string]any)
	if spoofed["storeId"] != "a" {
		t.Fatal("request body overrode trusted domain scope")
	}
	in["key"] = security.ID()
	in["partner"] = "ABC123"
	order := object(t, call("POST", "/api/orders", in, buyer), 201)["order"].(map[string]any)
	id := order["id"].(string)
	var partnerID string
	if e = owner.DB.QueryRow(ctx, "SELECT partner_id FROM orders WHERE id=$1", id).Scan(&partnerID); e != nil || partnerID != "a" {
		t.Fatal("order attributed to another partner", e)
	}
	object(t, request(h, "GET", "/api/orders/"+id, nil, buyer), 404)
	object(t, call("POST", "/api/visitors", map[string]any{"visitorId": security.ID(), "path": "/product/chatgpt"}), 200)
	var visitors int
	owner.DB.QueryRow(ctx, "SELECT count(*) FROM visitor_days WHERE shop_id='a'").Scan(&visitors)
	if visitors != 1 {
		t.Fatal("visitor attribution lost")
	}
	execSQL(t, owner, "UPDATE partners SET active=false WHERE id='a'")
	if call("GET", "/api/catalog", nil).Code != 404 {
		t.Fatal("disabled partner served")
	}
	execSQL(t, owner, "UPDATE partners SET active=true WHERE id='a'")
	dns.cname = "elsewhere.example.com."
	if e = svc.Check(ctx, "a"); e != nil {
		t.Fatal(e)
	}
	if call("GET", "/api/catalog", nil).Code != 404 {
		t.Fatal("moved DNS still routed")
	}
	dns.cname = "shops.shop.example.invalid."
	if e = svc.Check(ctx, "a"); e != nil {
		t.Fatal(e)
	}
	if e = svc.Remove(ctx, "a", "wrong.example.com"); e == nil {
		t.Fatal("stale removal accepted")
	}
	if e = svc.Remove(ctx, "a", "shop.example.com"); e != nil {
		t.Fatal(e)
	}
	if call("GET", "/api/catalog", nil).Code != 404 {
		t.Fatal("removal did not immediately stop routing")
	}
	p.failDelete = true
	if e = svc.Check(ctx, "a"); e == nil {
		t.Fatal("cleanup should fail")
	}
	view, _ = svc.Read(ctx, "a")
	if view.Domain == nil || view.Domain.State != "deleting" {
		t.Fatal("cleanup tombstone lost")
	}
	p.failDelete = false
	if e = svc.Check(ctx, "a"); e != nil {
		t.Fatal(e)
	}
	view, _ = svc.Read(ctx, "a")
	if view.Domain != nil {
		t.Fatal("domain not removed")
	}
}

func TestCustomDomainRecoverySessionScope(t *testing.T) {
	_, app, a, _, mail := identityFixture(t)
	ctx := context.Background()
	scoped := domains.WithScope(ctx, domains.Scope{Host: "shop.example.com", PartnerID: "a", Slug: "ABC123"})
	id, e := a.Send(scoped, "customer", auth.SendInput{Email: "buyer@example.test"}, "one")
	if e != nil {
		t.Fatal(e)
	}
	input := auth.VerifyInput{Challenge: id, Code: mail.get(id).Code}
	if _, e = a.Verify(ctx, "customer", input, "two"); e == nil {
		t.Fatal("custom challenge verified on primary host")
	}
	token, e := a.Verify(scoped, "customer", input, "three")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = a.Require(ctx, app.DB, "customer", token); e == nil {
		t.Fatal("session replay on primary host")
	}
	other := domains.WithScope(ctx, domains.Scope{Host: "other.example.com"})
	if _, e = a.Require(other, app.DB, "customer", token); e == nil {
		t.Fatal("session replay on another store")
	}
	if _, e = a.Require(scoped, app.DB, "customer", token); e != nil {
		t.Fatal(e)
	}
	if e = a.Logout(ctx, "customer", token); e != nil {
		t.Fatal(e)
	}
	if _, e = a.Require(scoped, app.DB, "customer", token); e != nil {
		t.Fatal("other domain revoked session")
	}
	if e = a.Logout(scoped, "customer", token); e != nil {
		t.Fatal(e)
	}
	if _, e = a.Require(scoped, app.DB, "customer", token); e == nil {
		t.Fatal("logout failed")
	}
}

func TestCustomDomainBindingPermissionsAndPendingCleanup(t *testing.T) {
	owner, app, a, _, mail := identityFixture(t)
	ctx := context.Background()
	execSQL(t, owner, `INSERT INTO partners(id,slug,name,email,active) VALUES('a','ABC123','A','a@example.test',true),('b','DEF456','B','b@example.test',true)`)
	p := &domainProvider{}
	completedDomainOrders(t, owner, "a", 21)
	completedDomainOrders(t, owner, "b", 21)
	svc := &domains.Service{Store: app, Provider: p, Target: "shops.shop.example.invalid", Zone: "shop.example.invalid", Limit: 1}
	h := httpapi.Router(app, "fixture", slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.Options{Origin: testOrigin, Auth: a, Domains: svc})
	object(t, request(h, "POST", "/api/partner/domain", map[string]any{"hostname": "shop.example.com"}, nil), 401)
	partner := login(t, h, mail, "partner", "a@example.test")
	object(t, request(h, "POST", "/api/partner/domain", map[string]any{"hostname": "shop.example.com"}, partner), 200)
	if e := svc.Bind(ctx, "b", "other.example.com"); e == nil {
		t.Fatal("local binding cap bypassed")
	}
	if e := svc.Bind(ctx, "a", "shop.example.com"); e != nil {
		t.Fatal("same binding retry not idempotent")
	}
	object(t, request(h, "POST", "/api/partner/domain/remove", map[string]any{"hostname": "stale.example.com"}, partner), 409)
	// The provider accepted a create but the response/commit was lost: durable intent must clean it up.
	execSQL(t, owner, `UPDATE partner_domains SET provider_id='creating' WHERE partner_id='a'`)
	p.h.ID = "unknown-create-id"
	p.h.Hostname = "shop.example.com"
	object(t, request(h, "POST", "/api/partner/domain/remove", map[string]any{"hostname": "shop.example.com"}, partner), 200)
	if e := svc.Check(ctx, "a"); e != nil {
		t.Fatal(e)
	}
	if p.creates != 0 || p.deletes != 1 {
		t.Fatal("cleanup created another hostname or leaked remote registration")
	}
	if e := svc.Bind(ctx, "b", "shop.example.com"); e != nil {
		t.Fatal("removed domain cannot be rebound", e)
	}
	view := object(t, request(h, "GET", "/api/partner/domain", nil, partner), 200)
	if view["domain"] != nil {
		t.Fatal("partner read another partner's domain")
	}
}
