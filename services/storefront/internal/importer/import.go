// Package importer accepts only the versioned offline snapshot format, never SQL.
package importer

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"gptibo/storefront/internal/store"
	"io"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed legacy-schema.json
var schema []byte

//go:embed legacy-extensions.json
var extensionSchema []byte

type Row map[string]*string
type Snapshot struct {
	Format   int              `json:"format"`
	Schema   string           `json:"schema"`
	Tables   map[string][]Row `json:"tables"`
	Excluded map[string]int64 `json:"excluded"`
}
type Column struct {
	Name string `json:"name"`
	Type string `json:"type"`
	PK   int    `json:"pk"`
}
type Table struct {
	Name    string   `json:"name"`
	Columns []Column `json:"columns"`
}
type Stats struct {
	Rows    int      `json:"rows"`
	SHA256  string   `json:"sha256"`
	Columns []string `json:"columns"`
}
type Report struct {
	SHA256     string            `json:"sha256"`
	Applied    bool              `json:"applied"`
	SourceRows map[string]int    `json:"source_rows"`
	Excluded   map[string]int64  `json:"expired_auth_rows"`
	Targets    map[string]Stats  `json:"targets"`
	Totals     map[string]string `json:"totals"`
	Review     map[string]int64  `json:"review"`
}

var excluded = []string{"admin_sessions", "admin_email_login_codes", "admin_login_limits", "admin_totp_usage", "partner_sessions", "partner_login_codes", "customer_email_sessions", "customer_login_codes"}

func hash(b []byte) string  { v := sha256.Sum256(b); return hex.EncodeToString(v[:]) }
func text(s string) *string { return &s }
func value(r Row, k string) string {
	if r[k] == nil {
		return ""
	}
	return *r[k]
}
func ident(s string) string { return pgx.Identifier{s}.Sanitize() }

// Git distributions use LF; existing Windows exports may use CRLF. Accept only
// these two byte representations of the exact supported schema, not arbitrary JSON.
func supportedSchema(digest string) bool {
	lf := strings.ReplaceAll(string(schema), "\r\n", "\n")
	return digest == hash([]byte(lf)) || digest == hash([]byte(strings.ReplaceAll(lf, "\n", "\r\n")))
}

func load(path string) (Snapshot, string, error) {
	var snapshot Snapshot
	f, e := os.Open(path)
	if e != nil {
		return snapshot, "", errors.New("snapshot unavailable")
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 256*1024*1024+1))
	if e != nil || len(b) > 256*1024*1024 {
		return snapshot, "", errors.New("snapshot exceeds 256 MiB")
	}
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if e = d.Decode(&snapshot); e != nil {
		return snapshot, "", errors.New("invalid snapshot format")
	}
	if d.Decode(new(any)) != io.EOF || snapshot.Format != 1 || !supportedSchema(snapshot.Schema) {
		return snapshot, "", errors.New("unsupported snapshot schema")
	}
	return snapshot, hash(b), nil
}

