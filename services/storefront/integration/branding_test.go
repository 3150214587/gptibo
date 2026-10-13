package integration_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"gptibo/storefront/internal/domains"
	"gptibo/storefront/internal/httpapi"
	"gptibo/storefront/internal/security"
)

func TestPartnerBrandingIsolationAndCatalog(t *testing.T) {
	owner, app, identity, h, mail := identityFixture(t)
	execSQL(t, owner, `INSERT INTO partners(id,slug,name,email,active) VALUES('a','AAAAAA','Account A','a@example.test',true),('b','BBBBBB','Account B','b@example.test',true)`)
	a := login(t, h, mail, "partner", "a@example.test")
	b := login(t, h, mail, "partner", "b@example.test")
	path := "/api/partner/branding"
	initial := object(t, request(h, "GET", path, nil, a), 200)
	if initial["name"] != "" || initial["logo"] != "" {
		t.Fatal(initial)
	}
	img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	img.Set(0, 0, color.NRGBA{R: 255, A: 255})
	var raw bytes.Buffer
	if e := png.Encode(&raw, img); e != nil {
		t.Fatal(e)
	}
	// Uploaded trailing data must not reach the storefront image.
	logo := "data:image/png;base64," + base64.StdEncoding.EncodeToString(append(raw.Bytes(), []byte("private metadata")...))
	object(t, request(h, "POST", path+"/logo", map[string]any{"logo": logo}, nil), 401)
	prepared := object(t, request(h, "POST", path+"/logo", map[string]any{"logo": logo}, a), 200)
	decoded, e := base64.StdEncoding.DecodeString(prepared["data"].(string))
	if e != nil || bytes.Contains(decoded, []byte("private metadata")) {
		t.Fatal("unsafe image retained")
	}
	if prepared["key"] != "store-logos/"+security.Hash("a")+"/"+security.Hash(string(decoded))+".png" {
		t.Fatal("incorrect object identity")
	}
	logo = "/" + prepared["key"].(string)
	saved := object(t, request(h, "POST", path, map[string]any{"name": " 星河商店 ", "logo": logo, "version": 0, "pageTitle": "星河订阅", "headline": "欢迎来到星河", "description": "按需选择你的服务"}, a), 200)
	if saved["name"] != "星河商店" || saved["version"] != float64(1) || saved["logo"] != logo {
		t.Fatal("branding normalization failed")
	}
	object(t, request(h, "POST", path, map[string]any{"name": "stale", "logo": "", "version": 0}, a), 409)
	object(t, request(h, "POST", path, map[string]any{"name": "other", "logo": "", "version": 1, "partner_id": "b"}, a), 400)
	for _, bad := range []string{"https://example.com/logo.png", "data:image/svg+xml;base64,PHN2Zy8+", "data:image/png;base64,AAAA"} {
		object(t, request(h, "POST", path, map[string]any{"name": "bad", "logo": bad, "version": 1}, a), 400)
		object(t, request(h, "POST", path+"/logo", map[string]any{"logo": bad}, a), 400)
	}
	object(t, request(h, "POST", path, map[string]any{"name": "stolen", "logo": logo, "version": 0}, b), 400)
	for i := 0; i < 16; i++ {
		object(t, request(h, "POST", path+"/logo", map[string]any{"logo": "invalid"}, a), 400)
	}
	object(t, request(h, "POST", path+"/logo", map[string]any{"logo": "invalid"}, a), 429)
	object(t, request(h, "POST", path, map[string]any{"name": strings.Repeat("店", 33), "logo": "", "version": 1}, a), 400)
	for _, field := range []string{"pageTitle", "headline", "description"} {
		object(t, request(h, "POST", path, map[string]any{"name": "ok", "logo": "", "version": 1, field: strings.Repeat("字", 201)}, a), 400)
	}
	object(t, request(h, "POST", path, map[string]any{"name": "ok", "logo": "", "version": 1, "headline": "bad\ncopy"}, a), 400)
	object(t, request(h, "GET", path, nil, nil), 401)
	other := object(t, request(h, "GET", path, nil, b), 200)
	if other["name"] != "" || other["logo"] != "" {
		t.Fatal("branding leaked")
	}
	catalog := object(t, request(h, "GET", "/api/catalog?partner=AAAAAA", nil, nil), 200)
	partner := catalog["partner"].(map[string]any)
	if partner["pageTitle"] != "星河订阅" || partner["headline"] != "欢迎来到星河" || partner["description"] != "按需选择你的服务" || partner["brandName"] != "星河商店" || partner["name"] != "Account A" || partner["logo"] != saved["logo"] {
		t.Fatal("public branding incorrect")
	}
	platform := object(t, request(h, "GET", "/api/catalog", nil, nil), 200)
	if platform["partner"] != nil {
		t.Fatal("platform contaminated")
	}
	execSQL(t, owner, `INSERT INTO partner_domains(partner_id,hostname,verification,provider_id,state,checked_at) VALUES('a','example.com','fixture','provider','active',now())`)
	svc := &domains.Service{Store: app, Provider: &domainProvider{}, Target: "shops.shop.example.invalid", Zone: "shop.example.invalid", Limit: 100}
	custom := httpapi.Router(app, "fixture", slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.Options{Origin: testOrigin, Auth: identity, Domains: svc, TrafficProtection: true, TrustedProxies: []string{"127.0.0.1/32"}})
	req := httptest.NewRequest("GET", "/api/catalog?partner=BBBBBB", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("X-Storefront-Host", "example.com")
	w := httptest.NewRecorder()
	custom.ServeHTTP(w, req)
	object(t, w, 404)
	req = httptest.NewRequest("GET", "/api/catalog", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("X-Storefront-Host", "example.com")
	w = httptest.NewRecorder()
	custom.ServeHTTP(w, req)
	hostPartner := object(t, w, 200)["partner"].(map[string]any)
	if hostPartner["brandName"] != "星河商店" || hostPartner["slug"] != "AAAAAA" {
		t.Fatal("domain branding spoofed")
	}
	admin := login(t, h, mail, "admin", "")
	object(t, request(h, "GET", "/api/admin/partners/a/branding", nil, admin), 200)
	object(t, request(h, "GET", "/api/admin/partners/a/branding", nil, b), 401)
	object(t, request(h, "POST", "/api/admin/partners/a/branding", map[string]any{}, admin), 405)
	reset := object(t, request(h, "POST", path, map[string]any{"name": "", "logo": "", "version": 1}, a), 200)
	if reset["pageTitle"] != "" || reset["headline"] != "" || reset["description"] != "" || reset["name"] != "" || reset["logo"] != "" {
		t.Fatal("reset failed")
	}
	var count int
	if e := owner.DB.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE action='partner-branding' AND target='a'`).Scan(&count); e != nil || count != 2 {
		t.Fatal("audit missing", count, e)
	}
}

func TestBrandingMigrationPreservesExistingShop(t *testing.T) {
	owner, app := fixture(t, true)
	ctx := context.Background()
	execSQL(t, owner, `INSERT INTO partners(id,slug,name,active) VALUES('a','AAAAAA','Existing account',true);
 INSERT INTO partner_domains(partner_id,hostname,verification,provider_id,state,checked_at) VALUES('a','example.com','preserve-txt','preserve-provider','active',now());
 DROP TABLE partner_branding; ALTER TABLE recharge_redemptions DROP COLUMN public_message, DROP COLUMN recovery_action; DELETE FROM storefront_migrations WHERE name IN ('022_partner_branding.sql','025_shop_copy_recharge_message.sql')`)
	if e := owner.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if e := owner.GrantRuntime(ctx, app.DB.Config().ConnConfig.User, app.DB.Config().ConnConfig.Password); e != nil {
		t.Fatal(e)
	}
	if e := app.VerifyRuntime(ctx); e != nil {
		t.Fatal(e)
	}
	if e := owner.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	var preserved bool
	if e := app.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM partner_domains WHERE hostname='example.com' AND verification='preserve-txt' AND provider_id='preserve-provider' AND state='active')`).Scan(&preserved); e != nil || !preserved {
		t.Fatal("binding changed", e)
	}
	execSQL(t, app, `INSERT INTO partner_branding(partner_id,name) VALUES('a','My shop')`)
}

func TestShopCopyMigrationPreservesExistingBranding(t *testing.T) {
	owner, app := fixture(t, true)
	ctx := context.Background()
	execSQL(t, owner, `INSERT INTO partners(id,slug,name,active) VALUES('a','AAAAAA','Existing',true);
 INSERT INTO partner_branding(partner_id,name,logo,version) VALUES('a','原来的店铺','existing-logo',7);
 ALTER TABLE partner_branding DROP COLUMN page_title, DROP COLUMN headline, DROP COLUMN description;
 ALTER TABLE recharge_redemptions DROP COLUMN public_message, DROP COLUMN recovery_action;
 DELETE FROM storefront_migrations WHERE name='025_shop_copy_recharge_message.sql'`)
	if e := app.VerifyRuntime(ctx); e == nil {
		t.Fatal("accepted database without new migration")
	}
	if e := owner.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if e := owner.GrantRuntime(ctx, app.DB.Config().ConnConfig.User, app.DB.Config().ConnConfig.Password); e != nil {
		t.Fatal(e)
	}
	if e := app.VerifyRuntime(ctx); e != nil {
		t.Fatal(e)
	}
	var intact bool
	if e := app.DB.QueryRow(ctx, `SELECT name='原来的店铺' AND logo='existing-logo' AND version=7 AND page_title='' AND headline='' AND description='' FROM partner_branding WHERE partner_id='a'`).Scan(&intact); e != nil || !intact {
		t.Fatal("existing branding changed", e)
	}
	execSQL(t, app, `UPDATE partner_branding SET page_title='新标题',headline='新首页标题',description='新简介' WHERE partner_id='a'`)
	if e := owner.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
}
