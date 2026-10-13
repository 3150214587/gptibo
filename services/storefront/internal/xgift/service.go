package xgift

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"gptibo/storefront/internal/auth"
	"gptibo/storefront/internal/catalog"
	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/orders"
	"gptibo/storefront/internal/security"
	"gptibo/storefront/internal/store"
	"gptibo/storefront/internal/worker"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type Service struct {
	Store                  *store.Store
	Auth                   *auth.Service
	Clients                map[string]*Client
	Backend                string
	Checkout, PaymentReady bool
}
type Purchase struct {
	ID        string  `json:"order_id"`
	Username  string  `json:"username"`
	Recipient string  `json:"recipient_id"`
	Product   string  `json:"product_code"`
	Points    int64   `json:"points"`
	Hash      string  `json:"credential_hash"`
	State     string  `json:"state"`
	Supplier  *string `json:"supplier_order"`
	Receipt   *string `json:"receipt"`
	Failure   *string `json:"failure_code"`
	Lease     *string `json:"lease_token"`
	Until     int64   `json:"lease_until"`
	Attempted *int64  `json:"attempted_at"`
	Next      int64   `json:"next_check"`
	Backend   string  `json:"supplier_backend"`
	Merchant  *string `json:"supplier_merchant_order_no"`
	Recovery  int64   `json:"recovery_attempt"`
}

