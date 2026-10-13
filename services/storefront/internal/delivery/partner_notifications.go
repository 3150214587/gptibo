package delivery

import (
	"context"
	"fmt"
	"maps"
	"time"

	"gptibo/storefront/internal/mailer"
	"gptibo/storefront/internal/orders"
	"gptibo/storefront/internal/security"
)

// A preference generation prevents disabling and re-enabling from resurrecting
// pending messages. Recipient changes also invalidate a queued notification.
const partnerNotificationEligible = `EXISTS(SELECT 1 FROM orders o JOIN partners p ON p.id=o.partner_id JOIN partner_notification_settings s ON s.partner_id=p.id WHERE o.id=n.order_id AND p.id=n.partner_id AND p.active AND p.email=n.recipient AND s.order_completed_email AND s.version=n.preference_version AND o.mode='live' AND o.is_test=0 AND o.payment_status='paid' AND o.status='success' AND o.refund_hold=0 AND o.refunded_cents=0 AND NOT EXISTS(SELECT 1 FROM recharge_orders t WHERE t.order_id=o.id AND (t.financial_hold=1 OR t.refunded_cents>0)))`

func PartnerOrderMessage(origin string, row map[string]any, brands ...mailer.Brand) (mailer.Message, error) {
	products := map[string]string{"chatgpt": "ChatGPT", "gemini": "Gemini", "duolingo": "多邻国", "x-premium": "X Premium", "gmail": "Gmail", "claude": "Claude", "grok": "Grok", "codex-sms": "Codex"}
	product := products[row["product_id"].(string)]
	if product == "" {
		product = "商品"
	}
	customer := row["customer_email"].(string)
	if customer == "" {
		customer = "未填写"
	}
	cents := row["price_cents"].(int64)
	brand := mailer.PlatformBrand(origin)
	name := row["partner_name"].(string)
	if len(brands) > 0 {
		brand = brands[0]
	}
	return mailer.BrandedTemplate("partner-order-completed", brand, map[string]string{
		"ORDER_ID": row["order_id"].(string), "SHOP_NAME": name,
		"ORDER_PRODUCT": product + " · " + row["plan_name"].(string), "ORDER_AMOUNT": fmt.Sprintf("¥%d.%02d", cents/100, cents%100),
		"ORDER_CUSTOMER": customer, "ORDER_COMPLETED": time.UnixMilli(row["completed_at"].(int64)).In(time.FixedZone("CST", 8*3600)).Format("2006/01/02 15:04:05"),
	})
}

func (s Service) PartnerOrderNotifications(ctx context.Context, limit int) (map[string]any, error) {
	out := map[string]any{"configured": s.Sender != nil, "sent": 0, "failed": 0}
	if s.Sender == nil {
		return out, nil
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	now := time.Now().UnixMilli()
	// Resend's idempotency window is 24 hours; stop retries before it expires.
	_, e := s.Store.DB.Exec(ctx, `UPDATE partner_order_notifications n SET cancelled_at=$1 WHERE delivered_at IS NULL AND cancelled_at IS NULL AND locked_until<=$1 AND ((first_attempt_at IS NOT NULL AND first_attempt_at<=$2) OR NOT (`+partnerNotificationEligible+`))`, now, now-23*3600000)
	if e != nil {
		return out, e
	}
	for range limit {
		if e = ctx.Err(); e != nil {
			return out, e
		}
		now = time.Now().UnixMilli()
		lease := security.ID()
		rows, e := orders.Rows(ctx, s.Store.DB, `UPDATE partner_order_notifications SET lock_token=$1,locked_until=$2 WHERE order_id=(SELECT n.order_id FROM partner_order_notifications n WHERE n.delivered_at IS NULL AND n.cancelled_at IS NULL AND n.next_attempt_at<=$3 AND n.locked_until<=$3 AND (n.first_attempt_at IS NULL OR n.first_attempt_at>$4) AND `+partnerNotificationEligible+` ORDER BY n.next_attempt_at,n.completed_at,n.order_id FOR UPDATE OF n SKIP LOCKED LIMIT 1) RETURNING *`, lease, now+60000, now, now-23*3600000)
		if e != nil {
			return out, e
		}
		if len(rows) == 0 {
			break
		}
		row := rows[0]
		sent, e := s.sendPartnerOrderNotification(ctx, row, lease)
		if e != nil {
			out["failed"] = out["failed"].(int) + 1
			backoff := min(int64(3600000), int64(60000)<<min(row["attempt_count"].(int64), int64(6)))
			cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_, _ = s.Store.DB.Exec(cleanup, `UPDATE partner_order_notifications SET locked_until=0,next_attempt_at=$1,attempt_count=attempt_count+1 WHERE order_id=$2 AND lock_token=$3`, time.Now().UnixMilli()+backoff, row["order_id"], lease)
			cancel()
		} else if sent {
			out["sent"] = out["sent"].(int) + 1
		}
	}
	return out, nil
}

func (s Service) sendPartnerOrderNotification(ctx context.Context, row map[string]any, lease string) (bool, error) {
	id := row["order_id"].(string)
	body := ""
	if row["message_json"] != nil {
		body = row["message_json"].(string)
	}
	if body == "" {
		brands := s.brands(s.Store.DB)
		shop, e := brands.Order(ctx, id)
		if e != nil {
			return false, e
		}
		issuer, e := brands.Upstream(ctx, row["partner_id"].(string))
		if e != nil {
			return false, e
		}
		details := maps.Clone(row)
		details["partner_name"] = shop.Name
		message, e := PartnerOrderMessage(s.Origin, details, issuer)
		if e != nil {
			return false, e
		}
		body, e = s.Sender.Freeze(row["recipient"].(string), message)
		if e != nil {
			return false, e
		}
	}
	now := time.Now().UnixMilli()
	tag, e := s.Store.DB.Exec(ctx, `UPDATE partner_order_notifications n SET message_json=$1,first_attempt_at=COALESCE(first_attempt_at,$2) WHERE order_id=$3 AND lock_token=$4 AND locked_until>$2 AND cancelled_at IS NULL AND delivered_at IS NULL AND (first_attempt_at IS NULL OR first_attempt_at>$5) AND `+partnerNotificationEligible, body, now, id, lease, now-23*3600000)
	if e != nil {
		return false, e
	}
	if tag.RowsAffected() != 1 {
		_, e = s.Store.DB.Exec(ctx, `UPDATE partner_order_notifications SET cancelled_at=$1,locked_until=0 WHERE order_id=$2 AND lock_token=$3 AND delivered_at IS NULL`, now, id, lease)
		return false, e
	}
	if _, e = s.Sender.SendFrozen(ctx, body, "partner-order-completed/"+id); e != nil {
		return false, e
	}
	tag, e = s.Store.DB.Exec(ctx, `UPDATE partner_order_notifications SET delivered_at=$1,locked_until=0,attempt_count=attempt_count+1 WHERE order_id=$2 AND lock_token=$3 AND locked_until>$1`, time.Now().UnixMilli(), id, lease)
	return tag.RowsAffected() == 1, e
}
