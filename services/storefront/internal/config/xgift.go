package config

import (
	"encoding/json"
	"errors"
	"gptibo/storefront/internal/xgift"
	"os"
)

func LoadXGift() (*xgift.Service, error) {
	s := &xgift.Service{Clients: map[string]*xgift.Client{}, Backend: os.Getenv("XGIFT_BACKEND")}
	if s.Backend == "" {
		s.Backend = "legacy"
	}
	if s.Backend != "legacy" && s.Backend != "platform" {
		return nil, errors.New("invalid XGIFT_BACKEND")
	}
	if raw := os.Getenv("XGIFT_MAX_SUPPLY_MINOR"); raw != "" {
		if json.Unmarshal([]byte(raw), &s.SupplyLimits) != nil || s.SupplyLimits == nil {
			return nil, errors.New("invalid XGIFT_MAX_SUPPLY_MINOR")
		}
		for plan, limit := range s.SupplyLimits {
			if !xgift.IsPlan(plan) || limit <= 0 || limit > 1000000000 {
				return nil, errors.New("invalid XGIFT_MAX_SUPPLY_MINOR")
			}
		}
	}
	var e error
	s.Checkout, e = flag("XGIFT_CHECKOUT_ENABLED")
	if e != nil {
		return nil, e
	}
	if os.Getenv("ORDER_TESTING_ENABLED") == "true" {
		s.Checkout = false
	}
	if s.Checkout && s.Backend == "platform" && len(s.SupplyLimits) == 0 {
		return nil, errors.New("platform checkout requires XGIFT_MAX_SUPPLY_MINOR")
	}
	for _, backend := range []string{"legacy", "platform"} {
		prefix := "XGIFT_"
		if backend == "platform" {
			prefix = "XGIFT_PLATFORM_"
		}
		enabled, e := flag(prefix + "ENABLED")
		if e != nil {
			return nil, e
		}
		if !enabled {
			continue
		}
		secret, e := Secret(prefix + "CLIENT_SECRET")
		if e != nil {
			return nil, e
		}
		c, e := xgift.NewClient(os.Getenv(prefix+"API_BASE"), backend, os.Getenv("XGIFT_USER_ID"), os.Getenv(prefix+"KEY_ID"), secret)
		if e != nil {
			return nil, e
		}
		s.Clients[backend] = c
	}
	return s, nil
}
