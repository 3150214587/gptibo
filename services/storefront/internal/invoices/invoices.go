package invoices

import (
	"context"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"gptibo/storefront/internal/auth"
	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/orders"
	"gptibo/storefront/internal/payments"
	"gptibo/storefront/internal/security"
	"gptibo/storefront/internal/store"
)

type Service struct {
	Store        *store.Store
	Auth         *auth.Service
	Provider     payments.Provider
	Payments     *payments.Service
	NotifyOrigin string
}

func get(ctx context.Context, q orders.Query, id, partner string, lock bool) (map[string]any, error) {
	sql := `SELECT i.*,p.name partner_name,p.email partner_email FROM partner_invoices i JOIN partners p ON p.id=i.partner_id WHERE i.id=$1 AND ($2='' OR i.partner_id=$2)`
	if lock {
		sql += ` FOR UPDATE OF i`
	}
	r, e := orders.Rows(ctx, q, sql, id, partner)
	if e != nil {
		return nil, e
	}
	if len(r) != 1 {
		return nil, fault.New(404, "开票申请不存在。")
	}
	return r[0], nil
}
func public(in map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range in {
		if k != "lease_token" && k != "lease_until" {
			out[k] = v
		}
	}
	return out
}
func validText(s string, max int, required bool) bool {
	return (!required || strings.TrimSpace(s) != "") && utf8.ValidString(s) && utf8.RuneCountInString(s) <= max && !strings.ContainsRune(s, 0)
}
func validStatus(s string) bool {
	return s == "" || s == "awaiting_payment" || s == "awaiting_submission" || s == "submitted" || s == "completed"
}
func scope(p auth.Principal) string {
	if p.Role == "partner" {
		return p.Subject
	}
	return ""
}
func (s Service) List(ctx context.Context, partner string, page int, status, query string) (map[string]any, error) {
	if page < 1 || page > 10000 || !validStatus(status) || !validText(query, 100, false) {
		return nil, fault.New(400, "筛选条件无效。")
	}
	tx, e := s.Store.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	from := ` FROM partner_invoices i JOIN partners p ON p.id=i.partner_id WHERE ($1='' OR i.partner_id=$1) AND ($2='' OR i.status=$2) AND ($3='' OR strpos(i.title,$3)>0 OR strpos(i.id,$3)>0 OR strpos(p.email,$3)>0)`
	var total int64
	e = tx.QueryRow(ctx, `SELECT count(*)`+from, partner, status, query).Scan(&total)
	if e != nil {
		return nil, e
	}
	r, e := orders.Rows(ctx, tx, `SELECT i.*,p.name partner_name,p.email partner_email`+from+` ORDER BY i.created_at DESC,i.id DESC LIMIT 20 OFFSET $4`, partner, status, query, (page-1)*20)
	if e != nil {
		return nil, e
	}
	for i, row := range r {
		r[i] = public(row)
	}
	return map[string]any{"rows": r, "total": total, "page": page}, tx.Commit(ctx)
}

type Input struct {
	Key        string `json:"key"`
	IncludeFee *bool  `json:"includeFee"`
	Base       int64  `json:"baseCents"`
	Title      string `json:"title"`
	Tax        string `json:"taxNumber"`
	Type       string `json:"invoiceType"`
	Email      string `json:"email"`
	Address    string `json:"address"`
	Phone      string `json:"phone"`
	Bank       string `json:"bankName"`
	Account    string `json:"bankAccount"`
	Note       string `json:"note"`
}

