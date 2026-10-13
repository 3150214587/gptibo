package management

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"gptibo/storefront/internal/auth"
	"gptibo/storefront/internal/fault"
)

func (s Service) NotificationSettings(ctx context.Context, id string) (map[string]any, error) {
	var enabled bool
	var recipient string
	var version int64
	e := s.Store.DB.QueryRow(ctx, `SELECT COALESCE(n.order_completed_email,false),p.email,COALESCE(n.version,0) FROM partners p LEFT JOIN partner_notification_settings n ON n.partner_id=p.id WHERE p.id=$1`, id).Scan(&enabled, &recipient, &version)
	if e == pgx.ErrNoRows {
		return nil, fault.New(404, "代理不存在。")
	}
	if e != nil {
		return nil, e
	}
	return map[string]any{"orderCompletedEmail": enabled, "recipient": recipient, "version": version}, nil
}

type NotificationSettingsInput struct {
	OrderCompletedEmail *bool  `json:"orderCompletedEmail"`
	Version             *int64 `json:"version"`
}

func (s Service) SaveNotificationSettings(ctx context.Context, token string, in NotificationSettingsInput) error {
	if in.Version == nil || *in.Version < 0 || in.OrderCompletedEmail == nil {
		return fault.New(400, "通知开关或版本无效。")
	}
	return s.write(ctx, "partner", token, func(tx pgx.Tx, p auth.Principal) error {
		if _, e := tx.Exec(ctx, `INSERT INTO partner_notification_settings(partner_id) VALUES($1) ON CONFLICT DO NOTHING`, p.Subject); e != nil {
			return e
		}
		tag, e := tx.Exec(ctx, `UPDATE partner_notification_settings SET order_completed_email=$1,version=version+1,updated_at=$2 WHERE partner_id=$3 AND version=$4`, *in.OrderCompletedEmail, time.Now().UnixMilli(), p.Subject, *in.Version)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return conflict()
		}
		return audit(ctx, tx, p, "partner-notification-settings", p.Subject, map[string]any{"orderCompletedEmail": *in.OrderCompletedEmail, "version": *in.Version + 1})
	})
}
