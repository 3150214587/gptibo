package config

import (
	"gptibo/storefront/internal/notifications"
	"os"
)

func LoadNotifications(origin string) (*notifications.Feishu, error) {
	enabled, e := flag("FEISHU_PAYOUT_ENABLED")
	if e != nil || !enabled {
		return nil, e
	}
	secret, e := Secret("FEISHU_APP_SECRET")
	if e != nil {
		return nil, e
	}
	recipient := os.Getenv("FEISHU_PAYOUT_RECIPIENT")
	if recipient == "" {
		recipient = os.Getenv("FEISHU_STOCK_RECIPIENT")
	}
	return notifications.NewFeishu(os.Getenv("FEISHU_APP_ID"), secret, recipient, origin)
}
