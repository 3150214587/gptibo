package config

import (
	"errors"
	"gptibo/storefront/internal/telegram"
	"os"
	"strconv"
	"strings"
)

func LoadTelegram() (*telegram.Client, error) {
	enabled, e := flag("TELEGRAM_ENABLED")
	if e != nil || !enabled {
		return nil, e
	}
	token, e := Secret("TELEGRAM_BOT_TOKEN")
	if e != nil {
		return nil, e
	}
	secret, e := Secret("TELEGRAM_WEBHOOK_SECRET")
	if e != nil {
		return nil, e
	}
	groups := []int64{}
	seen := map[int64]bool{}
	for _, part := range strings.Split(os.Getenv("TELEGRAM_ALLOWED_GROUP_IDS"), ",") {
		g, e := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if e != nil || g >= 0 {
			return nil, errors.New("invalid TELEGRAM_ALLOWED_GROUP_IDS")
		}
		if !seen[g] {
			groups = append(groups, g)
			seen[g] = true
		}
	}
	client, e := telegram.NewClient(token, os.Getenv("TELEGRAM_BOT_USERNAME"), secret, groups)
	if e != nil {
		return nil, e
	}
	client.RegistrationClosed, e = flag("TELEGRAM_REGISTRATION_CLOSED")
	return client, e
}
