package domainbilling

import (
	"context"
	"time"

	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/security"
)

// Promotional periods retain normal expiry/version semantics, without consulting
// supplier earnings or writing a debit. They never turn into lifetime-free slots.
func (s Service) renewFree(ctx context.Context, partner, id string, versions ...int64) error {
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = lock(ctx, tx); e != nil {
		return e
	}
	d, e := s.binding(ctx, tx, partner, id)
	if e != nil {
		return e
	}
	initial := len(versions) > 0
	if initial {
		var paid bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM domain_charges WHERE domain_id=$1 AND partner_id=$2 AND period=$3 AND source='promotion' AND state='paid')`, id, partner, versions[0]).Scan(&paid); e != nil {
			return e
		}
		if paid {
			return nil
		}
		if d["free"] == true || d["expires_at"] != nil || d["billing_version"].(int64) != versions[0] {
			return fault.New(409, "域名已开通或计费版本已变化，请刷新。")
		}
	} else if d["free"] == true || d["expires_at"] == nil || d["expires_at"].(time.Time).After(time.Now()) {
		return nil
	}
	period := d["billing_version"].(int64)
	var pending bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM domain_charges WHERE domain_id=$1 AND period=$2)`, id, period).Scan(&pending); e != nil {
		return e
	}
	if pending {
		if initial {
			return fault.New(409, "已有原金额账单，请先查询原单，避免重复开通。")
		}
		return nil
	}
	if _, e = tx.Exec(ctx, `INSERT INTO domain_charges(id,domain_id,partner_id,hostname,period,amount_cents,source,state,paid_at) VALUES($1,$2,$3,$4,$5,0,'promotion','paid',now())`, security.ID(), id, partner, d["hostname"], period); e != nil {
		return e
	}
	if e = extend(ctx, tx, id, period); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
