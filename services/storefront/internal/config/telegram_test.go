package config

import (
	"strings"
	"testing"
)

func TestTelegramRegistrationSwitch(t *testing.T) {
	t.Setenv("MIGRATION_REHEARSAL", "false")
	t.Setenv("TELEGRAM_ENABLED", "true")
	t.Setenv("TELEGRAM_BOT_TOKEN", "999:test-only")
	t.Setenv("TELEGRAM_BOT_TOKEN_FILE", "")
	t.Setenv("TELEGRAM_WEBHOOK_SECRET", strings.Repeat("a", 64))
	t.Setenv("TELEGRAM_WEBHOOK_SECRET_FILE", "")
	t.Setenv("TELEGRAM_BOT_USERNAME", "ExampleBot")
	t.Setenv("TELEGRAM_ALLOWED_GROUP_IDS", "-10042")
	for _, value := range []string{"", "false", "true", "invalid"} {
		t.Setenv("TELEGRAM_REGISTRATION_CLOSED", value)
		c, err := LoadTelegram()
		if value == "invalid" {
			if err == nil {
				t.Fatal("invalid registration flag accepted")
			}
			continue
		}
		if err != nil || c == nil || c.RegistrationClosed != (value == "true") {
			t.Fatal("registration flag not loaded", value, err)
		}
	}
}
