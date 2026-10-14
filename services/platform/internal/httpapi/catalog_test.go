package httpapi

import (
	"context"
	"encoding/json"
	"gptibo/platform/internal/commerce"
	"testing"
)

func TestGPTCatalogIsAdminOnlyAndReadOnly(t *testing.T) {
	s, h := fixture(t)
	calls := 0
	s.GPTPlans = func(context.Context) ([]commerce.UpstreamPlan, error) {
		calls++
		return []commerce.UpstreamPlan{{Code: "pro_50x", Name: "Pro 50x", Enabled: true, Purchasable: true}}, nil
	}
	for _, test := range []struct {
		token  string
		status int
	}{{"", 401}, {"one-session", 403}, {"admin-session", 200}} {
		if got := call(h, "GET", "/api/admin/gpt-plans", test.token, ""); got.Code != test.status {
			t.Fatal(got.Code, got.Body.String())
		}
	}
	if calls != 1 {
		t.Fatal("unauthorized request reached upstream", calls)
	}
	var audits int
	if err := s.Store.DB.QueryRow(context.Background(), "SELECT count(*) FROM audit").Scan(&audits); err != nil || audits != 0 {
		t.Fatal("catalog read wrote audit data", audits, err)
	}
}

func TestProductSaveValidatesLivePlanAndRetainsBindingOnPause(t *testing.T) {
	s, h := fixture(t)
	ctx := context.Background()
	if _, err := s.Store.DB.Exec(ctx, "UPDATE products SET upstream_plan='old-plan' WHERE code='gpt-25x'"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	s.GPTPlans = func(context.Context) ([]commerce.UpstreamPlan, error) {
		calls++
		return []commerce.UpstreamPlan{
			{Code: "pro_50x", Name: "Pro 50x", Enabled: true, Purchasable: true},
			{Code: "disabled", Enabled: false, Purchasable: true},
			{Code: "unavailable", Enabled: true, Purchasable: false},
		}, nil
	}
	for _, plan := range []string{"missing", "disabled", "unavailable", ""} {
		body, _ := json.Marshal(map[string]any{"name": "25x", "supply_minor": 1000, "enabled": true, "upstream_plan": plan})
		got := call(h, "POST", "/api/admin/products/gpt-25x", "admin-session", string(body))
		if got.Code != 409 && !(plan == "" && got.Code == 400) {
			t.Fatal(got.Code, got.Body.String())
		}
	}
	got := call(h, "POST", "/api/admin/products/gpt-25x", "admin-session", `{"name":"25x","supply_minor":1000,"enabled":true,"upstream_plan":"pro_50x"}`)
	if got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	beforePause := calls
	s.GPTPlans = func(context.Context) ([]commerce.UpstreamPlan, error) {
		calls++
		return nil, commerce.Fail("plan_query_failed", "上游暂不可用", 502)
	}
	got = call(h, "POST", "/api/admin/products/gpt-25x", "admin-session", `{"name":"25x","supply_minor":1200,"enabled":false}`)
	if got.Code != 200 || calls != beforePause {
		t.Fatal("pause depends on live upstream", got.Code, calls)
	}
	var plan string
	var enabled bool
	var price int64
	if err := s.Store.DB.QueryRow(ctx, "SELECT upstream_plan,enabled,supply_minor FROM products WHERE code='gpt-25x'").Scan(&plan, &enabled, &price); err != nil || plan != "pro_50x" || enabled || price != 1200 {
		t.Fatal(plan, enabled, price, err)
	}
	got = call(h, "POST", "/api/admin/products/gpt-25x", "admin-session", `{"name":"25x","supply_minor":1200,"enabled":true}`)
	if got.Code != 502 {
		t.Fatal("enabled without confirmed catalog", got.Code)
	}
	if err := s.Store.DB.QueryRow(ctx, "SELECT enabled FROM products WHERE code='gpt-25x'").Scan(&enabled); err != nil || enabled {
		t.Fatal("failed lookup changed product", enabled, err)
	}
}

func TestProductSaveRejectsConcurrentConfigChangeDuringPlanLookup(t *testing.T) {
	s, h := fixture(t)
	s.GPTPlans = func(ctx context.Context) ([]commerce.UpstreamPlan, error) {
		_, err := s.Store.DB.Exec(ctx, "UPDATE products SET upstream_plan='different',version=version+1 WHERE code='gpt-plus'")
		if err != nil {
			t.Fatal(err)
		}
		return []commerce.UpstreamPlan{{Code: "plus", Enabled: true, Purchasable: true}}, nil
	}
	got := call(h, "POST", "/api/admin/products/gpt-plus", "admin-session", `{"name":"Plus","supply_minor":1000,"enabled":true,"upstream_plan":"plus"}`)
	if got.Code != 409 {
		t.Fatal("concurrent mapping overwritten", got.Code, got.Body.String())
	}
}
