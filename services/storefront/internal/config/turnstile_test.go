package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTurnstileConfiguration(t *testing.T) {
	t.Setenv("TURNSTILE_ENABLED", "true")
	t.Setenv("TURNSTILE_CONFIG_FILE", "")
	if _, err := LoadHTTP(); err == nil {
		t.Fatal("enabled without config")
	}
	p := filepath.Join(t.TempDir(), "widgets.json")
	t.Setenv("TURNSTILE_CONFIG_FILE", p)
	if _, err := LoadHTTP(); err == nil {
		t.Fatal("missing config accepted")
	}
	os.WriteFile(p, []byte(`[{"sitekey":"public","secret":"private","hosts":["store.test"]}]`), 0600)
	t.Setenv("PUBLIC_ORIGIN", "https://other.test")
	if _, err := LoadHTTP(); err == nil {
		t.Fatal("unconfigured origin accepted")
	}
	t.Setenv("PUBLIC_ORIGIN", "https://store.test")
	h, err := LoadHTTP()
	if err != nil || h.Turnstile == nil {
		t.Fatal("valid config rejected", err)
	}
	t.Setenv("TURNSTILE_ENABLED", "typo")
	if _, err := LoadHTTP(); err == nil {
		t.Fatal("invalid enable flag accepted")
	}
}
