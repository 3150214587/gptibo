package config

import (
	"errors"
	"gptibo/storefront/internal/domains"
	"os"
	"regexp"
	"strconv"
	"strings"
)

func LoadDomains() (*domains.Service, error) {
	if os.Getenv("CUSTOM_DOMAINS_ENABLED") != "true" {
		return nil, nil
	}
	if os.Getenv("MIGRATION_REHEARSAL") == "true" {
		return nil, errors.New("custom domains cannot run in rehearsal")
	}
	zone := strings.ToLower(strings.TrimSpace(os.Getenv("CUSTOM_DOMAINS_ZONE")))
	target := strings.ToLower(strings.TrimSpace(os.Getenv("CUSTOM_DOMAINS_TARGET")))
	normalized, e := domains.Normalize(target, "invalid.invalid")
	if e != nil || normalized != target || zone == "" || !strings.HasSuffix(target, "."+zone) {
		return nil, errors.New("invalid custom domain zone/target")
	}
	zoneID := os.Getenv("CF_SAAS_ZONE_ID")
	if !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(zoneID) {
		return nil, errors.New("invalid CF_SAAS_ZONE_ID")
	}
	token, e := Secret("CF_SAAS_API_TOKEN")
	if e != nil {
		return nil, e
	}
	limit := 100
	if raw := os.Getenv("CUSTOM_DOMAINS_LIMIT"); raw != "" {
		limit, e = strconv.Atoi(raw)
		if e != nil || limit < 1 || limit > 100 {
			return nil, errors.New("CUSTOM_DOMAINS_LIMIT must be 1..100")
		}
	}
	var monthlyFee int64
	if raw := os.Getenv("CUSTOM_DOMAINS_MONTHLY_FEE_CENTS"); raw != "" {
		monthlyFee, e = strconv.ParseInt(raw, 10, 64)
		if e != nil || monthlyFee < 0 || monthlyFee > 10000000 {
			return nil, errors.New("CUSTOM_DOMAINS_MONTHLY_FEE_CENTS must be 0..10000000")
		}
	}
	overrides := make(map[string]bool)
	if raw := strings.TrimSpace(os.Getenv("CUSTOM_DOMAINS_ELIGIBILITY_ALLOWLIST")); raw != "" {
		for _, entry := range strings.Split(raw, ",") {
			id := strings.TrimSpace(entry)
			if !regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`).MatchString(id) {
				return nil, errors.New("CUSTOM_DOMAINS_ELIGIBILITY_ALLOWLIST must contain comma-separated partner UUIDs")
			}
			overrides[id] = true
		}
	}
	return &domains.Service{MonthlyFeeCents: monthlyFee, Target: target, Zone: zone, Limit: limit, EligibilityOverrides: overrides, Provider: &domains.Cloudflare{Zone: zoneID, Token: token, Limit: limit}}, nil
}
