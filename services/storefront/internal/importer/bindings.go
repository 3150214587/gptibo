package importer

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"gptibo/storefront/internal/mailer"
	"gptibo/storefront/internal/payments"
	"gptibo/storefront/internal/procurement"
	"gptibo/storefront/internal/security"
	"gptibo/storefront/internal/store"
	"gptibo/storefront/internal/recharge"
	"gptibo/storefront/internal/xgift"
)

// CheckBindings makes no upstream requests and emits counts, never credentials,
// recipients or payloads. This is a cutover gate, separate from process health.
func CheckBindings(ctx context.Context, s *store.Store, pay payments.Service, tb *recharge.Service, pc *procurement.Service, xg *xgift.Service, from string) (map[string]int64, error) {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	out := map[string]int64{}
	checks := []struct {
		name, query string
		valid       func([]string) bool
	}{
		{"payment_accounts", `SELECT provider,account FROM payments UNION ALL SELECT provider,account FROM invoice_payments`, func(v []string) bool { _, e := pay.Resolve(v[0], v[1]); return e == nil }},
		{"recharge_accounts", `SELECT account FROM recharge_orders`, func(v []string) bool { return tb != nil && tb.Client != nil && v[0] == tb.Client.Account() }},
		{"delivery_supplier_credentials", `SELECT credential_hash FROM delivery_supplier_orders`, func(v []string) bool { return pc != nil && pc.DeliverySupplier != nil && v[0] == security.Hash(pc.DeliverySupplier.Key) }},
		{"gmail_accounts", `SELECT api_base,app_id FROM account_supplier_orders`, func(v []string) bool {
			return pc != nil && pc.AccountSupplier != nil && v[0] == pc.AccountSupplier.Base && v[1] == pc.AccountSupplier.App
		}},
		{"xgift_credentials", `SELECT supplier_backend,credential_hash FROM xgift_purchases`, func(v []string) bool {
			return xg != nil && xg.Clients[v[0]] != nil && v[1] == xg.Clients[v[0]].Fingerprint()
		}},
		{"unsent_mail_sender", `SELECT mail_body FROM email_deliveries WHERE status<>'sent'`, func(v []string) bool {
			var m mailer.Frozen
			return json.Unmarshal([]byte(v[0]), &m) == nil && from != "" && mailer.SameSender(m.From, from) && len(m.To) == 1
		}},
	}
	for _, check := range checks {
		out[check.name+"_checked"] = 0
		out[check.name+"_mismatched"] = 0
		rows, e := tx.Query(ctx, check.query)
		if e != nil {
			return nil, e
		}
		for rows.Next() {
			values, e := rows.Values()
			if e != nil {
				rows.Close()
				return nil, e
			}
			v := make([]string, len(values))
			for i, x := range values {
				v[i], _ = x.(string)
			}
			out[check.name+"_checked"]++
			if !check.valid(v) {
				out[check.name+"_mismatched"]++
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
	}
	return out, tx.Commit(ctx)
}