// Run executes validation, insertion and reconciliation in one transaction.
// A dry run always rolls back. Apply requires the exact digest reviewed by dry run.
func Run(ctx context.Context, s *store.Store, path, expected string, apply bool) (Report, error) {
	snap, digest, e := load(path)
	if e != nil {
		return Report{}, e
	}
	report := Report{SHA256: digest, SourceRows: map[string]int{}, Excluded: snap.Excluded, Targets: map[string]Stats{}, Totals: map[string]string{}, Review: map[string]int64{}}
	if apply && (expected == "" || expected != digest) {
		return report, errors.New("apply requires the reviewed snapshot SHA256")
	}
	var spec struct {
		Tables []Table `json:"tables"`
	}
	if e = json.Unmarshal(schema, &spec); e != nil {
		return report, e
	}
	var extensions map[string][]Column
	if e = json.Unmarshal(extensionSchema, &extensions); e != nil {
		return report, e
	}
	if len(snap.Tables) != len(spec.Tables)-len(excluded) || len(snap.Excluded) != len(excluded) {
		return report, errors.New("snapshot table inventory differs")
	}
	for _, t := range spec.Tables {
		if slices.Contains(excluded, t.Name) {
			n, ok := snap.Excluded[t.Name]
			if !ok || n < 0 {
				return report, errors.New("invalid expired-auth inventory")
			}
			continue
		}
		rows, ok := snap.Tables[t.Name]
		if !ok {
			return report, fmt.Errorf("missing table %s", t.Name)
		}
		report.SourceRows[t.Name] = len(rows)
		for i, r := range rows {
			columns := append([]Column{}, t.Columns...)
			for _, c := range extensions[t.Name] {
				if _, ok := r[c.Name]; ok {
					columns = append(columns, c)
				}
			}
			if len(r) != len(columns) {
				return report, fmt.Errorf("column inventory: %s row %d", t.Name, i)
			}
			for _, c := range columns {
				v, ok := r[c.Name]
				if !ok {
					return report, fmt.Errorf("missing column: %s", t.Name)
				}
				if v != nil && c.Type == "INTEGER" {
					if _, e = strconv.ParseInt(*v, 10, 64); e != nil {
						return report, fmt.Errorf("invalid integer: %s row %d", t.Name, i)
					}
				}
			}
		}
	}
	if e = s.Verify(ctx); e != nil {
		return report, e
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return report, e
	}
	defer tx.Rollback(context.Background())
	if _, e = tx.Exec(ctx, `SET LOCAL timezone='UTC'; SET LOCAL lock_timeout='3s'; SELECT pg_advisory_xact_lock(903493); SET CONSTRAINTS ALL DEFERRED`); e != nil {
		return report, errors.New("maintenance lock unavailable")
	}
	tables, e := tx.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname=current_schema() ORDER BY tablename`)
	if e != nil {
		return report, e
	}
	names := []string{}
	for tables.Next() {
		var n string
		tables.Scan(&n)
		names = append(names, n)
	}
	tables.Close()
	if e = tables.Err(); e != nil {
		return report, e
	}
	for _, n := range names {
		if _, e = tx.Exec(ctx, "LOCK TABLE "+ident(n)+" IN ACCESS EXCLUSIVE MODE"); e != nil {
			return report, errors.New("target must be offline")
		}
		if slices.Contains([]string{"storefront_migrations", "accounting_state", "default_price_settings", "payment_settings", "partner_ticket_notifications"}, n) {
			if n != "storefront_migrations" {
				var changed bool
				if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM "+ident(n)+" WHERE version<>0)").Scan(&changed); e != nil || changed {
					return report, errors.New("target settings are not fresh")
				}
			}
			continue
		}
		var count int64
		if e = tx.QueryRow(ctx, "SELECT count(*) FROM "+ident(n)).Scan(&count); e != nil {
			return report, e
		}
		if count != 0 {
			return report, fmt.Errorf("target must be empty: %s", n)
		}
	}
	// Only the two recovery INSERT side effects are disabled, never FK/check constraints.
	if _, e = tx.Exec(ctx, `ALTER TABLE xgift_recovery_attempts DISABLE TRIGGER xgift_recovery_guard; ALTER TABLE xgift_recovery_attempts DISABLE TRIGGER xgift_recovery_apply; DELETE FROM accounting_state; DELETE FROM default_price_settings`); e != nil {
		return report, errors.New("database owner maintenance connection required")
	}
	// Base catalog and partner FKs are immediate; all business references are deferred.
	priority := func(n string) int {
		switch n {
		case "catalog_plans":
			return 0
		case "partners":
			return 1
		}
		return 2
	}
	sort.SliceStable(spec.Tables, func(i, j int) bool { return priority(spec.Tables[i].Name) < priority(spec.Tables[j].Name) })
	expectedRows := map[string][]string{}
	for _, table := range spec.Tables {
		if slices.Contains(excluded, table.Name) {
			continue
		}
		for i, source := range snap.Tables[table.Name] {
			raw, _ := json.Marshal(source)
			if _, e = tx.Exec(ctx, `INSERT INTO legacy_import_rows(source_table,ordinal,payload) VALUES($1,$2,$3)`, table.Name, i, raw); e != nil {
				return report, errors.New("archive insert failed")
			}
			transformed, err := transform(table.Name, source, i, &report)
			if err != nil {
				return report, fmt.Errorf("mapping invalid: %s row %d", table.Name, i)
			}
			for target, rows := range transformed {
				for _, row := range rows {
					columns := make([]string, 0, len(row))
					for k := range row {
						columns = append(columns, k)
					}
					sort.Strings(columns)
					existing, ok := report.Targets[target]
					if ok && !slices.Equal(existing.Columns, columns) {
						return report, errors.New("inconsistent target projection")
					}
					existing.Columns = columns
					existing.Rows++
					report.Targets[target] = existing
					fields := []string{}
					for _, c := range columns {
						fields = append(fields, ident(c))
					}
					values := make(map[string]any, len(row))
					for k, v := range row {
						values[k] = v
					}
					if target == "partner_order_versions" {
						var hidden []string
						if json.Unmarshal([]byte(value(row, "hidden_product_ids")), &hidden) != nil || hidden == nil {
							return report, errors.New("invalid hidden product list")
						}
						values["hidden_product_ids"] = hidden
					}
					encoded, _ := json.Marshal(values)
					query := "INSERT INTO " + ident(target) + " (" + strings.Join(fields, ",") + ") SELECT " + strings.Join(fields, ",") + " FROM jsonb_populate_record(NULL::" + ident(target) + ",$1) RETURNING jsonb_build_array(" + strings.Join(fields, ",") + ")::text"
					var normalized string
					if e = tx.QueryRow(ctx, query, encoded).Scan(&normalized); e != nil {
						var pg *pgconn.PgError
						if errors.As(e, &pg) {
							return report, fmt.Errorf("constraint rejected: %s row %d (SQLSTATE %s, constraint %s)", table.Name, i, pg.Code, pg.ConstraintName)
						}
						return report, fmt.Errorf("constraint rejected: %s row %d", table.Name, i)
					}
					expectedRows[target] = append(expectedRows[target], hash([]byte(normalized)))
				}
			}
		}
	}
	// Empty optional settings are represented by the native singleton defaults.
	if _, e = tx.Exec(ctx, `INSERT INTO accounting_state(id) VALUES(1) ON CONFLICT DO NOTHING; INSERT INTO default_price_settings(id) VALUES(1) ON CONFLICT DO NOTHING`); e != nil {
		return report, e
	}
	// Default costs moved into catalog_plans; original settings remain in the archive.
	if _, e = tx.Exec(ctx, `UPDATE catalog_plans c SET default_cost_cents=(r.payload->>'cost_cents')::bigint FROM legacy_import_rows r WHERE r.source_table='partner_default_prices' AND c.id=r.payload->>'plan'`); e != nil {
		return report, errors.New("default price mapping violates catalog bounds")
	}
	// Audit all source references that had no D1 FK or changed representation.
	var orphans int
	if e = tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM legacy_import_rows r LEFT JOIN catalog_plans c ON c.id=r.payload->>'plan' WHERE r.source_table='partner_default_prices' AND c.id IS NULL)+(SELECT count(*) FROM support_contacts c LEFT JOIN partners p ON p.id=c.owner WHERE c.owner<>'platform' AND p.id IS NULL)`).Scan(&orphans); e != nil || orphans != 0 {
		return report, errors.New("unmapped default prices or contact owners")
	}
	if _, e = tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE; ALTER TABLE xgift_recovery_attempts ENABLE TRIGGER xgift_recovery_guard; ALTER TABLE xgift_recovery_attempts ENABLE TRIGGER xgift_recovery_apply`); e != nil {
		return report, errors.New("cross-table integrity check failed")
	}
	for table, stats := range report.Targets {
		got, err := tableHash(ctx, tx, table, stats.Columns)
		if err != nil {
			return report, err
		}
		stats.SHA256 = rowHash(expectedRows[table])
		report.Targets[table] = stats
		if got.SHA256 != stats.SHA256 || got.Rows != stats.Rows {
			return report, fmt.Errorf("reconciliation mismatch: %s", table)
		}
	}
	// Verify default-cost migration independently because it is outside source catalog columns.
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM legacy_import_rows r JOIN catalog_plans c ON c.id=r.payload->>'plan' WHERE r.source_table='partner_default_prices' AND c.default_cost_cents IS DISTINCT FROM (r.payload->>'cost_cents')::bigint`).Scan(&orphans); e != nil || orphans != 0 {
		return report, errors.New("default prices differ after import")
	}
	for key, query := range map[string]string{
		"orders_sale_cents":     "SELECT COALESCE(sum(price_cents),0)::text FROM orders",
		"orders_refunded_cents": "SELECT COALESCE(sum(refunded_cents),0)::text FROM orders",
		"payout_cents":          "SELECT COALESCE(sum(amount_cents),0)::text FROM partner_payouts",
		"debit_cents":           "SELECT COALESCE(sum(amount_cents),0)::text FROM partner_debits",
		"invoice_fee_cents":     "SELECT COALESCE(sum(fee_cents),0)::text FROM partner_invoices",
	} {
		var n string
		if e = tx.QueryRow(ctx, query).Scan(&n); e != nil {
			return report, e
		}
		report.Totals[key] = n
	}
	for key, query := range map[string]string{
		"financial_holds":          "SELECT count(*) FROM recharge_orders WHERE financial_hold=1",
		"unknown_redemptions":      "SELECT count(*) FROM recharge_redemptions WHERE status='unknown'",
		"approved_refunds":         "SELECT count(*) FROM refund_cases WHERE status='approved'",
		"unsent_delivery":          "SELECT count(*) FROM email_deliveries WHERE status<>'sent'",
		"pending_payouts":          "SELECT count(*) FROM partner_payouts WHERE status='pending'",
		"pending_payments":         "SELECT count(*) FROM payments WHERE state='pending'",
		"pending_invoice_payments": "SELECT count(*) FROM invoice_payments WHERE state='pending'",
		"uncertain_delivery_supplier":        "SELECT count(*) FROM delivery_supplier_orders WHERE state IN ('creating','unknown','pending')",
		"uncertain_gmail":          "SELECT count(*) FROM account_supplier_orders WHERE state IN ('creating','unknown')",
		"uncertain_xgift":          "SELECT count(*) FROM xgift_purchases WHERE state IN ('creating','unknown','pending')",
		"refund_holds":             "SELECT count(*) FROM orders WHERE refund_hold=1",
	} {
		var n int64
		if e = tx.QueryRow(ctx, query).Scan(&n); e != nil {
			return report, e
		}
		report.Review[key] = n
	}
	// The receipt also covers initially empty tables and native/defaulted columns.
	for _, table := range names {
		if slices.Contains([]string{"storefront_migrations", "legacy_imports", "worker_heartbeats", "admin_totp_usage"}, table) || strings.HasPrefix(table, "auth_") {
			continue
		}
		cols, e := tx.Query(ctx, `SELECT column_name FROM information_schema.columns WHERE table_schema=current_schema() AND table_name=$1 ORDER BY ordinal_position`, table)
		if e != nil {
			return report, e
		}
		columns := []string{}
		for cols.Next() {
			var c string
			cols.Scan(&c)
			columns = append(columns, c)
		}
		cols.Close()
		if e = cols.Err(); e != nil {
			return report, e
		}
		stats, e := tableHash(ctx, tx, table, columns)
		if e != nil {
			return report, e
		}
		report.Targets[table] = stats
	}
	if apply {
		report.Applied = true
		raw, _ := json.Marshal(report)
		if _, e = tx.Exec(ctx, `INSERT INTO legacy_imports(sha256,report) VALUES($1,$2)`, digest, raw); e != nil {
			return report, e
		}
		if e = tx.Commit(ctx); e != nil {
			report.Applied = false
			return report, errors.New("import commit uncertain; inspect import receipt before retry")
		}
	}
	return report, nil
}