func find(ctx context.Context, q orders.Query, id string) (Purchase, error) {
	var p Purchase
	var b []byte
	e := q.QueryRow(ctx, `SELECT to_jsonb(p) FROM xgift_purchases p WHERE order_id=$1`, id).Scan(&b)
	if e == nil {
		e = json.Unmarshal(b, &p)
	}
	return p, e
}
func (s *Service) Enabled(plan string) bool {
	return IsPlan(plan) && s.Checkout && s.PaymentReady && s.Clients[s.Backend] != nil
}
func (s *Service) Preflight(ctx context.Context, owner, plan, value string) (map[string]any, error) {
	username := Username(value)
	if username == "" || !IsPlan(plan) {
		return nil, fault.New(400, "请填写正确的 X 用户名并选择套餐。")
	}
	if !s.Enabled(plan) {
		return nil, fault.New(503, "X 赠送暂未开放下单。")
	}
	c := s.Clients[s.Backend]
	check, e := c.Eligibility(ctx, username, plan)
	if e != nil {
		return nil, e
	}
	if check.Username != username || check.Eligible == nil {
		return nil, fault.New(503, "赠送资格暂未确认。")
	}
	if !*check.Eligible {
		message := "72小时内改过资料或头像不可充值"
		if check.Reason == "user_not_found" {
			message = "未找到该 X 账号，请检查用户名。"
		}
		return map[string]any{"eligible": false, "username": username, "message": message}, nil
	}
	if !regexp.MustCompile(`^[0-9]{1,25}$`).MatchString(check.Recipient) {
		return nil, fault.New(503, "无法确认接收账号身份。")
	}
	type result struct {
		products []Product
		balance  Balance
		accept   bool
		e        error
	}
	ch := make(chan result, 3)
	go func() { p, e := c.Products(ctx); ch <- result{products: p, e: e} }()
	go func() { b, e := c.Balance(ctx); ch <- result{balance: b, e: e} }()
	go func() {
		if c.Backend == "platform" {
			ch <- result{accept: true}
			return
		}
		var caps struct {
			Accept bool `json:"accepts_orders"`
		}
		e := c.Request(ctx, "/capabilities", nil, "", &caps)
		ch <- result{accept: caps.Accept, e: e}
	}()
	var all result
	for range 3 {
		r := <-ch
		if r.e != nil {
			all.e = r.e
		}
		if r.products != nil {
			all.products = r.products
		}
		if r.balance.Available > 0 {
			all.balance = r.balance
		}
		all.accept = all.accept || r.accept
	}
	if all.e != nil {
		return nil, all.e
	}
	available := false
	for _, p := range all.products {
		if p.Code == plan && p.Enabled == 1 && p.Points == plans[plan] && all.balance.Available >= p.Points && all.accept {
			available = true
		}
	}
	if !available {
		return nil, fault.New(503, "赠送服务暂不可下单。")
	}
	token, expires := security.ID(), time.Now().UnixMilli()+300000
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `INSERT INTO xgift_preflights(token,owner_hash,username,recipient_id,product_code,points,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, token, owner, username, check.Recipient, plan, plans[plan], expires); e != nil {
		return nil, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO xgift_preflight_backends(token,supplier_backend) VALUES($1,$2)`, token, s.Backend); e != nil {
		return nil, e
	}
	return map[string]any{"eligible": true, "username": username, "token": token, "expires_at": expires, "message": "当前可接收赠送，请核对用户名后继续付款。"}, tx.Commit(ctx)
}
func (s *Service) Snapshot(ctx context.Context, tx pgx.Tx, o orders.Order, in orders.CreateInput) error {
	if !s.Enabled(o.Plan) {
		return orders.Conflict()
	}
	var recipient, backend string
	var points int64
	e := tx.QueryRow(ctx, `SELECT p.recipient_id,p.points,COALESCE(b.supplier_backend,'legacy') FROM xgift_preflights p LEFT JOIN xgift_preflight_backends b ON b.token=p.token WHERE p.token=$1 AND p.owner_hash=$2 AND p.username=$3 AND p.product_code=$4 AND p.expires_at>$5`, in.PreflightToken, o.OwnerHash, Username(in.Username), o.Plan, time.Now().UnixMilli()).Scan(&recipient, &points, &backend)
	if e == pgx.ErrNoRows {
		return fault.New(409, "请重新检测赠送资格；检测有效期为 5 分钟。")
	}
	if e != nil {
		return e
	}
	if backend != s.Backend {
		return orders.Conflict()
	}
	if points != plans[o.Plan] {
		return orders.Conflict()
	}
	_, e = tx.Exec(ctx, `INSERT INTO xgift_purchases(order_id,username,recipient_id,product_code,points,credential_hash,updated_at,supplier_backend) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, o.ID, Username(in.Username), recipient, o.Plan, points, s.Clients[backend].Fingerprint(), time.Now().UnixMilli(), backend)
	return e
}
func (s *Service) Replay(ctx context.Context, q orders.Query, o orders.Order, in orders.CreateInput) error {
	if !IsPlan(o.Plan) {
		return nil
	}
	p, e := find(ctx, q, o.ID)
	if e != nil {
		return e
	}
	if p.Username != Username(in.Username) {
		return fault.New(409, "请求编号已绑定其他接收账号。")
	}
	return nil
}
func (s *Service) Availability(_ context.Context, p []catalog.Plan) []catalog.Plan {
	for i := range p {
		if IsPlan(p[i].ID) && !s.Enabled(p[i].ID) {
			p[i].Enabled = 0
		}
	}
	return p
}
func merchant(p Purchase) string {
	if p.Merchant != nil {
		return *p.Merchant
	}
	return p.ID
}
func validResult(v Remote, p Purchase) error {
	currency, amount := "bdt", int64(30000)
	if p.Product == "x-premium-6m" {
		amount = 60000
	}
	if p.Backend == "platform" {
		currency, amount = "CNY", p.Points
	}
	if v.Merchant != merchant(p) || v.Recipient != p.Username || v.Product != p.Product || v.Points != p.Points || v.Currency != currency || v.Amount != amount || !regexp.MustCompile(`^ord_[a-f0-9]{32}$`).MatchString(v.ID) || (p.Supplier != nil && *p.Supplier != v.ID) || !slices.Contains([]string{"queued", "running", "unknown", "succeeded", "failed"}, v.Status) {
		return errors.New("gift result identity mismatch")
	}
	if v.Status == "succeeded" {
		pattern := `^cs_live_[A-Za-z0-9]+$`
		if p.Backend == "platform" {
			pattern = `^rcpt_[a-f0-9]{24}$`
		}
		if v.Receipt == nil || !regexp.MustCompile(pattern).MatchString(*v.Receipt) {
			return errors.New("gift receipt mismatch")
		}
	}
	return nil
}
func eligible(o orders.Order) bool {
	return o.Mode == "live" && o.Test == 0 && o.Payment == "paid" && o.Hold == 0 && o.Refunded == 0 && o.Blocked == 0 && (o.Status == "session" || o.Status == "processing")
}
func (s *Service) fence(ctx context.Context, tx pgx.Tx, p Purchase) (orders.Order, Purchase, error) {
	o, e := orders.Get(ctx, tx, p.ID, nil, true)
	if e != nil {
		return o, p, e
	}
	fresh, e := find(ctx, tx, p.ID)
	if e != nil {
		return o, p, e
	}
	if !eligible(o) || p.Lease == nil || fresh.Lease == nil || *p.Lease != *fresh.Lease || fresh.Until <= time.Now().UnixMilli() {
		return o, p, orders.Conflict()
	}
	return o, fresh, nil
}
func (s *Service) Fulfill(ctx context.Context, id string) error {
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	o, e := orders.Get(ctx, tx, id, nil, true)
	if e != nil {
		return e
	}
	p, e := find(ctx, tx, id)
	if e == pgx.ErrNoRows {
		return nil
	}
	if e != nil {
		return e
	}
	if !eligible(o) || p.State == "failed" || p.State == "succeeded" || p.Next > time.Now().UnixMilli() || p.Until > time.Now().UnixMilli() {
		return nil
	}
	if p.Attempted == nil && !s.Checkout {
		return nil
	}
	c := s.Clients[p.Backend]
	if c == nil || c.Fingerprint() != p.Hash {
		return fault.New(409, "赠送接口凭据已变化，请恢复原配置核对原单。")
	}
	now, lease := time.Now().UnixMilli(), security.ID()
	if _, e = tx.Exec(ctx, `UPDATE xgift_purchases SET lease_token=$1,lease_until=$2 WHERE order_id=$3`, lease, now+120000, id); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	p.Lease = &lease
	p.Until = now + 120000
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = s.Store.DB.Exec(cleanup, `UPDATE xgift_purchases SET lease_until=0 WHERE order_id=$1 AND lease_token=$2`, id, lease)
	}()
	run := func() error {
		var result Remote
		var callErr error
		if p.Attempted == nil {
			check, e := c.Eligibility(ctx, p.Username, p.Product)
			if e != nil {
				return e
			}
			if check.Eligible == nil || check.Username != p.Username {
				return errors.New("gift eligibility mismatch")
			}
			if !*check.Eligible || check.Recipient != p.Recipient {
				failure := "recipient_not_eligible"
				return s.finish(ctx, p, "failed", nil, nil, &failure)
			}
			tx, e := s.Store.DB.Begin(ctx)
			if e != nil {
				return e
			}
			defer tx.Rollback(ctx)
			_, p, e = s.fence(ctx, tx, p)
			if e != nil {
				return e
			}
			attempt := time.Now().UnixMilli()
			if _, e = tx.Exec(ctx, `UPDATE xgift_purchases SET state='creating',attempted_at=$1,updated_at=$1 WHERE order_id=$2 AND attempted_at IS NULL`, attempt, id); e != nil {
				return e
			}
			if e = tx.Commit(ctx); e != nil {
				return e
			}
			p.Attempted = &attempt
			result, callErr = c.Create(ctx, merchant(p), p.Product, p.Username, p.Recipient, p.Points)
		} else {
			result, callErr = c.Query(ctx, merchant(p))
			var api *APIError
			if errors.As(callErr, &api) && p.Supplier == nil && api.Status == 404 && s.Checkout && ((p.Backend == "platform" && api.Code == "order_missing") || (p.Backend == "legacy" && api.Code == "not_found")) {
				result, callErr = c.Create(ctx, merchant(p), p.Product, p.Username, p.Recipient, p.Points)
			}
		}
		if callErr != nil {
			return callErr
		}
		if e = validResult(result, p); e != nil {
			return e
		}
		state := result.Status
		if state == "queued" || state == "running" {
			state = "pending"
		}
		failure := result.Failure
		if failure != nil && !regexp.MustCompile(`^[a-z0-9_]{1,64}$`).MatchString(*failure) {
			other := "unknown"
			failure = &other
		}
		return s.finish(ctx, p, state, &result.ID, result.Receipt, failure)
	}
	e = run()
	if e == nil {
		return nil
	}
	var api *APIError
	if p.Supplier == nil && errors.As(e, &api) && slices.Contains([]string{"not_eligible", "recipient_changed", "product_unavailable", "insufficient_points", "execution_disabled", "product_disabled", "price_changed", "insufficient_balance", "execution_paused", "card_rule_disabled", "card_rule_missing", "card_missing", "x_configuration_missing", "billing_profile_missing", "provider_missing", "worker_unavailable"}, api.Code) {
		return s.finish(ctx, p, "failed", nil, nil, &api.Code)
	}
	cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = s.Store.DB.Exec(cleanup, `UPDATE xgift_purchases SET state=CASE WHEN attempted_at IS NOT NULL THEN 'unknown' ELSE state END,next_check=$1,updated_at=$2 WHERE order_id=$3 AND lease_token=$4`, time.Now().UnixMilli()+15000, time.Now().UnixMilli(), id, lease)
	return e
}
func (s *Service) finish(ctx context.Context, p Purchase, state string, supplier, receipt, failure *string) error {
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	o, p, e := s.fence(ctx, tx, p)
	if e != nil {
		return e
	}
	now := time.Now().UnixMilli()
	if _, e = tx.Exec(ctx, `UPDATE xgift_purchases SET state=$1,supplier_order=COALESCE(supplier_order,$2),receipt=$3,failure_code=$4,next_check=$5,updated_at=$6 WHERE order_id=$7`, state, supplier, receipt, failure, now+5000, now, p.ID); e != nil {
		return e
	}
	next := o
	next.Status = "processing"
	note := "已收款，赠送处理中"
	if state == "succeeded" {
		next.Status = "success"
		note = "X 赠送付款已核验完成，请核对到账"
	} else if state == "failed" {
		next.Status = "failed"
		note = "赠送未完成，请处理原订单售后"
	}
	if o.Status != next.Status {
		if next.Started == nil {
			next.Started = &now
		}
		if _, e = orders.Change(ctx, tx, o, next, "system", "xgift-progress", note); e != nil {
			return e
		}
		if o.PartnerID == nil {
			var cost *int64
			if state == "succeeded" {
				cost = &p.Points
			}
			if _, e = tx.Exec(ctx, `UPDATE orders SET cost_cents=$1 WHERE id=$2`, cost, p.ID); e != nil {
				return e
			}
		}
	}
	return tx.Commit(ctx)
}
func (s *Service) Reconcile(ctx context.Context) error {
	now := time.Now().UnixMilli()
	if _, e := s.Store.DB.Exec(ctx, `DELETE FROM xgift_preflights WHERE expires_at<$1`, now); e != nil {
		return e
	}
	rows, e := orders.Rows(ctx, s.Store.DB, `SELECT x.order_id FROM xgift_purchases x JOIN orders o ON o.id=x.order_id WHERE x.state NOT IN('failed','succeeded') AND x.next_check<=$1 AND x.lease_until<=$1 AND o.mode='live' AND o.is_test=0 AND o.payment_status='paid' AND o.refund_hold=0 AND o.refunded_cents=0 AND o.redemption_blocked=0 AND o.status IN('session','processing') ORDER BY x.updated_at LIMIT 5`, now)
	if e != nil {
		return e
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r["order_id"].(string))
	}
	return worker.Each(ctx, ids, func(ctx context.Context, id string) error {
		e := s.Fulfill(ctx, id)
		if e != nil {
			_, _ = s.Store.DB.Exec(ctx, `UPDATE xgift_purchases SET updated_at=$1 WHERE order_id=$2 AND lease_until<=$1`, time.Now().UnixMilli(), id)
		}
		return e
	})
}
func (s *Service) Recover(ctx context.Context, id, token, note string, version int64) error {
	if utf8.RuneCountInString(strings.TrimSpace(note)) < 1 || utf8.RuneCountInString(note) > 500 {
		return fault.New(400, "请填写 1–500 字的处理说明。")
	}
	if _, e := s.Auth.Require(ctx, s.Store.DB, "admin", token); e != nil {
		return e
	}
	p, e := find(ctx, s.Store.DB, id)
	if e != nil {
		return e
	}
	if p.Backend != "platform" || p.State != "failed" || p.Failure == nil || (*p.Failure != "card_wallet_insufficient" && *p.Failure != "x_price_changed") || p.Supplier == nil || p.Receipt != nil || p.Until > time.Now().UnixMilli() {
		return orders.Conflict()
	}
	c := s.Clients[p.Backend]
	if !s.Checkout || c == nil || c.Fingerprint() != p.Hash {
		return orders.Conflict()
	}
	original, e := c.Query(ctx, merchant(p))
	if e != nil {
		return e
	}
	if e = validResult(original, p); e != nil {
		return e
	}
	if original.Status != "failed" || original.Failure == nil || *original.Failure != *p.Failure || original.Receipt != nil || (*original.Failure == "x_price_changed" && original.Stage != "preflight") {
		return fault.New(409, "原供货订单尚未确认未付款，禁止补发。")
	}
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	actor, e := s.Auth.Require(ctx, tx, "admin", token)
	if e != nil {
		return e
	}
	o, e := orders.Get(ctx, tx, id, nil, true)
	if e != nil {
		return e
	}
	fresh, e := find(ctx, tx, id)
	if e != nil {
		return e
	}
	if o.Version != version || o.Status != "failed" || o.Mode != "live" || o.Test != 0 || o.Payment != "paid" || o.Hold != 0 || o.Refunded != 0 || o.Blocked != 0 || fresh.Recovery != p.Recovery || fresh.State != p.State || fresh.Supplier == nil || *fresh.Supplier != *p.Supplier || fresh.Until > time.Now().UnixMilli() {
		return orders.Conflict()
	}
	attempt := p.Recovery + 1
	_, e = tx.Exec(ctx, `INSERT INTO xgift_recovery_attempts(id,order_id,attempt,merchant_order_no,previous_merchant_order_no,previous_supplier_order,previous_failure_code,expected_order_version,actor,note,created_at,previous_stage) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, security.ID(), id, attempt, id+":retry:"+strconv.FormatInt(attempt, 10), merchant(p), p.Supplier, p.Failure, version, "admin:"+actor.Subject, strings.TrimSpace(note), time.Now().UnixMilli(), original.Stage)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
