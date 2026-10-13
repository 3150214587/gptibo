package recharge

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/orders"
	"gptibo/storefront/internal/refunds"
)

// VerifyExternalRefund only reads an existing supplier order. It must never
// create an order, submit a refund, or recover a recharge submission.
func (s *Service) VerifyExternalRefund(ctx context.Context, id string) error {
	return s.locked(ctx, id, func(r Row) error {
		if r.Remote == nil {
			return orders.Conflict()
		}
		var remote RemoteOrder
		if err := s.Client.Request(ctx, "/orders/"+*r.Remote, nil, "", &remote); err != nil {
			return err
		}
		if !slices.Contains([]string{"refunded", "partially_refunded"}, remote.Payment) {
			return orders.Conflict()
		}
		if err := s.apply(ctx, r, remote, false); err != nil {
			return err
		}
		amount, err := Money(remote.Refunded)
		if err != nil {
			return err
		}
		fresh, err := Find(ctx, s.Store.DB, id)
		if err != nil {
			return err
		}
		if fresh.Refunded != amount || fresh.Payment != remote.Payment {
			return orders.Conflict()
		}
		return nil
	})
}

func (s *Service) ReconcileRefund(ctx context.Context, id string, amount int64) (string, error) {
	reference := ""
	e := s.locked(ctx, id, func(r Row) error {
		o, e := orders.Get(ctx, s.Store.DB, id, nil, false)
		if e != nil {
			return e
		}
		if r.Remote == nil || o.Payment != "paid" || o.Hold == 0 {
			return orders.Conflict()
		}
		var remote RemoteOrder
		if e = s.Client.Request(ctx, "/orders/"+*r.Remote, nil, "", &remote); e != nil {
			return e
		}
		if e = s.apply(ctx, r, remote, false); e != nil {
			return e
		}
		fresh, e := Find(ctx, s.Store.DB, id)
		if e != nil {
			return e
		}
		total := o.Refunded + amount
		if !slices.Contains([]string{"partially_refunded", "refunded"}, remote.Payment) || fresh.Refunded != total {
			return fault.New(409, "平台已退金额与本次核准金额不一致。")
		}
		reference = "recharge-order:" + *r.Remote + ":" + Amount(total)
		return nil
	})
	return reference, e
}
func (s *Service) RefundOriginal(ctx context.Context, id, caseID string, amount int64) (string, error) {
	reference := ""
	e := s.locked(ctx, id, func(r Row) error {
		tx, e := s.Store.DB.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		o, e := orders.Get(ctx, tx, id, nil, true)
		if e != nil {
			return e
		}
		r, e = s.fence(ctx, tx, r)
		if e != nil {
			return e
		}
		if r.Remote == nil || r.Collection != "platform_collect" || o.Payment != "paid" || o.Hold == 0 {
			return orders.Conflict()
		}
		records, e := orders.Rows(ctx, tx, `SELECT * FROM recharge_refunds WHERE case_id=$1`, caseID)
		if e != nil {
			return e
		}
		// An existing intent may have succeeded before its response was lost.
		// Recover it with the original key/body even if order sync saw the refund.
		if len(records) == 0 && r.Refunded > o.Refunded {
			return fault.New(409, "原订单已有平台退款，请核对上游已退款后记账。")
		}
		if len(records) == 0 {
			var reason string
			if e = tx.QueryRow(ctx, `SELECT reason FROM refund_cases WHERE id=$1 AND order_id=$2 AND status='approved' AND approved_cents=$3`, caseID, id, amount).Scan(&reason); e != nil {
				return e
			}
			kind := "partial"
			if amount == o.Price-o.Refunded {
				kind = "full"
			}
			raw := Raw(struct {
				Merchant string `json:"merchant_refund_no"`
				Type     string `json:"type"`
				Amount   string `json:"amount"`
				Reason   string `json:"reason"`
			}{caseID, kind, Amount(amount), reason})
			if _, e = tx.Exec(ctx, `INSERT INTO recharge_refunds(case_id,order_id,create_body,updated_at) VALUES($1,$2,$3,$4)`, caseID, id, raw, time.Now().UnixMilli()); e != nil {
				return e
			}
			records = []map[string]any{{"create_body": raw, "remote_id": nil}}
		}
		record := records[0]
		raw := record["create_body"].(string)
		var original struct {
			Merchant string `json:"merchant_refund_no"`
			Amount   string `json:"amount"`
		}
		if json.Unmarshal([]byte(raw), &original) != nil || original.Amount != Amount(amount) || original.Merchant != caseID {
			return orders.Conflict()
		}
		if e = tx.Commit(ctx); e != nil {
			return e
		}
		var v RemoteRefund
		if remote, ok := record["remote_id"].(string); ok {
			e = s.Client.Request(ctx, "/refunds/"+remote, nil, "", &v)
		} else {
			e = s.Client.Request(ctx, "/orders/"+*r.Remote+"/refunds", &raw, "refund:"+caseID, &v)
		}
		if e != nil {
			return e
		}
		n, err := Money(v.Amount)
		if err != nil || !remoteID.MatchString(v.ID) || (record["remote_id"] != nil && record["remote_id"] != v.ID) || v.Order != *r.Remote || v.Merchant != caseID || n != amount || !slices.Contains([]string{"requested", "approved", "processing", "succeeded", "failed", "rejected", "cancelled"}, v.Status) {
			return errors.New("supplier refund mismatch")
		}
		tag, e := s.Store.DB.Exec(ctx, `UPDATE recharge_refunds SET remote_id=$1,status=$2,updated_at=$3 WHERE case_id=$4 AND EXISTS(SELECT 1 FROM recharge_orders WHERE order_id=$5 AND lease_token=$6 AND lease_until>$3)`, v.ID, v.Status, time.Now().UnixMilli(), caseID, id, r.Lease)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return orders.Conflict()
		}
		if slices.Contains([]string{"requested", "approved", "processing"}, v.Status) {
			return refunds.ErrSupplierPending
		}
		if !slices.Contains([]string{"succeeded", "rejected", "cancelled"}, v.Status) {
			return fault.New(409, "平台退款尚未确认，系统会继续核对同一退款单。")
		}
		var latest RemoteOrder
		if e = s.Client.Request(ctx, "/orders/"+*r.Remote, nil, "", &latest); e != nil {
			return e
		}
		if e = s.apply(ctx, r, latest, false); e != nil {
			return e
		}
		if v.Status != "succeeded" {
			return refunds.ErrSupplierRejected
		}
		verified, e := Money(latest.Refunded)
		if e != nil || verified < o.Refunded+amount {
			return fault.New(409, "退款金额尚未核对一致。")
		}
		reference = "recharge:" + caseID
		return nil
	})
	return reference, e
}
