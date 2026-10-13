package telegram

import (
	"context"
	"gptibo/storefront/internal/mailer"
	"gptibo/storefront/internal/orders"
	"gptibo/storefront/internal/security"
	"time"
)

func (s *Service) Invitations(ctx context.Context) error {
	if s.Client == nil || s.Sender == nil {
		return nil
	}
	var first error
	for range 10 {
		now, lease := time.Now().UnixMilli(), security.ID()
		rows, e := orders.Rows(ctx, s.Store.DB, `UPDATE telegram_partner_applications SET lock_token=$1,lock_until=$2 WHERE id=(SELECT id FROM telegram_partner_applications WHERE (mail_status='pending' OR(mail_status IN('sent','failed') AND notice_sent=0)) AND next_attempt_at<=$3 AND lock_until<=$3 ORDER BY next_attempt_at FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING *`, lease, now+60000, now)
		if e != nil {
			return e
		}
		if len(rows) == 0 {
			break
		}
		row := rows[0]
		id := row["id"].(string)
		e = func() (attemptErr error) {
			defer func() {
				c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				next := int64(0)
				if attemptErr != nil {
					next = time.Now().UnixMilli() + min(int64(3600000), int64(60000)<<min(row["mail_attempts"].(int64), int64(6)))
				}
				_, _ = s.Store.DB.Exec(c, `UPDATE telegram_partner_applications SET lock_token=NULL,lock_until=0,next_attempt_at=GREATEST(next_attempt_at,$3::bigint) WHERE id=$1 AND lock_token=$2`, id, lease, next)
			}()
			var valid bool
			e := s.Store.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM partners WHERE id=$1 AND active AND email=$2)`, row["partner_id"], row["email"]).Scan(&valid)
			if e != nil {
				return e
			}
			if !valid {
				_, e = s.Store.DB.Exec(ctx, `UPDATE telegram_partner_applications SET mail_status='failed',notice_sent=1 WHERE id=$1 AND lock_token=$2`, id, lease)
				return e
			}
			status := row["mail_status"].(string)
			attempt := row["mail_attempts"].(int64)
			if status == "pending" {
				if attempt >= 10 || time.Now().UnixMilli()-row["created_at"].(int64) >= 23*3600000 {
					status = "failed"
					if _, e = s.Store.DB.Exec(ctx, `UPDATE telegram_partner_applications SET mail_status='failed' WHERE id=$1 AND lock_token=$2`, id, lease); e != nil {
						return e
					}
				} else {
					body := ""
					if row["mail_body"] != nil {
						body = row["mail_body"].(string)
					} else {
						m, e := mailer.Template("partner-welcome", s.Origin, nil)
						if e != nil {
							return e
						}
						body, e = s.Sender.Freeze(row["email"].(string), m)
						if e != nil {
							return e
						}
					}
					tag, e := s.Store.DB.Exec(ctx, `UPDATE telegram_partner_applications SET mail_body=$1,mail_attempts=mail_attempts+1 WHERE id=$2 AND lock_token=$3 AND lock_until>$4`, body, id, lease, time.Now().UnixMilli())
					if e != nil {
						return e
					}
					if tag.RowsAffected() != 1 {
						return orders.Conflict()
					}
					// Compatible with the original invitation's Resend request key.
					if _, e = s.Sender.SendFrozen(ctx, body, "partner-welcome/telegram-"+id); e != nil {
						return e
					}
					tag, e = s.Store.DB.Exec(ctx, `UPDATE telegram_partner_applications SET mail_status='sent' WHERE id=$1 AND lock_token=$2 AND lock_until>$3`, id, lease, time.Now().UnixMilli())
					if e != nil {
						return e
					}
					if tag.RowsAffected() != 1 {
						return orders.Conflict()
					}
					status = "sent"
				}
			}
			if row["notice_sent"] == int64(0) {
				if e = s.Client.Call(ctx, "sendMessage", reply(row["user_id"].(int64), s.status(status)), nil); e != nil {
					return e
				}
				_, e = s.Store.DB.Exec(ctx, `UPDATE telegram_partner_applications SET notice_sent=1 WHERE id=$1 AND lock_token=$2 AND lock_until>$3`, id, lease, time.Now().UnixMilli())
				return e
			}
			return nil
		}()
		if e != nil {
			if first == nil {
				first = e
			}
		}
	}
	now := time.Now().UnixMilli()
	_, e := s.Store.DB.Exec(ctx, `DELETE FROM telegram_partner_updates WHERE created_at<$1 AND lock_until<=$2`, now-7*86400000, now)
	if e != nil {
		return e
	}
	_, e = s.Store.DB.Exec(ctx, `UPDATE telegram_partner_sessions SET step='idle',wechat='',qq='',email='' WHERE expires_at<=$1 AND lock_until<=$1 AND step NOT IN('idle','submitted')`, now)
	if e != nil {
		return e
	}
	return first
}