func rowHash(rows []string) string { sort.Strings(rows); return hash([]byte(strings.Join(rows, "\n"))) }
func tableHash(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, table string, columns []string) (Stats, error) {
	fields := []string{}
	for _, c := range columns {
		fields = append(fields, ident(c))
	}
	rows, e := q.Query(ctx, "SELECT jsonb_build_array("+strings.Join(fields, ",")+")::text FROM "+ident(table))
	if e != nil {
		return Stats{}, e
	}
	defer rows.Close()
	hashes := []string{}
	for rows.Next() {
		var v string
		if e = rows.Scan(&v); e != nil {
			return Stats{}, e
		}
		hashes = append(hashes, hash([]byte(v)))
	}
	return Stats{Rows: len(hashes), SHA256: rowHash(hashes), Columns: columns}, rows.Err()
}

func transform(table string, source Row, index int, report *Report) (map[string][]Row, error) {
	r := Row{}
	for k, v := range source {
		r[k] = v
	}
	// Runtime leases cannot survive the old worker shutdown. Business keys stay byte-identical.
	for _, k := range []string{"lease_until", "lock_until", "locked_until"} {
		if _, ok := r[k]; ok {
			r[k] = text("0")
		}
	}
	if _, ok := r["lease_token"]; ok {
		r["lease_token"] = nil
	}
	if _, ok := r["lock_token"]; ok {
		r["lock_token"] = nil
		if table == "payout_notifications" || table == "recharge_reminders" {
			r["lock_token"] = text("")
		}
	}
	boolean := func(k string) error {
		if value(r, k) != "0" && value(r, k) != "1" {
			return errors.New("invalid boolean")
		}
		r[k] = text(strconv.FormatBool(value(r, k) == "1"))
		return nil
	}
	timestamp := func(k string) error {
		n, e := strconv.ParseInt(value(r, k), 10, 64)
		if e != nil {
			return e
		}
		r[k] = text(time.UnixMilli(n).UTC().Format(time.RFC3339Nano))
		return nil
	}
	switch table {
	case "catalog_plans":
		for _, k := range []string{"enabled", "partner_enabled", "renewal_only"} {
			if e := boolean(k); e != nil {
				return nil, e
			}
		}
		if e := timestamp("updated_at"); e != nil {
			return nil, e
		}
		r["sort_order"] = text(strconv.Itoa(index))
	case "partners":
		if e := boolean("active"); e != nil {
			return nil, e
		}
		if e := timestamp("created_at"); e != nil {
			return nil, e
		}
		delete(r, "updated_at")
	case "support_contacts":
		delete(r, "partner_id")
		delete(r, "change_id")
		delete(r, "updated_at")
	case "partner_announcements":
		if e := boolean("enabled"); e != nil {
			return nil, e
		}
		delete(r, "change_id")
		delete(r, "updated_at")
	case "partner_default_price_settings":
		table = "default_price_settings"
		delete(r, "change_id")
		delete(r, "updated_at")
	case "partner_default_prices":
		return map[string][]Row{}, nil
	case "partner_product_order":
		var products []string
		if json.Unmarshal([]byte(value(r, "product_ids")), &products) != nil {
			return nil, errors.New("invalid product order")
		}
		hidden := r["hidden_product_ids"]
		if hidden == nil {
			hidden = text("[]")
		}
		out := map[string][]Row{"partner_order_versions": {{"partner_id": r["partner_id"], "version": r["version"], "hidden_product_ids": hidden}}}
		for i, p := range products {
			out["partner_product_order"] = append(out["partner_product_order"], Row{"partner_id": r["partner_id"], "product_id": text(p), "position": text(strconv.Itoa(i))})
		}
		return out, nil
	case "admin_audit":
		table = "audit_events"
		r["actor_role"] = text("admin")
		r["actor_id"] = text("legacy-import")
		r["target"] = r["target_id"]
		delete(r, "target_id")
		detail, _ := json.Marshal(map[string]any{"note": value(r, "note"), "source": "D1"})
		r["detail"] = text(string(detail))
		delete(r, "note")
		if e := timestamp("created_at"); e != nil {
			return nil, e
		}
	case "telegram_partner_applications":
		// D1 did not freeze these invitations. Unknown prior sends cannot safely reuse a changed body.
		if value(r, "mail_status") == "pending" && value(r, "mail_attempts") != "0" {
			r["mail_status"] = text("failed")
			r["notice_sent"] = text("0")
			report.Review["invitation_delivery_unconfirmed"]++
		}
	}
	return map[string][]Row{table: {r}}, nil
}

// Reconcile checks a committed import before enabling API/workers. It never writes.
func Reconcile(ctx context.Context, s *store.Store, digest string) (Report, error) {
	var raw []byte
	var report Report
	tx, e := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return report, e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SET LOCAL timezone='UTC'`); e != nil {
		return report, e
	}
	if e = tx.QueryRow(ctx, `SELECT report FROM legacy_imports WHERE sha256=$1`, digest).Scan(&raw); e != nil {
		return report, errors.New("import receipt not found")
	}
	if e = json.Unmarshal(raw, &report); e != nil {
		return report, e
	}
	for table, expected := range report.Targets {
		actual, e := tableHash(ctx, tx, table, expected.Columns)
		if e != nil || actual.Rows != expected.Rows || actual.SHA256 != expected.SHA256 {
			return report, fmt.Errorf("imported data changed: %s", table)
		}
	}
	return report, nil
}
