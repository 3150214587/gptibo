package delivery

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/mailer"
	"gptibo/storefront/internal/orders"
	"gptibo/storefront/internal/security"
)

const reminderEligible = `o.mode='live' AND o.is_test=0 AND o.payment_status='paid' AND o.status='session' AND o.plan<>'codex-sms-once' AND o.customer_email IS NOT NULL AND o.customer_email<>'' AND o.redemption_blocked=0 AND o.refund_hold=0 AND o.refunded_cents=0 AND o.started_at IS NULL AND NOT EXISTS(SELECT 1 FROM recharge_redemptions r WHERE r.order_id=o.id) AND NOT EXISTS(SELECT 1 FROM order_events e WHERE e.order_id=o.id AND e.to_status IN ('processing','success','failed')) AND NOT EXISTS(SELECT 1 FROM recharge_orders t WHERE t.order_id=o.id AND (t.financial_hold=1 OR t.refunded_cents>0))`

func (s Service) ReminderMessage(ctx context.Context, id, plan, partner string, stores ...string) (mailer.Message, error) {
	shop := ""
	if len(stores) > 0 {
		shop = stores[0]
	}
	brand, e := s.brands(s.Store.DB).Store(ctx, partner, shop)
	if e != nil {
		return mailer.Message{}, e
	}
	contact := func(owner string) ([]string, error) {
		var wechat, qq, email string
		e := s.Store.DB.QueryRow(ctx, `SELECT wechat,qq,email FROM support_contacts WHERE owner=$1`, owner).Scan(&wechat, &qq, &email)
		if e != nil && e != pgx.ErrNoRows {
			return nil, e
		}
		lines := []string{}
		if wechat != "" {
			lines = append(lines, "微信："+wechat)
		}
		if qq != "" {
			lines = append(lines, "QQ："+qq)
		}
		if email != "" {
			lines = append(lines, "邮箱："+email)
		}
		return lines, nil
	}
	if partner == "" {
		partner = "platform"
	}
	lines := []string{}
	if partner == "platform" {
		lines, e = contact("platform")
	} else if brand.Contacts != "" {
		lines = strings.Split(brand.Contacts, "\n")
	}
	if e != nil {
		return mailer.Message{}, e
	}

	contacts := strings.Join(lines, "\n")
	if contacts == "" {
		contacts = "请返回订单页面查看客服联系方式，或联系向您提供购买链接的服务商。"
	}
	return mailer.BrandedTemplate("recharge-reminder", brand, map[string]string{"REMINDER_ORDER_ID": id, "REMINDER_PLAN_NAME": plan, "REMINDER_CONTACTS": contacts})
}
func (s Service) Reminders(ctx context.Context, limit int) (map[string]any, error) {
	out := map[string]any{"configured": s.Sender != nil, "sent": 0, "failed": 0}
	if s.Sender == nil {
		return out, nil
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	now := time.Now().UnixMilli()
	_, e := s.Store.DB.Exec(ctx, `UPDATE recharge_reminders SET cancelled_at=$1 WHERE delivered_at IS NULL AND cancelled_at IS NULL AND locked_until<=$1 AND ((first_attempt_at IS NOT NULL AND first_attempt_at<=$2) OR NOT EXISTS(SELECT 1 FROM orders o WHERE o.id=recharge_reminders.order_id AND `+reminderEligible+`))`, now, now-23*3600000)
	if e != nil {
		return out, e
	}
	for range limit {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		lease := security.ID()
		now = time.Now().UnixMilli()
		candidates, e := orders.Rows(ctx, s.Store.DB, `UPDATE recharge_reminders SET lock_token=$1,locked_until=$2 WHERE order_id=(SELECT n.order_id FROM recharge_reminders n JOIN orders o ON o.id=n.order_id WHERE n.delivered_at IS NULL AND n.cancelled_at IS NULL AND n.next_attempt_at<=$3 AND n.locked_until<=$3 AND n.paid_at<=$4 AND `+reminderEligible+` ORDER BY n.next_attempt_at FOR UPDATE OF n SKIP LOCKED LIMIT 1) RETURNING *`, lease, now+60000, now, now-900000)
		if e != nil {
			return out, e
		}
		if len(candidates) == 0 {
			break
		}
		row := candidates[0]
		id := row["order_id"].(string)
		sent, e := s.sendReminder(ctx, row, lease)
		if e != nil {
			out["failed"] = out["failed"].(int) + 1
			backoff := min(int64(3600000), int64(60000)<<min(row["attempt_count"].(int64), int64(6)))
			cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_, _ = s.Store.DB.Exec(cleanup, `UPDATE recharge_reminders SET locked_until=0,next_attempt_at=$1,attempt_count=attempt_count+1 WHERE order_id=$2 AND lock_token=$3`, time.Now().UnixMilli()+backoff, id, lease)
			cancel()
		} else if sent {
			out["sent"] = out["sent"].(int) + 1
		}
	}
	return out, nil
}
func (s Service) sendReminder(ctx context.Context, row map[string]any, lease string) (bool, error) {
	id := row["order_id"].(string)
	list, e := orders.Rows(ctx, s.Store.DB, `SELECT o.customer_email,o.plan_name,COALESCE(o.partner_id,'platform') partner,COALESCE(o.store_id,'') shop FROM orders o WHERE o.id=$1 AND `+reminderEligible, id)
	if e != nil {
		return false, e
	}
	cancel := func() (bool, error) {
		_, e := s.Store.DB.Exec(ctx, `UPDATE recharge_reminders SET cancelled_at=$1,locked_until=0 WHERE order_id=$2 AND lock_token=$3`, time.Now().UnixMilli(), id, lease)
		return false, e
	}
	if len(list) == 0 {
		return cancel()
	}
	o := list[0]
	var message mailer.Message
	if row["message_json"] != nil {
		if e = json.Unmarshal([]byte(row["message_json"].(string)), &message); e != nil {
			return false, e
		}
	} else {
		message, e = s.ReminderMessage(ctx, id, o["plan_name"].(string), o["partner"].(string), o["shop"].(string))
		if e != nil {
			return false, e
		}
	}
	recipient := o["customer_email"].(string)
	if row["recipient"] != nil {
		recipient = row["recipient"].(string)
	}
	body := ""
	if row["message_json"] != nil {
		var frozen mailer.Frozen
		_ = json.Unmarshal([]byte(row["message_json"].(string)), &frozen)
		if frozen.From != "" && len(frozen.To) == 1 {
			body = row["message_json"].(string)
		}
	}
	if body == "" {
		body, e = s.Sender.Freeze(recipient, message)
		if e != nil {
			return false, e
		}
	}
	tag, e := s.Store.DB.Exec(ctx, `UPDATE recharge_reminders SET message_json=$1,recipient=$2,first_attempt_at=COALESCE(first_attempt_at,$3) WHERE order_id=$4 AND lock_token=$5 AND locked_until>$3`, body, recipient, time.Now().UnixMilli(), id, lease)
	if e != nil {
		return false, e
	}
	if tag.RowsAffected() != 1 {
		return false, orders.Conflict()
	}
	var eligible bool
	e = s.Store.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM orders o WHERE o.id=$1 AND `+reminderEligible+`)`, id).Scan(&eligible)
	if e != nil {
		return false, e
	}
	if !eligible {
		return cancel()
	}
	if _, e = s.Sender.SendFrozen(ctx, body, "recharge-reminder/"+id); e != nil {
		return false, e
	}
	tag, e = s.Store.DB.Exec(ctx, `UPDATE recharge_reminders SET delivered_at=$1,locked_until=0,attempt_count=attempt_count+1 WHERE order_id=$2 AND lock_token=$3 AND locked_until>$1`, time.Now().UnixMilli(), id, lease)
	if e != nil {
		return false, e
	}
	return tag.RowsAffected() == 1, nil
}
func (s Service) PreviewMembership(ctx context.Context, plan, key string) (map[string]any, error) {
	if (plan != "duolingo-12m" && plan != "gemini-pro-18m") || !security.ValidID(key) {
		return nil, fault.New(400, "预览套餐或请求编号无效。")
	}
	if s.Sender == nil || s.AdminEmail == "" {
		return nil, fault.New(503, "预览邮件未配置。")
	}
	var instructions string
	e := s.Store.DB.QueryRow(ctx, `SELECT delivery_instructions FROM catalog_plans WHERE id=$1`, plan).Scan(&instructions)
	if e == pgx.ErrNoRows {
		return nil, fault.New(404, "未找到套餐。")
	}
	if e != nil {
		return nil, e
	}
	value := "https://example.com/claim/gemini-preview"
	if plan == "duolingo-12m" {
		value = "https://www.duolingo.com/claim/super?code=PREVIEW-ONLY-NOT-A-REAL-CODE"
	}
	m, e := Message(plan, "YMX-PREVIEW", value, instructions, s.Origin)
	if e != nil {
		return nil, e
	}
	raw, e := s.Sender.Freeze(s.AdminEmail, m)
	if e != nil {
		return nil, e
	}
	id, e := s.Sender.SendFrozen(ctx, raw, "membership-preview/"+key)
	if e != nil {
		return nil, e
	}
	return map[string]any{"ok": true, "to": s.AdminEmail, "providerId": id}, nil
}
func (s Service) PreviewReminder(ctx context.Context, partner string) (map[string]any, error) {
	if s.Sender == nil || s.AdminEmail == "" {
		return nil, fault.New(503, "预览邮件未配置。")
	}
	if partner != "" {
		var exists bool
		e := s.Store.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM partners WHERE id=$1)`, partner).Scan(&exists)
		if e != nil {
			return nil, e
		}
		if !exists {
			return nil, fault.New(404, "未找到代理。")
		}
	}
	m, e := s.ReminderMessage(ctx, "模板预览 · 非真实订单", "ChatGPT Plus 月度充值", partner)
	if e != nil {
		return nil, e
	}
	raw, e := s.Sender.Freeze(s.AdminEmail, m)
	if e != nil {
		return nil, e
	}
	if _, e = s.Sender.SendFrozen(ctx, raw, "recharge-reminder/preview-"+security.ID()); e != nil {
		return nil, e
	}
	return map[string]any{"ok": true, "to": s.AdminEmail}, nil
}
