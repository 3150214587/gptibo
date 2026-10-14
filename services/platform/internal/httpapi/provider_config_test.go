package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProviderAddressChangeCannotForwardSavedCredentials(t *testing.T) {
	_, h := fixture(t)
	input := map[string]any{"environment": "sandbox", "api_base": "https://cards.example.invalid/openapi/v1", "api_key": "sk_synthetic", "app_id": "fixture", "writes_enabled": false}
	post := func() int {
		b, _ := json.Marshal(input)
		return call(h, "POST", "/api/admin/provider", "admin-session", string(b)).Code
	}
	if post() != 200 {
		t.Fatal("initial provider save failed")
	}
	input["api_key"] = ""
	if post() != 200 {
		t.Fatal("same connection could not retain key")
	}
	input["api_base"] = "https://other.example.invalid/openapi/v1"
	if post() != 400 {
		t.Fatal("saved key forwarded to different host")
	}
	r := call(h, "GET", "/api/admin/provider", "admin-session", "")
	if r.Code != 200 || strings.Contains(r.Body.String(), "sk_synthetic") {
		t.Fatal("provider response leaked credential")
	}
	if got := call(h, "POST", "/api/admin/products/x-3m", "admin-session", `{"name":"X","supply_minor":100,"enabled":true,"config":{}}`); got.Code != 409 {
		t.Fatal("unconfigured product enabled", got.Code)
	}
}
