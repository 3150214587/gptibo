package providers

import (
	"context"
	"encoding/json"
	"gptibo/platform/internal/commerce"
	"net/http"
	"strings"
	"testing"
)

func TestGPTCatalogUsesRegistryOrderKeyAndSanitizesData(t *testing.T) {
	var data Object
	err := json.Unmarshal([]byte(`{"version":8,"api_key":"never-expose","registry":[
	 {"product":"gpt","key":"public-plus","acc_plan_key":"internal-plus","label":"Plus","purchasable":true,"secret":"never-expose"},
	 {"product":"gpt","key":"pro_20x","label":"Pro 20x","purchasable":true},
	 {"product":"gpt","key":"pro_50x","name":"Pro 50x","purchasable":false},
	 {"product":"gpt","key":"credit","is_credit":true,"purchasable":true},
	 {"product":"gpt","key":"cdk","flow":"cdk","purchasable":true},
	 {"product":"other","key":"other","purchasable":true}],
	 "plans":{"internal-plus":{"enabled":true},"pro_20x":{"enabled":false},"pro_50x":{"enabled":true}}}`), &data)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := normalizeGPTPlans(data)
	if err != nil || len(rows) != 3 {
		t.Fatal(rows, err)
	}
	if rows[0].Code != "public-plus" || rows[0].Name != "Plus" || !rows[0].Enabled || !rows[0].Purchasable || rows[1].Enabled || rows[2].Purchasable {
		t.Fatal("registry key, name or saleability changed", rows)
	}
	if rows[1].Code != "pro_20x" || rows[2].Code != "pro_50x" {
		t.Fatal("supplier multipliers must not be silently renamed", rows)
	}
	safe, _ := json.Marshal(rows)
	if strings.Contains(string(safe), "never-expose") || strings.Contains(string(safe), "internal-plus") {
		t.Fatal("raw upstream data escaped the catalog boundary")
	}
}

func TestGPTCatalogRejectsMissingAndAmbiguousRegistry(t *testing.T) {
	for _, raw := range []string{
		`{}`, `{"registry":[],"plans":null}`,
		`{"registry":[{"product":"gpt","key":""}],"plans":{}}`,
		`{"registry":[{"product":"gpt","key":"same"},{"product":"gpt","key":"same"}],"plans":{}}`,
	} {
		var data Object
		_ = json.Unmarshal([]byte(raw), &data)
		if _, err := normalizeGPTPlans(data); err == nil {
			t.Fatal("malformed catalog accepted", raw)
		}
	}
}

func TestGPTCatalogOnlyReadsOfficialPlanEndpoint(t *testing.T) {
	old := client
	defer func() { client = old }()
	calls := 0
	client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "GET" || r.URL.String() != "https://cards.example.invalid/openapi/v1/gpt-direct/plans?product=gpt" || r.Header.Get("X-API-Key") != "sk_synthetic" {
			t.Fatal("unexpected catalog request")
		}
		return response(`{"code":0,"data":{"registry":[{"product":"gpt","key":"plus","purchasable":true}],"plans":{"plus":{"enabled":true}}}}`), nil
	})}
	data, rows, err := readGPTPlans(context.Background(), commerce.ProviderConfig{APIBase: "https://cards.example.invalid/openapi/v1", Environment: "production", APIKey: "sk_synthetic"})
	if err != nil || len(rows) != 1 || calls != 1 || data == nil {
		t.Fatal(rows, calls, err)
	}
	if _, err = plans(context.Background(), commerce.Snapshot{Plan: "plus", Provider: commerce.ProviderConfig{APIBase: "https://cards.example.invalid/openapi/v1", APIKey: "sk_synthetic"}}); err != nil {
		t.Fatal("execution catalog differs from admin catalog", err)
	}
}
