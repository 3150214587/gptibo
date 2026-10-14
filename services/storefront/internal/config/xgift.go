package config

import (
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
	var e error
	s.Checkout, e = flag("XGIFT_CHECKOUT_ENABLED")
	if e != nil {
		return nil, e
	}
	if os.Getenv("ORDER_TESTING_ENABLED") == "true" {
		s.Checkout = false
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
