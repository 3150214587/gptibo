package recharge

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/orders"
	"gptibo/storefront/internal/rechargecopy"
	"gptibo/storefront/internal/security"
)

type Attempt struct {
	Message  string  `json:"public_message"`
	ID       string  `json:"id"`
	Order    string  `json:"order_id"`
	Number   int64   `json:"attempt_no"`
	Hash     string  `json:"request_hash"`
	Mode     string  `json:"credential_mode"`
	Remote   *string `json:"remote_id"`
	Status   string  `json:"status"`
	Fallback int     `json:"fallback"`
	Created  int64   `json:"created_at"`
	Stage    *string `json:"progress_stage"`
	Version  int64   `json:"progress_version"`
	Updated  *int64  `json:"progress_updated_at"`
}

func latest(ctx context.Context, q orders.Query, id string) (*Attempt, error) {
	var raw []byte
	var a Attempt
	e := q.QueryRow(ctx, `SELECT to_jsonb(a) FROM recharge_redemptions a WHERE order_id=$1 ORDER BY attempt_no DESC LIMIT 1`, id).Scan(&raw)
	if e == pgx.ErrNoRows {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	e = json.Unmarshal(raw, &a)
	return &a, e
}
func terminal(s string) bool {
	return slices.Contains([]string{"succeeded", "failed", "cancelled", "rejected"}, s)
}
func expired(a Attempt) bool {
	return a.Status == "unknown" && time.Now().UnixMilli()-a.Created >= 7*86400000
}
func publicCode(v *string, allowed map[string]string) *string {
	if v != nil && allowed[*v] != "" {
		return v
	}
	return nil
}
func optionalTime(v *string) (*int64, error) {
	if v == nil {
		return nil, nil
	}
	t, e := time.Parse(time.RFC3339Nano, *v)
	n := t.UnixMilli()
	return &n, e
}
func (s *Service) applyRedemption(ctx context.Context, r Row, a Attempt, v Redemption) error {
	if !remoteID.MatchString(v.ID) || r.Remote == nil || v.Order != *r.Remote || (a.Remote != nil && *a.Remote != v.ID) || !slices.Contains([]string{"queued", "running", "succeeded", "failed", "cancelled"}, v.Status) || v.Fallback == nil || (v.Attempt != nil && *v.Attempt != a.Number) {
		return errors.New("redemption identity mismatch")
	}
	version := int64(0)
	if v.Version != nil {
		version = *v.Version
	}
	if version < 0 || version > 9007199254740991 {
		return errors.New("invalid redemption version")
	}
	result := publicCode(v.Result, resultStages)
	resultStage := ""
	if result != nil {
		resultStage = resultStages[*result]
	}
	if resultStage != "" && ((v.Status == "succeeded" && resultStage != "completed") || (slices.Contains([]string{"failed", "cancelled"}, v.Status) && !slices.Contains([]string{"failed", "cancelled"}, resultStage))) {
		return errors.New("redemption result mismatch")
	}
	var stage *string
	if v.Stage != nil && slices.Contains([]string{"queued", "logging_in", "preparing_funds", "awaiting_card", "dispatching", "processing", "requires_action", "confirming", "review", "upgrading", "completed", "failed", "cancelled"}, *v.Stage) {
		stage = v.Stage
	} else if resultStage != "" && !(slices.Contains([]string{"queued", "running"}, v.Status) && slices.Contains([]string{"completed", "failed", "cancelled"}, resultStage)) {
		stage = &resultStage
	}
	if terminal(v.Status) {
		value := v.Status
		if value == "succeeded" {
			value = "completed"
		}
		stage = &value
	}
	updated, e := optionalTime(v.Updated)
	if e != nil {
		return errors.New("invalid redemption progress time")
	}
	finished, e := optionalTime(v.Finished)
	if e != nil {
		return errors.New("invalid redemption finish time")
	}
	failure := publicCode(v.Failure, failureMessages)
	if failure == nil && v.Failure != nil {
		other := "other"
		failure = &other
	}
	category := publicCode(v.Category, categoryMessages)
	var masked *string
	if v.Email != nil && len(*v.Email) <= 320 && regexp.MustCompile(`^[^@\s]*\*[^@\s]*@[^@\s]+$`).MatchString(*v.Email) {
		masked = v.Email
	}
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	o, e := orders.Get(ctx, tx, r.OrderID, nil, true)
	if e != nil {
		return e
	}
	r, e = s.fence(ctx, tx, r)
	if e != nil {
		return e
	}
	current, e := latest(ctx, tx, o.ID)
	if e != nil {
		return e
	}
	if current == nil || current.ID != a.ID {
		return orders.Conflict()
	}
	a = *current
	if version < a.Version {
		return tx.Commit(ctx)
	}
	if a.Status != "unknown" && a.Status != "queued" && a.Status != "running" && a.Status != v.Status && !(slices.Contains([]string{"failed", "cancelled"}, a.Status) && v.Status == "succeeded" && version > a.Version) {
		return tx.Commit(ctx)
	}
	if !(version > a.Version || (version == 0 && (a.Stage == nil || v.Version == nil)) || (terminal(v.Status) && stage != nil && (*stage == "failed" || *stage == "cancelled" || *stage == "completed"))) {
		stage = a.Stage
		updated = a.Updated
	}
	message := ""
	if v.Message != nil {
		message = rechargecopy.PublicMessage(*v.Message)
	}
	if version > 0 && version == a.Version && a.Message != "" {
		message = a.Message
	}
	recovery := ""
	if v.Recovery == "retry" || v.Recovery == "refund" {
		recovery = v.Recovery
	}
	fallback := 0
	if *v.Fallback && v.Retry && v.Next == "resubmit" && v.Recovery != "refund" && slices.Contains([]string{"failed", "cancelled"}, v.Status) {
		fallback = 1
	}
	_, e = tx.Exec(ctx, `UPDATE recharge_redemptions SET remote_id=$1,status=$2,fallback=$3,failure_code=$4,error_category=$5,result_code=$6,account_email_masked=$7,finished_at=$8,progress_stage=$9,progress_version=$10,progress_updated_at=$11,public_message=$13,recovery_action=$14 WHERE id=$12`, v.ID, v.Status, fallback, failure, category, result, masked, finished, stage, version, updated, a.ID, message, recovery)
	if e != nil {
		return e
	}
	status, note := "processing", "充值任务已受理，等待完成"
	if v.Status == "succeeded" {
		status, note = "success", "充值服务已确认完成"
	} else if v.Status == "failed" || v.Status == "cancelled" {
		failureCode, categoryCode := "", ""
		if category != nil {
			categoryCode = *category
		}
		if failure != nil {
			failureCode = *failure
		}
		status, note = "failed", rechargecopy.FailureMessage(failureCode, categoryCode)
	}
	// A terminal failure must stop showing as processing even during refunds or
	// an administrative block. These flags still prevent submission and success settlement.
	canUpdate := o.Payment == "paid" && o.Blocked == 0 && o.Hold == 0 && o.Refunded == 0 && r.Hold == 0
	if status == "failed" && (o.Status == "processing" || o.Status == "session") {
		canUpdate = true
	}
	if canUpdate && o.Status != status && o.Status != "success" && o.Status != "refunded" {
		next := o
		next.Status = status
		now := time.Now().UnixMilli()
		if next.Started == nil {
			next.Started = &now
		}
		if _, e = orders.Change(ctx, tx, o, next, "system", "recharge-status", note); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}

type Credential struct {
	Mode        string `json:"mode"`
	Session     string `json:"session,omitempty"`
	AccessToken string `json:"access_token,omitempty"`
}
type Input struct {
	PrecheckReceipt string     `json:"precheckReceipt"`
	Action          string     `json:"action"`
	Key             string     `json:"key"`
	Confirmed       bool       `json:"customerConfirmedEmail"`
	Credential      Credential `json:"credential"`
}

func (s *Service) Action(ctx context.Context, id string, in Input) error {
	if in.Action == "poll" {
		return s.Sync(ctx, id, false)
	}
	if in.Action == "query" {
		return s.Sync(ctx, id, true)
	}
	if in.Action != "submit" || !in.Confirmed || !security.ValidID(in.Key) {
		return fault.New(400, "请确认账号并重新提交。")
	}
	c := in.Credential
	if c.Mode == "session" && len(c.Session) > 0 && len(c.Session) <= 32000 && utf8.ValidString(c.Session) {
		var session struct {
			User struct {
				Email string `json:"email"`
			} `json:"user"`
			Token string `json:"accessToken"`
		}
		if json.Unmarshal([]byte(c.Session), &session) != nil || session.User.Email == "" || session.Token == "" {
			return fault.New(400, "Session 中缺少账号邮箱或 Access Token。")
		}
		c.AccessToken = ""
	} else if c.Mode == "access_token" && strings.TrimSpace(c.AccessToken) != "" && len(c.AccessToken) <= 32000 && utf8.ValidString(c.AccessToken) {
		c.Session = ""
	} else {
		return fault.New(400, "请选择 Session 或 Access Token，并填写有效凭据。")
	}
	return s.locked(ctx, id, func(r Row) error { return s.submit(ctx, id, in, c, r, "customer") })
}

func (s *Service) submit(ctx context.Context, id string, in Input, c Credential, r Row, actor string) error {
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
	if o.Blocked != 0 || o.Hold != 0 || o.Refunded != 0 || r.Hold != 0 || r.Remote == nil || r.Payment != "paid" || o.Payment != "paid" || o.Status == "success" {
		return orders.Conflict()
	}
	raw := Raw(struct {
		Mode       string     `json:"mode"`
		Order      string     `json:"order_id"`
		Credential Credential `json:"credential"`
		Confirmed  bool       `json:"customer_confirmed_email"`
	}{"auto_recharge", *r.Remote, c, true})
	digest := security.Hash(raw)
	a, e := latest(ctx, tx, id)
	if e != nil {
		return e
	}
	if a != nil && a.ID == in.Key {
		if expired(*a) || a.Hash != digest {
			return fault.New(409, "原提交编号与资料不一致或已超过幂等保护期，请核对原任务。")
		}
		if a.Remote != nil || terminal(a.Status) {
			return tx.Commit(ctx)
		}
	} else {
		if a != nil && a.Status != "rejected" && a.Fallback == 0 {
			return fault.New(409, "原充值结果尚未确认，请查询原任务，勿重复提交。")
		}
		// Preserve same-key recovery above; only new attempts require fresh input.
		if e = credentialExpiry(c, time.Now()); e != nil {
			return e
		}
		if s.Checker != nil {
			if e = s.verifyPrecheck(in.PrecheckReceipt, id, o.Plan, in.Key, c, time.Now()); e != nil {
				return e
			}
		}
		number := int64(1)
		if a != nil {
			number = a.Number + 1
		}
		now := time.Now().UnixMilli()
		_, e = tx.Exec(ctx, `INSERT INTO recharge_redemptions(id,order_id,attempt_no,request_hash,credential_mode,created_at) VALUES($1,$2,$3,$4,$5,$6)`, in.Key, id, number, digest, c.Mode, now)
		if e != nil {
			return e
		}
		next := o
		next.Status = "session"
		next.Started = nil
		note := "客户已授权提交充值资料，等待充值服务确认受理"
		action := "recharge-submit"
		if actor != "customer" {
			action, note = "recharge-resend", "管理员使用已留存的充值资料自动补发，等待上游确认"
		}
		if _, e = orders.Change(ctx, tx, o, next, actor, action, note); e != nil {
			return e
		}
		a = &Attempt{ID: in.Key, Order: id, Number: number, Hash: digest, Mode: c.Mode, Status: "unknown", Created: now}
	}
	if e = s.saveCredential(ctx, tx, id, a.ID, c); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	var remote Redemption
	e = s.Client.SubmitRedemption(ctx, raw, "recharge:"+a.ID, &remote, func() error {
		var allowed bool
		err := s.Store.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM orders o JOIN recharge_orders t ON t.order_id=o.id JOIN recharge_redemptions a ON a.order_id=o.id WHERE o.id=$1 AND o.payment_status='paid' AND o.status<>'success' AND o.refund_hold=0 AND o.refunded_cents=0 AND o.redemption_blocked=0 AND t.financial_hold=0 AND t.payment_status='paid' AND t.lease_token=$2 AND t.lease_until>$3 AND a.id=$4 AND a.status='unknown' AND a.remote_id IS NULL AND NOT EXISTS(SELECT 1 FROM recharge_redemptions newer WHERE newer.order_id=o.id AND newer.attempt_no>a.attempt_no))`, id, r.Lease, time.Now().UnixMilli(), a.ID).Scan(&allowed)
		if err != nil {
			return err
		}
		if !allowed {
			return orders.Conflict()
		}
		return nil
	})
	if e == nil {
		if e = s.applyRedemption(ctx, r, *a, remote); e != nil {
			slog.WarnContext(ctx, "recharge response not applied", "order_id", id, "attempt_id", a.ID)
		}
		return e
	}
	var api *APIError
	if errors.As(e, &api) {
		slog.WarnContext(ctx, "recharge submission unconfirmed", "order_id", id, "attempt_id", a.ID, "code", api.Code, "upstream_status", api.HTTPStatus, "request_id", api.RequestID, "retryable", api.Retryable)
	}
	if errors.As(e, &api) && !api.Retryable && slices.Contains([]string{"invalid_request", "session_invalid", "invalid_credential"}, api.Code) {
		tx, err := s.Store.DB.Begin(ctx)
		if err != nil {
			return e
		}
		defer tx.Rollback(ctx)
		o, err := orders.Get(ctx, tx, id, nil, true)
		if err != nil {
			return e
		}
		if _, err = s.fence(ctx, tx, r); err != nil {
			return e
		}
		tag, err := tx.Exec(ctx, `UPDATE recharge_redemptions SET status='rejected',failure_code='request_rejected' WHERE id=$1 AND status='unknown'`, a.ID)
		if err != nil {
			return e
		}
		if tag.RowsAffected() == 1 && o.Hold == 0 && o.Refunded == 0 && (o.Status == "session" || o.Status == "processing") {
			next := o
			next.Status = "failed"
			if _, err = orders.Change(ctx, tx, o, next, "system", "recharge-rejected", failureMessages["request_rejected"]); err != nil {
				return e
			}
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
	}
	return e
}
