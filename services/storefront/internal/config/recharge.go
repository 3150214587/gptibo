package config

import (
	"encoding/json"
	"errors"
	"gptibo/storefront/internal/recharge"
	"net/url"
	"os"
	"regexp"
	"slices"
)

func LoadRecharge() (*recharge.Service, error) {
	enabled, e := flag("RECHARGE_ENABLED")
	if e != nil || !enabled {
		return nil, e
	}
	key, e := Secret("RECHARGE_CLIENT_SECRET")
	if e != nil {
		return nil, e
	}
	client, e := recharge.NewClient(os.Getenv("RECHARGE_API_BASE"), os.Getenv("RECHARGE_PARTNER_ID"), os.Getenv("RECHARGE_KEY_ID"), key)
	if e != nil {
		return nil, e
	}
	s := &recharge.Service{Client: client, Products: map[string]string{}}
	precheck, e := flag("SESSION_PRECHECK_ENABLED")
	if e != nil {
		return nil, e
	}
	if precheck {
		secret, err := Secret("SESSION_PRECHECK_SECRET")
		if err != nil {
			return nil, err
		}
		s.Checker, err = recharge.NewSessionChecker(os.Getenv("SESSION_PRECHECK_URL"), secret)
		if err != nil {
			return nil, err
		}
	}
	s.Checkout, e = flag("RECHARGE_CHECKOUT_ENABLED")
	if e != nil {
		return nil, e
	}
	if os.Getenv("ORDER_TESTING_ENABLED") == "true" {
		s.Checkout = false
	}
	raw := os.Getenv("RECHARGE_PRODUCT_MAP")
	if raw != "" {
		if json.Unmarshal([]byte(raw), &s.Products) != nil || s.Products == nil {
			return nil, errors.New("invalid RECHARGE_PRODUCT_MAP")
		}
	}
	for plan, code := range s.Products {
		if !slices.Contains([]string{"plus", "pro-5x", "pro-20x", "pro-25x"}, plan) || !regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`).MatchString(code) {
			return nil, errors.New("invalid RECHARGE_PRODUCT_MAP")
		}
	}
	s.WebhookSecret, e = optionalSecret("RECHARGE_WEBHOOK_SECRET")
	if e != nil {
		return nil, e
	}
	s.Notify = os.Getenv("RECHARGE_NOTIFY_URL")
	if s.Notify != "" {
		u, e := url.Parse(s.Notify)
		if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/api/recharge/webhook" {
			return nil, errors.New("invalid RECHARGE_NOTIFY_URL")
		}
	}
	if s.Checkout && (s.Notify == "" || s.WebhookSecret == "" || len(s.Products) == 0 || os.Getenv("RECHARGE_COLLECTION_MODE") != "" && os.Getenv("RECHARGE_COLLECTION_MODE") != "platform_collect") {
		return nil, errors.New("Recharge checkout requires products, webhook secret and platform_collect notification URL")
	}
	return s, nil
}
