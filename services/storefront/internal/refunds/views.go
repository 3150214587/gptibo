package refunds

import (
	"context"
	"github.com/jackc/pgx/v5"
	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/orders"
	"gptibo/storefront/internal/payments"
)

func (s Service) CustomerRead(ctx context.Context, id string, access *orders.Access) (map[string]any, error) {
	tx, e := s.Store.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	o, e := orders.Get(ctx, tx, id, access, false)
	if e != nil {
		return nil, e
	}
	refunded := o.Refunded
	if o.Mode == "live" && o.Test == 0 && o.Payment == "paid" {
		// A verified external refund is already returned money, even before the
		// accounting worker adds its local history. Do not offer it for refund again.
		e = tx.QueryRow(ctx, `SELECT greatest($2::bigint,COALESCE((SELECT refunded_cents FROM recharge_orders WHERE order_id=$1 AND remote_id IS NOT NULL AND verified_at>0 AND ((payment_status='refunded' AND refunded_cents=$3) OR (payment_status='partially_refunded' AND refunded_cents>0 AND refunded_cents<$3))),0))`, id, o.Refunded, o.Price).Scan(&refunded)
		if e != nil {
			return nil, e
		}
	}
	cases, e := orders.Rows(ctx, tx, `SELECT c.id,c.status,c.requested_cents,c.approved_cents,c.version,c.reason,c.note,c.created_at,c.updated_at,r.fee_cents,t.status AS platform_status FROM refund_cases c LEFT JOIN payment_refund_attempts r ON r.id=c.id LEFT JOIN recharge_refunds t ON t.case_id=c.id WHERE c.order_id=$1 ORDER BY c.created_at DESC`, id)
	if e != nil {
		return nil, e
	}
	events, e := orders.Rows(ctx, tx, `SELECT e.case_id,e.status,e.note,e.actor,e.created_at FROM refund_events e JOIN refund_cases r ON r.id=e.case_id WHERE r.order_id=$1 ORDER BY e.created_at,e.id`, id)
	if e != nil {
		return nil, e
	}
	var fee int64
	if e = tx.QueryRow(ctx, `SELECT COALESCE((SELECT fee_cents FROM payments WHERE order_id=$1),0)`, id).Scan(&fee); e != nil {
		return nil, e
	}
	remainingFee := int64(0)
	if o.Refunded < o.Price {
		remainingFee, e = payments.RefundFee(o.Price, fee, o.Refunded, o.Price-o.Refunded)
		if e != nil {
			return nil, e
		}
	}
	for _, c := range cases {
		if c["fee_cents"] == nil {
			c["fee_cents"] = int64(0)
			if (c["status"] == "approved" || c["status"] == "pending") && o.Refunded < o.Price {
				amount := c["requested_cents"].(int64)
				if c["approved_cents"] != nil {
					amount = c["approved_cents"].(int64)
				}
				f, err := payments.RefundFee(o.Price, fee, o.Refunded, amount)
				if err == nil {
					c["fee_cents"] = f
				}
			}
		}
	}
	message := ""
	supported, e := orders.CurrentWarranty(ctx, tx, o.Plan, false)
	if e != nil {
		return nil, e
	}
	if !supported {
		message = WarrantyUnavailableMessage
	}
	platform, e := automaticPlatformRefund(ctx, tx, o)
	if e != nil {
		return nil, e
	}
	return map[string]any{"automaticPlatformRefund": platform, "supportsWarranty": supported, "warrantyMessage": message, "refundableFeeCents": remainingFee, "cases": cases, "events": events, "orderVersion": o.Version, "refundableCents": o.Price - refunded}, tx.Commit(ctx)
}

// Refund cases are retained after processing. Counting customer submissions keeps the
// notification version stable on retries and status changes, even when a new
// request arrives while an older one leaves the pending queue.
func (s Service) AdminNotifications(ctx context.Context) (map[string]any, error) {
	var version, pending int64
	err := s.Store.DB.QueryRow(ctx, `SELECT count(*) FILTER (WHERE request_key NOT LIKE 'external-refund:%'),count(*) FILTER (WHERE status IN ('pending','approved')) FROM refund_cases`).Scan(&version, &pending)
	return map[string]any{"version": version, "pending": pending}, err
}

