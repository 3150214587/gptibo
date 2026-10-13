package integration_test

import (
	"context"
	"gptibo/storefront/internal/domains"
	"gptibo/storefront/internal/turnstile"
	"os"
	"path/filepath"
	"testing"
)

func TestDomainActivatesOnlyAfterVerificationRegistration(t *testing.T) {
	owner, app, _, _, _ := identityFixture(t)
	ctx := context.Background()
	execSQL(t, owner, `INSERT INTO partners(id,slug,name,active) VALUES('a','ABC123','A',true);
 INSERT INTO partner_domains(partner_id,hostname,verification,provider_id,state) VALUES('a','shop.example.com','verified','cf-domain','pending_certificate')`)
	completedDomainOrders(t, owner, "a", 3)
	file := filepath.Join(t.TempDir(), "widgets.json")
	if e := os.WriteFile(file, []byte(`[{"sitekey":"key","secret":"secret","hosts":["shop.example.invalid"]}]`), 0600); e != nil {
		t.Fatal(e)
	}
	verification, e := turnstile.NewFile(file)
	if e != nil {
		t.Fatal(e)
	}
	provider := &domainProvider{}
	provider.h.ID = "cf-domain"
	provider.h.Hostname = "shop.example.com"
	provider.h.Status = "active"
	provider.h.SSL.Status = "active"
	svc := &domains.Service{Store: app, Provider: provider, DNS: &domainDNS{txt: "verified", cname: "shops.shop.example.invalid."}, Target: "shops.shop.example.invalid", Zone: "shop.example.invalid", Limit: 100,
		VerificationReady: func(host string) error { _, e := verification.SiteKey(host); return e }}
	if e = svc.Check(ctx, "a"); e != nil {
		t.Fatal(e)
	}
	v, e := svc.Read(ctx, "a")
	if e != nil {
		t.Fatal(e)
	}
	if v.Domain.State != "pending_certificate" {
		t.Fatal("unregistered hostname activated", v.Domain.State)
	}
	if _, e = svc.Resolve(ctx, "shop.example.com"); e == nil {
		t.Fatal("unregistered domain routed")
	}
	if e = os.WriteFile(file, []byte(`[{"sitekey":"key","secret":"secret","hosts":["shop.example.invalid","shop.example.com"]}]`), 0600); e != nil {
		t.Fatal(e)
	}
	if e = svc.Check(ctx, "a"); e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Resolve(ctx, "shop.example.com"); e != nil {
		t.Fatal("registered hostname did not activate without restart", e)
	}
}
