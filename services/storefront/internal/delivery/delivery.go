package delivery

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"gptibo/storefront/internal/auth"
	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/mailer"
	"gptibo/storefront/internal/orders"
	"gptibo/storefront/internal/security"
	"gptibo/storefront/internal/store"
)

type Service struct {
	DomainsEnabled     func() bool
	Store              *store.Store
	Auth               *auth.Service
	Sender             mailer.Sender
	Origin, AdminEmail string
}

func claimURL(value string) string {
	u, e := url.Parse(value)
	if e != nil || u.Scheme != "https" || u.User != nil || !strings.Contains(u.Hostname(), ".") || strings.ContainsAny(value, "<>\"'") {
		return ""
	}
	return value
}
func Message(plan, id, value, instructions, origin string, brands ...mailer.Brand) (mailer.Message, error) {
	kind := orders.EmailKind(plan)
	if kind == "" {
		return mailer.Message{}, fault.New(400, "该商品不支持邮件交付。")
	}
	link := ""
	if plan != "gmail-account" {
		link = claimURL(value)
	}
	if kind == "link" && link == "" {
		return mailer.Message{}, fault.New(400, "请填写有效 HTTPS 领取链接。")
	}
	template := "delivery-cdk"
	if plan == "gmail-account" {
		template = "delivery-gmail"
	} else if kind == "link" {
		template = "delivery-link"
	} else if link != "" {
		template = "delivery-duolingo-link"
	}
	brand := mailer.PlatformBrand(origin)
	if len(brands) > 0 {
		brand = brands[0]
	}
	return mailer.BrandedTemplate(template, brand, map[string]string{"DELIVERY_ORDER": id, "DELIVERY_VALUE": value, "DELIVERY_URL": link, "DELIVERY_INSTRUCTIONS": instructions})
}
func ValidValue(kind, value string) bool {
	limit := 512
	if kind == "link" {
		limit = 2048
	}
	if strings.TrimSpace(value) == "" || !utf8.ValidString(value) || len(value) > limit {
		return false
	}
	for _, r := range value {
		if r < 32 || r == 127 {
			return false
		}
	}
	return kind != "link" || claimURL(value) != ""
}
func eligible(o orders.Order) bool {
	return o.Mode == "live" && o.Test == 0 && o.Payment == "paid" && o.Hold == 0 && o.Refunded == 0 && o.Blocked == 0 && (o.Status == "session" || o.Status == "processing")
}
func find(ctx context.Context, q orders.Query, id string) (map[string]any, error) {
	r, e := orders.Rows(ctx, q, `SELECT * FROM email_deliveries WHERE order_id=$1`, id)
	if e != nil {
		return nil, e
	}
	if len(r) == 0 {
		return nil, nil
	}
	return r[0], nil
}
func (s Service) View(ctx context.Context, id string) (map[string]any, error) {
	tx, e := s.Store.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	o, e := orders.Get(ctx, tx, id, nil, false)
	if e != nil {
		return nil, e
	}
	kind := orders.EmailKind(o.Plan)
	if kind == "" {
		return nil, orders.Conflict()
	}
	row, e := find(ctx, tx, id)
	if e != nil {
		return nil, e
	}
	var instructions string
	e = tx.QueryRow(ctx, `SELECT delivery_instructions FROM catalog_plans WHERE id=$1`, o.Plan).Scan(&instructions)
	if e != nil {
		return nil, e
	}
	out := map[string]any{"kind": kind, "status": "waiting", "sentAt": nil, "value": "", "instructions": instructions, "version": int64(-1), "firstAttemptAt": nil, "providerId": nil, "preview": nil}
	if row != nil {
		var m mailer.Message
		if e = json.Unmarshal([]byte(row["mail_body"].(string)), &m); e != nil {
			return nil, e
		}
		out["status"] = row["status"]
		out["sentAt"] = row["sent_at"]
		out["value"] = row["value"]
		out["instructions"] = row["instructions"]
		out["version"] = row["version"]
		out["firstAttemptAt"] = row["first_attempt_at"]
		out["providerId"] = row["provider_id"]
		out["preview"] = m
	}
	var automatic bool
	e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM delivery_supplier_orders WHERE order_id=$1)`, id).Scan(&automatic)
	if e != nil {
		return nil, e
	}
	if automatic {
		out["automatic"] = true
	}
	return map[string]any{"delivery": out, "recipient": o.Email, "orderVersion": o.Version}, tx.Commit(ctx)
}

// Called by verified supplier purchase transactions; it never sends a message.
func (s Service) SaveDraft(ctx context.Context, tx pgx.Tx, o orders.Order, value, instructions string) error {
	if s.Sender == nil || o.Email == nil {
		return fault.New(503, "邮件服务或收件地址未配置。")
	}
	brand, e := s.brands(tx).Order(ctx, o.ID)
	if e != nil {
		return e
	}
	m, e := Message(o.Plan, o.ID, value, instructions, s.Origin, brand)
	if e != nil {
		return e
	}
	body, e := s.Sender.Freeze(*o.Email, m)
	if e != nil {
		return e
	}
	tag, e := tx.Exec(ctx, `INSERT INTO email_deliveries(order_id,kind,value,instructions,mail_body,request_key,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$7) ON CONFLICT(order_id) DO UPDATE SET value=excluded.value,instructions=excluded.instructions,mail_body=excluded.mail_body,request_key=excluded.request_key,updated_at=excluded.updated_at,version=email_deliveries.version+1 WHERE email_deliveries.status='draft'`, o.ID, orders.EmailKind(o.Plan), value, instructions, body, security.ID(), time.Now().UnixMilli())
	if e == nil && tag.RowsAffected() != 1 {
		return orders.Conflict()
	}
	return e
}

type Input struct {
	Action       string `json:"action"`
	Version      *int64 `json:"version"`
	OrderVersion *int64 `json:"orderVersion"`
	Value        string `json:"value"`
	Instructions string `json:"instructions"`
	Note         string `json:"note"`
}

func (s Service) Act(ctx context.Context, id, token string, in Input, system bool) error {
	if in.Action != "save" && in.Action != "send" && in.Action != "confirm-sent" {
		return fault.New(400, "发货操作无效。")
	}
	if in.Version == nil || in.OrderVersion == nil {
		return fault.New(400, "请提供订单及发货版本。")
	}
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	actor := "system"
	if !system {
		p, e := s.Auth.Require(ctx, tx, "admin", token)
		if e != nil {
			return e
		}
		actor = "admin:" + p.Subject
	} else if in.Action != "send" {
		return fault.New(403, "自动任务仅能发送已保存内容。")
	}
	o, e := orders.Get(ctx, tx, id, nil, true)
	if e != nil {
		return e
	}
	if orders.EmailKind(o.Plan) == "" {
		return orders.Conflict()
	}
	row, e := find(ctx, tx, id)
	if e != nil {
		return e
	}
	if in.Action != "save" && row != nil && row["status"] == "sent" {
		return tx.Commit(ctx)
	}
	if !eligible(o) || s.Sender == nil {
		return fault.New(409, "仅邮件服务已配置的已付款、无售后及暂停标记的待发货订单可发货。")
	}
	version := int64(-1)
	if row != nil {
		version = row["version"].(int64)
	}
	if version != *in.Version || o.Version != *in.OrderVersion {
		return orders.Conflict()
	}
	if in.Action == "save" {
		var auto bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM delivery_supplier_orders WHERE order_id=$1)`, id).Scan(&auto)
		if e != nil {
			return e
		}
		if auto || o.Plan == "gmail-account" || (row != nil && row["status"] != "draft") {
			return fault.New(409, "自动采购或已开始发送的原发货内容不能替换。")
		}
		if !ValidValue(orders.EmailKind(o.Plan), in.Value) || strings.TrimSpace(in.Instructions) == "" || utf8.RuneCountInString(in.Instructions) > 2000 {
			return fault.New(400, "请填写有效发货内容和 1–2000 字领取说明。")
		}
		if e = s.SaveDraft(ctx, tx, o, strings.TrimSpace(in.Value), strings.TrimSpace(in.Instructions)); e != nil {
			return e
		}
		if _, e = orders.Change(ctx, tx, o, o, actor, "email-delivery-draft", "已保存邮件发货草稿"); e != nil {
			return e
		}
		return tx.Commit(ctx)
	}
	if row == nil {
		return fault.New(409, "请先保存并预览发货内容。")
	}
	confirm := in.Action == "confirm-sent"
	now := time.Now().UnixMilli()
	if confirm && (row["first_attempt_at"] == nil || utf8.RuneCountInString(strings.TrimSpace(in.Note)) < 4 || utf8.RuneCountInString(in.Note) > 500) {
		return fault.New(400, "请核实邮件服务原记录，并填写至少 4 字核查说明。")
	}
	if !confirm && row["first_attempt_at"] != nil && now-row["first_attempt_at"].(int64) >= 23*3600000 {
		return fault.New(409, "原邮件已超过安全重试时间，请核查原记录后人工确认。")
	}
	lease := security.ID()
	tag, e := tx.Exec(ctx, `UPDATE email_deliveries SET status='sending',lease_token=$1,lease_until=$2,first_attempt_at=COALESCE(first_attempt_at,$3),updated_at=$3,version=version+1 WHERE order_id=$4 AND lease_until<=$3`, lease, now+60000, now, id)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return orders.Conflict()
	}
	next := o
	next.Status = "processing"
	if next.Started == nil {
		next.Started = &now
	}
	if _, e = orders.Change(ctx, tx, o, next, actor, "email-delivery-send", "开始核对邮件发货"); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	var provider *string
	if !confirm {
		var sent string
		sent, e = s.Sender.SendFrozen(ctx, row["mail_body"].(string), "membership-delivery/"+row["request_key"].(string))
		if e == nil {
			provider = &sent
		}
	}
	if e == nil {
		note := "发货邮件已被邮件服务接收，请客户查看收件箱"
		if confirm {
			note = "管理员已核实原邮件发送：" + strings.TrimSpace(in.Note)
		}
		e = s.finish(ctx, id, lease, provider, actor, note)
	}
	if e != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = s.Store.DB.Exec(cleanup, `UPDATE email_deliveries SET status='unknown',lease_until=0,updated_at=$1,version=version+1 WHERE order_id=$2 AND lease_token=$3 AND status='sending'`, time.Now().UnixMilli(), id, lease)
		return fault.New(503, "邮件发送结果尚未确认，请核对原邮件；安全期内重试会复用原编号和内容。")
	}
	return nil
}
func (s Service) finish(ctx context.Context, id, lease string, provider *string, actor, note string) error {
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	o, e := orders.Get(ctx, tx, id, nil, true)
	if e != nil {
		return e
	}
	if !eligible(o) {
		return orders.Conflict()
	}
	now := time.Now().UnixMilli()
	tag, e := tx.Exec(ctx, `UPDATE email_deliveries SET status='sent',provider_id=COALESCE($1,provider_id),sent_at=$2,updated_at=$2,version=version+1,lease_until=0 WHERE order_id=$3 AND lease_token=$4 AND status='sending' AND lease_until>$2`, provider, now, id, lease)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return orders.Conflict()
	}
	next := o
	next.Status = "success"
	if _, e = orders.Change(ctx, tx, o, next, actor, "email-delivery-sent", note); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s Service) SendSaved(ctx context.Context, id string) error {
	o, e := orders.Get(ctx, s.Store.DB, id, nil, false)
	if e != nil {
		return e
	}
	row, e := find(ctx, s.Store.DB, id)
	if e != nil {
		return e
	}
	if row == nil || row["status"] == "sent" || row["lease_until"].(int64) > time.Now().UnixMilli() {
		return nil
	}
	v := row["version"].(int64)
	return s.Act(ctx, id, "", Input{Action: "send", Version: &v, OrderVersion: &o.Version}, true)
}

func (s Service) brands(q mailer.BrandQuery) mailer.Brands {
	return mailer.Brands{DB: q, Origin: s.Origin, DomainsEnabled: s.DomainsEnabled != nil && s.DomainsEnabled()}
}