func (s Service) Create(ctx context.Context, token string, in Input) (map[string]any, error) {
	if !security.ValidID(in.Key) || in.IncludeFee == nil || in.Base < 10 || in.Base > 10000000 || in.Type != "ordinary" {
		return nil, fault.New(400, "申请参数或发票类型无效。")
	}
	values := []struct {
		v        string
		max      int
		required bool
	}{{in.Title, 200, true}, {in.Tax, 100, true}, {in.Address, 300, false}, {in.Phone, 50, false}, {in.Bank, 200, false}, {in.Account, 100, false}, {in.Note, 1000, false}}
	for _, v := range values {
		if !validText(v.v, v.max, v.required) {
			return nil, fault.New(400, "请核对完整开票信息及长度。")
		}
	}
	email, e := auth.Email(in.Email)
	if e != nil {
		return nil, e
	}
	fee := (in.Base*5 + 50) / 100
	include := int64(0)
	if *in.IncludeFee {
		include = 1
	}
	snapshot := map[string]any{"title": strings.TrimSpace(in.Title), "tax_number": strings.TrimSpace(in.Tax), "invoice_type": in.Type, "email": email, "address": strings.TrimSpace(in.Address), "phone": strings.TrimSpace(in.Phone), "bank_name": strings.TrimSpace(in.Bank), "bank_account": strings.TrimSpace(in.Account), "note": strings.TrimSpace(in.Note), "base_cents": in.Base, "fee_cents": fee, "include_fee": include, "invoice_cents": in.Base + include*fee}
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	p, e := s.Auth.Require(ctx, tx, "partner", token)
	if e != nil {
		return nil, e
	}
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,12))`, in.Key); e != nil {
		return nil, e
	}
	existing, e := orders.Rows(ctx, tx, `SELECT * FROM partner_invoices WHERE id=$1`, in.Key)
	if e != nil {
		return nil, e
	}
	if len(existing) == 0 {
		if e = s.Auth.LimitIn(ctx, tx, "invoice-create:"+p.Subject, 20, 3600); e != nil {
			return nil, e
		}
		now := time.Now().UnixMilli()
		_, e = tx.Exec(ctx, `INSERT INTO partner_invoices(id,partner_id,title,tax_number,invoice_type,email,address,phone,bank_name,bank_account,note,base_cents,fee_cents,include_fee,invoice_cents,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$16)`, in.Key, p.Subject, snapshot["title"], snapshot["tax_number"], in.Type, email, snapshot["address"], snapshot["phone"], snapshot["bank_name"], snapshot["bank_account"], snapshot["note"], in.Base, fee, include, in.Base+include*fee, now)
		if e != nil {
			return nil, e
		}
		if e = event(ctx, tx, in.Key, "partner", "created", "代理提交开票信息"); e != nil {
			return nil, e
		}
	} else {
		if existing[0]["partner_id"] != p.Subject {
			return nil, orders.Conflict()
		}
		for k, v := range snapshot {
			if !reflect.DeepEqual(existing[0][k], v) {
				return nil, orders.Conflict()
			}
		}
	}
	row, e := get(ctx, tx, in.Key, p.Subject, false)
	if e != nil {
		return nil, e
	}
	return map[string]any{"invoice": public(row)}, tx.Commit(ctx)
}
func event(ctx context.Context, tx pgx.Tx, id, actor, action, note string) error {
	_, e := tx.Exec(ctx, `INSERT INTO invoice_events(id,invoice_id,actor,action,note,created_at) VALUES($1,$2,$3,$4,$5,$6)`, security.ID(), id, actor, action, note, time.Now().UnixMilli())
	return e
}
func paymentView(p, row map[string]any) any {
	if p == nil {
		return nil
	}
	var qr any
	if row["status"] == "awaiting_payment" && p["state"] == "pending" && p["expires_at"].(int64) > time.Now().UnixMilli() {
		qr = p["qr"]
	}
	return map[string]any{"provider": p["provider"], "status": p["state"], "amountCents": p["amount_cents"], "expiresAt": p["expires_at"], "qr": qr}
}
func currentPayment(ctx context.Context, q orders.Query, id string) (map[string]any, error) {
	r, e := orders.Rows(ctx, q, `SELECT * FROM invoice_payments WHERE invoice_id=$1 ORDER BY CASE state WHEN 'paid' THEN 0 WHEN 'pending' THEN 1 ELSE 2 END,created_at DESC,id DESC LIMIT 1`, id)
	if e != nil {
		return nil, e
	}
	if len(r) == 0 {
		return nil, nil
	}
	return r[0], nil
}
func (s Service) Detail(ctx context.Context, id, partner string, admin bool) (map[string]any, error) {
	tx, e := s.Store.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	row, e := get(ctx, tx, id, partner, false)
	if e != nil {
		return nil, e
	}
	p, e := currentPayment(ctx, tx, id)
	if e != nil {
		return nil, e
	}
	events, e := orders.Rows(ctx, tx, `SELECT * FROM invoice_events WHERE invoice_id=$1 ORDER BY created_at,id`, id)
	if e != nil {
		return nil, e
	}
	out := map[string]any{"invoice": public(row), "events": events, "payment": paymentView(p, row)}
	if admin {
		out["submission"] = map[string]any{"requestId": id, "title": row["title"], "taxNumber": row["tax_number"], "invoiceType": row["invoice_type"], "amountCents": row["invoice_cents"], "email": row["email"], "address": row["address"], "phone": row["phone"], "bankName": row["bank_name"], "bankAccount": row["bank_account"], "note": row["note"]}
	}
	return out, tx.Commit(ctx)
}

type ActionInput struct {
	Version   *int64 `json:"version"`
	Reference string `json:"reference"`
	Note      string `json:"note"`
}

func (s Service) Advance(ctx context.Context, token, id string, submit bool, in ActionInput) error {
	if in.Version == nil || *in.Version < 0 || !validText(in.Reference, 200, false) || !validText(in.Note, 500, false) {
		return fault.New(400, "开票处理参数无效。")
	}
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	p, e := s.Auth.Require(ctx, tx, "admin", token)
	if e != nil {
		return e
	}
	row, e := get(ctx, tx, id, "", true)
	if e != nil {
		return e
	}
	expected, next, column := "submitted", "completed", "completed_at"
	if submit {
		expected, next, column = "awaiting_submission", "submitted", "submitted_at"
	}
	if row["status"] != expected || row["version"] != *in.Version || row["paid_at"] == nil {
		return orders.Conflict()
	}
	reference := strings.TrimSpace(in.Reference)
	if reference == "" {
		reference = row["platform_reference"].(string)
	}
	note := strings.TrimSpace(in.Note)
	if note == "" {
		if submit {
			note = "已人工提交开票平台"
		} else {
			note = "平台已完成开票"
		}
	}
	_, e = tx.Exec(ctx, `UPDATE partner_invoices SET status=$1,`+column+`=$2,platform_reference=$3,admin_note=$4,updated_at=$2,version=version+1 WHERE id=$5`, next, time.Now().UnixMilli(), reference, strings.TrimSpace(in.Note), id)
	if e != nil {
		return e
	}
	if e = event(ctx, tx, id, "admin:"+p.Subject, next, note); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
