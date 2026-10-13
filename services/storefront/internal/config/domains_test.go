package config

import (
	"strings"
	"testing"
)

func TestCustomDomainConfigurationFailsClosed(t *testing.T) {
	t.Setenv("CUSTOM_DOMAINS_ENABLED", "")
	s, e := LoadDomains()
	if e != nil || s != nil {
		t.Fatal("default should be disabled")
	}
	t.Setenv("CUSTOM_DOMAINS_ENABLED", "true")
	if _, e = LoadDomains(); e == nil {
		t.Fatal("incomplete credentials accepted")
	}
	t.Setenv("MIGRATION_REHEARSAL", "false")
	t.Setenv("CUSTOM_DOMAINS_ZONE", "shop.example.com")
	t.Setenv("CUSTOM_DOMAINS_TARGET", "shops.shop.example.com")
	t.Setenv("CF_SAAS_ZONE_ID", strings.Repeat("a", 32))
	t.Setenv("CF_SAAS_API_TOKEN", "test-token")
	t.Setenv("CF_SAAS_API_TOKEN_FILE", "")
	t.Setenv("CUSTOM_DOMAINS_LIMIT", "100")
	t.Setenv("CUSTOM_DOMAINS_ELIGIBILITY_ALLOWLIST", "")
	t.Setenv("CUSTOM_DOMAINS_MONTHLY_FEE_CENTS", "")
	s, e = LoadDomains()
	if e != nil || !s.Enabled() {
		t.Fatal("valid config rejected", e)
	}
	if len(s.EligibilityOverrides) != 0 {
		t.Fatal("order gate must apply by default")
	}
	if s.MonthlyFeeCents != 0 {
		t.Fatal("default tariff must be free")
	}
	for _, raw := range []string{"0", "500", "2000"} {
		t.Setenv("CUSTOM_DOMAINS_MONTHLY_FEE_CENTS", raw)
		v, err := LoadDomains()
		if err != nil || (raw != "0" && v.MonthlyFeeCents == 0) {
			t.Fatal("valid tariff rejected", raw, err)
		}
	}
	for _, raw := range []string{"-1", "1.5", "bad", "10000001"} {
		t.Setenv("CUSTOM_DOMAINS_MONTHLY_FEE_CENTS", raw)
		if _, err := LoadDomains(); err == nil {
			t.Fatal("invalid tariff accepted", raw)
		}
	}
	t.Setenv("CUSTOM_DOMAINS_MONTHLY_FEE_CENTS", "0")
	const allowedID = "00000000-0000-4000-8000-000000000001"
	t.Setenv("CUSTOM_DOMAINS_ELIGIBILITY_ALLOWLIST", " "+allowedID+", "+allowedID+" ")
	s, e = LoadDomains()
	if e != nil || len(s.EligibilityOverrides) != 1 || !s.EligibilityOverrides[allowedID] || s.Limit != 100 {
		t.Fatal("explicit partner exception not loaded", e)
	}
	for _, invalid := range []string{"*", "someone@example.test", allowedID + ",", "not-a-partner-id"} {
		t.Setenv("CUSTOM_DOMAINS_ELIGIBILITY_ALLOWLIST", invalid)
		if _, e = LoadDomains(); e == nil {
			t.Fatal("invalid or broad exception accepted")
		}
	}
	t.Setenv("CUSTOM_DOMAINS_ELIGIBILITY_ALLOWLIST", "")
	t.Setenv("CUSTOM_DOMAINS_LIMIT", "101")
	if _, e = LoadDomains(); e == nil {
		t.Fatal("paid quota expansion accepted")
	}
	t.Setenv("CUSTOM_DOMAINS_LIMIT", "100")
	t.Setenv("CUSTOM_DOMAINS_TARGET", "shops.foreign.com")
	if _, e = LoadDomains(); e == nil {
		t.Fatal("foreign CNAME target accepted")
	}
	t.Setenv("CUSTOM_DOMAINS_TARGET", "shops.shop.example.com")
	t.Setenv("MIGRATION_REHEARSAL", "true")
	if _, e = LoadDomains(); e == nil {
		t.Fatal("rehearsal may mutate provider")
	}
}
