package refunds

import (
	"context"
	"fmt"
	"time"

	"gptibo/storefront/internal/orders"
	"gptibo/storefront/internal/security"
)

type externalVerifier interface {
	VerifyExternalRefund(context.Context, string) error
}

// ReconcileExternal records refunds made outside the local application. Every
// candidate is re-read from the original supplier before accounting is changed.
// This path has no call to RefundOriginal and never initiates a money movement.
func (s Service) ReconcileExternal(ctx context.Context) error {
	verifier, ok := s.Supplier.(externalVerifier)
	if !ok {
		return nil
	}
	rows, err := orders.Rows(ctx, s.Store.DB, `SELECT o.id FROM orders o JOIN recharge_orders t ON t.order_id=o.id
 WHERE o.mode='live' AND o.is_test=0 AND o.payment_status='paid' AND o.refund_hold=0
 AND t.remote_id IS NOT NULL AND t.collection_mode='platform_collect' AND t.payment_status IN ('refunded','partially_refunded')
 AND t.refunded_cents>o.refunded_cents AND t.refunded_cents<=o.price_cents AND t.lease_until<=$1
 AND NOT EXISTS(SELECT 1 FROM payments WHERE order_id=o.id)
 AND NOT EXISTS(SELECT 1 FROM refund_cases WHERE order_id=o.id AND status IN ('pending','approved'))
 AND NOT EXISTS(SELECT 1 FROM partner_earning_orders e JOIN partner_payouts p ON p.id=e.payout_id WHERE e.id=o.id AND p.status='pending')
 ORDER BY t.updated_at,o.id LIMIT 50`, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	var first error
	for _, row := range rows {
		id := row["id"].(string)
		started := time.Now().UnixMilli()
		err = verifier.VerifyExternalRefund(ctx, id)
		if err == nil {
			err = s.recordExternal(ctx, id, started)
		}
		if err != nil {
			if first == nil {
				first = err
			}
			// Rotate failed candidates rather than letting a bad supplier response block
			// later orders. Pending webhook versions remain independently actionable.
			_, _ = s.Store.DB.Exec(ctx, `UPDATE recharge_orders SET updated_at=$1 WHERE order_id=$2 AND lease_until<=$1`, time.Now().UnixMilli(), id)
		}
	}
	return first
}

func (s Service) recordExternal(ctx context.Context, id string, verifiedSince int64) error {
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lock(ctx, tx); err != nil {
		return err
	}
	o, err := orders.Get(ctx, tx, id, nil, true)
	if err != nil {
		return err
	}
	var remote, status, collection string
	var total, verified, lease int64
	err = tx.QueryRow(ctx, `SELECT COALESCE(remote_id,''),payment_status,collection_mode,refunded_cents,COALESCE(verified_at,0),lease_until FROM recharge_orders WHERE order_id=$1 FOR UPDATE`, id).Scan(&remote, &status, &collection, &total, &verified, &lease)
	if err != nil {
		return err
	}
	if total == o.Refunded {
		return tx.Commit(ctx)
	}
	if o.Mode != "live" || o.Test != 0 || o.Payment != "paid" || o.Hold != 0 || remote == "" || collection != "platform_collect" || verified < verifiedSince || lease > time.Now().UnixMilli() || total <= o.Refunded || total > o.Price || (status != "refunded" && status != "partially_refunded") || (status == "refunded" && total != o.Price) || (status == "partially_refunded" && total >= o.Price) {
		return orders.Conflict()
	}
	var busy bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM refund_cases WHERE order_id=$1 AND status IN ('pending','approved')) OR EXISTS(SELECT 1 FROM payments WHERE order_id=$1)`, id).Scan(&busy)
	if err != nil {
		return err
	}
	if busy {
		return orders.Conflict()
	}
	amount := total - o.Refunded
	now := time.Now().UnixMilli()
	note := "平台已确认退款，已自动核对并同步本站记录。"
	c := Case{ID: security.ID(), OrderID: id, Status: "approved", Requested: amount, Approved: &amount}
	key := fmt.Sprintf("external-refund:%s:%d", remote, total)
	_, err = tx.Exec(ctx, `INSERT INTO refund_cases(id,order_id,request_key,reason,status,requested_cents,approved_cents,created_at,updated_at,last_event_id) VALUES($1,$2,$3,$4,'approved',$5,$5,$6,$6,$7)`, c.ID, id, key, note, amount, now, security.ID())
	if err != nil {
		return err
	}
	reference := fmt.Sprintf("recharge-order:%s:%d.%02d", remote, total/100, total%100)
	if err = settle(ctx, tx, c, o, note, reference, "system:external-refund", true, true); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