func (s Service) AdminList(ctx context.Context, page int, status string) (map[string]any, error) {
	if page < 1 || page > 10000 || (status != "" && status != "pending" && status != "approved" && status != "refunded" && status != "rejected" && status != "cancelled") {
		return nil, fault.New(400, "筛选条件无效。")
	}
	tx, e := s.Store.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	list, e := orders.Rows(ctx, tx, `SELECT r.id,r.order_id,r.reason,r.status,r.requested_cents,r.approved_cents,r.note,r.reference,r.version,r.created_at,r.updated_at,o.plan_name,o.mode,o.customer_email,o.partner_name FROM refund_cases r JOIN orders o ON o.id=r.order_id WHERE ($1='' OR r.status=$1) ORDER BY CASE WHEN r.status IN ('pending','approved') THEN 0 ELSE 1 END,r.created_at DESC,r.id DESC LIMIT 20 OFFSET $2`, status, (page-1)*20)
	if e != nil {
		return nil, e
	}
	var total int64
	e = tx.QueryRow(ctx, `SELECT count(*) FROM refund_cases WHERE ($1='' OR status=$1)`, status).Scan(&total)
	if e != nil {
		return nil, e
	}
	return map[string]any{"rows": list, "total": total}, tx.Commit(ctx)
}
func (s Service) AdminDetail(ctx context.Context, id string) (map[string]any, error) {
	tx, e := s.Store.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	c, e := get(ctx, tx, id, false)
	if e != nil {
		return nil, e
	}
	o, e := orders.Get(ctx, tx, c.OrderID, nil, false)
	if e != nil {
		return nil, e
	}
	events, e := orders.Rows(ctx, tx, `SELECT id,case_id,status,note,actor,created_at FROM refund_events WHERE case_id=$1 ORDER BY created_at,id`, id)
	if e != nil {
		return nil, e
	}
	payouts, e := orders.Rows(ctx, tx, `SELECT id,status,reviewed_at FROM partner_payouts WHERE id IN (SELECT payout_id FROM partner_earning_orders WHERE id=$1) ORDER BY (status='pending') DESC,reviewed_at DESC NULLS LAST,id`, o.ID)
	if e != nil {
		return nil, e
	}
	var channel, payout, platform any
	if len(payouts) > 0 {
		payout = payouts[0]
	}
	debits, e := orders.Rows(ctx, tx, `SELECT d.partner_id,agent.name partner_name,agent.email partner_email,d.amount_cents,d.payout_id,p.status payout_status FROM partner_debits d JOIN partners agent ON agent.id=d.partner_id LEFT JOIN partner_payouts p ON p.id=d.payout_id WHERE d.refund_id=$1 ORDER BY d.partner_id`, id)
	if e != nil {
		return nil, e
	}
	var partnerDebit any
	if len(debits) > 0 {
		partnerDebit = debits[0]
	}
	if o.Mode == "live" {
		t, e := orders.Rows(ctx, tx, `SELECT remote_id,payment_status "paymentStatus",refunded_cents "refundedCents",verified_at "checkedAt",financial_hold=1 "financialHold" FROM recharge_orders WHERE order_id=$1`, o.ID)
		if e != nil {
			return nil, e
		}
		if len(t) > 0 {
			channel = "recharge"
			if t[0]["remote_id"] != nil {
				platform = t[0]
			}
		} else {
			p, e := orders.Rows(ctx, tx, `SELECT provider FROM payments WHERE order_id=$1`, o.ID)
			if e != nil {
				return nil, e
			}
			if len(p) > 0 {
				channel = p[0]["provider"]
			}
		}
	}
	paymentRefund, e := orders.Rows(ctx, tx, `SELECT product_cents "productCents",fee_cents "feeCents",product_cents+fee_cents "amountCents",state FROM payment_refund_attempts WHERE id=$1`, c.ID)
	if e != nil {
		return nil, e
	}
	var paymentRefundView any
	if len(paymentRefund) > 0 {
		paymentRefundView = paymentRefund[0]
	}
	payment, e := orders.Rows(ctx, tx, `SELECT fee_cents "feeCents",fee_bps "feeBps",amount_cents "amountCents" FROM payments WHERE order_id=$1`, o.ID)
	if e != nil {
		return nil, e
	}
	var fee int64
	if len(payment) > 0 {
		fee = payment[0]["feeCents"].(int64)
	}
	refundFee := int64(0)
	if c.Approved != nil && c.Status == "approved" {
		refundFee, e = payments.RefundFee(o.Price, fee, o.Refunded, *c.Approved)
		if e != nil {
			return nil, e
		}
	}
	if len(paymentRefund) > 0 {
		refundFee = paymentRefund[0]["feeCents"].(int64)
	}
	upstreamRefund, e := orders.Rows(ctx, tx, `SELECT remote_id,status,updated_at FROM recharge_refunds WHERE case_id=$1`, c.ID)
	if e != nil {
		return nil, e
	}
	redemptions, e := orders.Rows(ctx, tx, `SELECT status,progress_stage,failure_code,COALESCE(progress_updated_at,finished_at,created_at) updated_at FROM recharge_redemptions WHERE order_id=$1 ORDER BY created_at DESC,id DESC LIMIT 1`, o.ID)
	if e != nil {
		return nil, e
	}
	var upstreamView, redemptionView any
	if len(upstreamRefund) > 0 {
		upstreamView = upstreamRefund[0]
	}
	if len(redemptions) > 0 {
		redemptionView = redemptions[0]
	}
	out := map[string]any{"upstreamRefund": upstreamView, "redemption": redemptionView, "paymentRefund": paymentRefundView, "refundFeeCents": refundFee, "refund": c, "paymentProvider": channel, "platformStatus": platform, "order": map[string]any{"id": o.ID, "plan": o.PlanName, "plan_id": o.Plan, "payment_status": o.Payment, "created_at": o.Created, "updated_at": o.Updated, "refund_hold": o.Hold, "purchase_note": o.Note, "price_cents": o.Price, "refunded_cents": o.Refunded, "customer_email": o.Email, "mode": o.Mode, "status": o.Status, "partner_name": o.PartnerName}, "payout": payout, "events": events}
	out["partnerDebit"] = partnerDebit
	out["partnerDebits"] = debits
	out["payouts"] = payouts
	out["hasPartner"] = o.PartnerID != nil
	return out, tx.Commit(ctx)
}
