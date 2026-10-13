package payments

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/orders"
	"time"
)

// Rates are integer basis points: 140 = 1.40%. Round once to the nearest cent.
func FeeCents(base, bps int64) (int64, error) {
	if base < 1 || base > 10000000 || bps < 0 || bps > 10000 {
		return 0, errors.New("invalid payment fee")
	}
	fee := (base*bps + 5000) / 10000
	if base+fee > 10000000 {
		return 0, errors.New("payment amount exceeds limit")
	}
	return fee, nil
}

// Cumulative allocation ensures splitting refunds cannot over/under-refund a
// rounding cent. Final merchandise refund returns every remaining fee cent.
func RefundFee(base, fee, previous, amount int64) (int64, error) {
	if base < 1 || base > 10000000 || fee < 0 || fee > base || previous < 0 || amount < 1 || amount > base-previous {
		return 0, errors.New("invalid proportional refund")
	}
	return (fee*(previous+amount)+base/2)/base - (fee*previous+base/2)/base, nil
}

type feeQuoteKey struct{}

func WithFeeQuote(ctx context.Context, bps *int64) context.Context {
	return context.WithValue(ctx, feeQuoteKey{}, bps)
}
func rates(ctx context.Context, q orders.Query) (map[string]int64, error) {
	var out map[string]int64
	err := q.QueryRow(ctx, `SELECT fee_rates_bps FROM payment_settings WHERE id=true`).Scan(&out)
	return out, err
}
func (s Service) insertIntent(ctx context.Context, tx pgx.Tx, o orders.Order, p Provider, expected *int64) error {
	r, err := rates(ctx, tx)
	if err != nil {
		return err
	}
	bps := r[p.ID()]
	if o.Test != 0 {
		bps = 0
	}
	if (expected == nil && bps != 0) || (expected != nil && *expected != bps) {
		return fault.New(409, "支付手续费已变化，请刷新后确认应付金额。")
	}
	fee, err := FeeCents(o.Price, bps)
	if err != nil {
		return fault.New(400, "商品金额与手续费合计超出支付限额。")
	}
	now := time.Now().UnixMilli()
	_, err = tx.Exec(ctx, `INSERT INTO payments(order_id,provider,account,amount_cents,fee_cents,fee_bps,expires_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$8)`, o.ID, p.ID(), p.Account(), o.Price+fee, fee, bps, now+900000, now)
	return err
}

func (s Service) refundIntent(ctx context.Context, p Payment, o orders.Order, id string, amount int64) (Refund, string, error) {
	fee, err := RefundFee(o.Price, p.Fee, o.Refunded, amount)
	if err != nil {
		return Refund{}, "", err
	}
	// An existing intent wins over recalculation, including after a confirmed
	// refund whose enclosing case has not yet completed its local transaction.
	_, err = s.Store.DB.Exec(ctx, `INSERT INTO payment_refund_attempts(id,order_id,product_cents,fee_cents) VALUES($1,$2,$3,$4) ON CONFLICT(id) DO NOTHING`, id, o.ID, amount, fee)
	if err != nil {
		return Refund{}, "", err
	}
	var orderID, state string
	var base, savedFee int64
	err = s.Store.DB.QueryRow(ctx, `SELECT order_id,product_cents,fee_cents,state FROM payment_refund_attempts WHERE id=$1`, id).Scan(&orderID, &base, &savedFee, &state)
	if err != nil {
		return Refund{}, "", err
	}
	if orderID != o.ID || base != amount {
		return Refund{}, "", orders.Conflict()
	}
	return Refund{id, base + savedFee}, state, nil
}
