package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"gptibo/platform/internal/commerce"
	"gptibo/platform/internal/providers"
	"gptibo/platform/internal/security"
	"gptibo/platform/internal/store"
	"html"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"
)

type Worker struct {
	Store    *store.Store
	Engine   *providers.Engine
	Commerce *commerce.Service
	ID       string
}

func (w *Worker) Run(ctx context.Context) {
	notificationCtx, stopNotifications := context.WithCancel(ctx)
	notificationDone := make(chan struct{})
	go func() { defer close(notificationDone); w.notifications(notificationCtx) }()
	defer func() { stopNotifications(); <-notificationDone }()
	alertCtx, stopAlerts := context.WithCancel(ctx)
	alertDone := make(chan struct{})
	go func() { defer close(alertDone); w.manualAlerts(alertCtx) }()
	defer func() { stopAlerts(); <-alertDone }()
	manualCtx, stopManual := context.WithCancel(ctx)
	manualDone := make(chan struct{})
	go func() {
		defer close(manualDone)
		timer := time.NewTicker(3 * time.Second)
		defer timer.Stop()
		for {
			if _, err := w.orderLane(manualCtx, true); err != nil && manualCtx.Err() == nil {
				slog.Error("manual payment worker database failure")
			}
			select {
			case <-manualCtx.Done():
				return
			case <-timer.C:
			}
		}
	}()
	defer func() { stopManual(); <-manualDone }()
	// Long provider calls must not stop liveness reporting.
	heartbeatCtx, stopHeartbeat := context.WithCancel(ctx)
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		timer := time.NewTicker(10 * time.Second)
		defer timer.Stop()
		for {
			_, err := w.Store.DB.Exec(heartbeatCtx, "INSERT INTO worker_heartbeats(id) VALUES($1) ON CONFLICT(id) DO UPDATE SET updated_at=now()", w.ID)
			if err != nil && ctx.Err() == nil {
				slog.Error("worker heartbeat failed")
			}
			select {
			case <-heartbeatCtx.Done():
				return
			case <-timer.C:
			}
		}
	}()
	defer func() { stopHeartbeat(); <-heartbeatDone }()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	w.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.tick(ctx)
		}
	}
}
func (w *Worker) tick(ctx context.Context) {
	_, e := w.Store.DB.Exec(ctx, "INSERT INTO worker_heartbeats(id) VALUES($1) ON CONFLICT(id) DO UPDATE SET updated_at=now()", w.ID)
	if e != nil {
		slog.Error("worker heartbeat failed")
		return
	}
	for i := 0; i < 8; i++ {
		worked, e := w.order(ctx)
		if e != nil {
			slog.Error("order worker database failure")
		}
		if !worked {
			break
		}
	}
	if w.Engine != nil {
		if _, err := w.Engine.MaintainCards(ctx); err != nil {
			slog.Error("card maintenance database failure")
		}
	}
	_, _ = w.Store.DB.Exec(ctx, "DELETE FROM nonces WHERE expires_at<now(); DELETE FROM sessions WHERE expires_at<now(); DELETE FROM login_limits WHERE reset_at<now()-interval '1 hour'")
}

func (w *Worker) notifications(ctx context.Context) {
	timer := time.NewTicker(3 * time.Second)
	defer timer.Stop()
	for {
		for i := 0; i < 8 && ctx.Err() == nil; i++ {
			worked, err := w.outbox(ctx)
			if err != nil && ctx.Err() == nil {
				slog.Error("notification worker database failure")
			}
			if !worked {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}
func (w *Worker) order(ctx context.Context) (bool, error) {
	return w.orderLane(ctx, false)
}

func (w *Worker) orderLane(ctx context.Context, manual bool) (bool, error) {
	token := security.Token()
	var o commerce.Order
	var config, credential, job string
	e := w.Store.Tx(ctx, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, "WITH candidate AS (SELECT id FROM orders WHERE status IN ('queued','running','unknown') AND (manual_payment_at IS NOT NULL)=$2 AND next_at<=now() AND (lease_until IS NULL OR lease_until<now()) ORDER BY next_at FOR UPDATE SKIP LOCKED LIMIT 1) UPDATE orders o SET lease_token=$1,lease_until=now()+interval '5 minutes' FROM candidate c WHERE o.id=c.id RETURNING o.id,o.user_id,o.merchant_order_no,o.product_code,o.business,o.supply_minor,o.status,o.recipient,o.account_label,o.stage,o.failure_code,o.receipt,o.created_at,o.updated_at,o.config_cipher,o.credential_cipher,o.job_cipher", token, manual)
		return row.Scan(&o.ID, &o.UserID, &o.MerchantOrderNo, &o.ProductCode, &o.Business, &o.SupplyMinor, &o.Status, &o.Recipient, &o.AccountLabel, &o.Stage, &o.FailureCode, &o.Receipt, &o.CreatedAt, &o.UpdatedAt, &config, &credential, &job)
	})
	if errors.Is(e, pgx.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	taskCtx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	result := w.Engine.Execute(taskCtx, o, token, config, credential, job)
	if result.Receipt != "" {
		result.Receipt = "rcpt_" + security.Hash(o.ID + ":" + result.Receipt)[:24]
	}
	return true, w.Commerce.Finish(ctx, o.ID, token, result.Status, result.Stage, result.Failure, result.Receipt)
}

type notification struct {
	URL    string          `json:"url"`
	Secret string          `json:"secret"`
	Email  string          `json:"email"`
	Event  json.RawMessage `json:"event"`
}

func (w *Worker) outbox(ctx context.Context) (bool, error) {
	token := security.Token()
	var id, kind, cipher string
	var attempts int
	e := w.Store.Tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, "WITH candidate AS (SELECT id FROM outbox WHERE status='pending' AND next_at<=now() AND (lease_until IS NULL OR lease_until<now()) ORDER BY next_at FOR UPDATE SKIP LOCKED LIMIT 1) UPDATE outbox o SET lease_token=$1,lease_until=now()+interval '60 seconds' FROM candidate c WHERE o.id=c.id RETURNING o.id,o.kind,o.payload_cipher,o.attempts", token).Scan(&id, &kind, &cipher, &attempts)
	})
	if errors.Is(e, pgx.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	b, e := w.Store.Vault.Open("outbox:"+id, cipher)
	var n notification
	if e == nil {
		e = json.Unmarshal(b, &n)
	}
	if e == nil {
		if kind == "invitation" {
			var usable bool
			u, parseErr := url.Parse(n.URL)
			if parseErr == nil {
				e = w.Store.DB.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM invitations i JOIN users u ON u.id=i.user_id WHERE i.token_hash=$1 AND i.used_at IS NULL AND i.expires_at>now() AND u.email=$2 AND u.status IN ('invited','active'))", security.Hash(u.Fragment), n.Email).Scan(&usable)
			}
			if parseErr != nil || e == nil && !usable {
				e = errors.New("invitation_expired")
			}
			if e == nil {
				e = sendMail(ctx, id, n)
			}
		} else {
			e = sendWebhook(ctx, id, n)
		}
	}
	status, lastError := "sent", ""
	if e != nil {
		status = "pending"
		lastError = "delivery_failed"
		if attempts >= 9 {
			status = "dead"
		}
		if e.Error() == "email_not_configured" {
			lastError = "email_not_configured"
		}
		for _, code := range []string{"mail_unauthorized", "mail_sender_rejected", "mail_rate_limited", "mail_invalid_request", "mail_service_unavailable", "mail_unconfirmed"} {
			if e.Error() == code {
				lastError = code
			}
		}
		if e.Error() == "invitation_expired" {
			status = "dead"
			lastError = "invitation_expired"
		}
	}
	delay := time.Duration(1<<min(attempts, 10)) * 30 * time.Second
	attemptDelta := 1
	if lastError == "email_not_configured" {
		// Configuration absence is not a delivery attempt. Keep the invitation recoverable.
		status, attemptDelta, delay = "pending", 0, time.Minute
	}
	_, e = w.Store.DB.Exec(ctx, "UPDATE outbox SET status=$3,attempts=attempts+$6,last_error=$4,next_at=$5,lease_token='',lease_until=NULL WHERE id=$1 AND lease_token=$2 AND status='pending'", id, token, status, lastError, time.Now().Add(delay), attemptDelta)
	return true, e
}
func sendMail(ctx context.Context, id string, n notification) error {
	return sendMailWithClient(ctx, id, n, &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }})
}
func sendMailWithClient(ctx context.Context, id string, n notification, client *http.Client) error {
	key, from := os.Getenv("RESEND_API_KEY"), os.Getenv("MAIL_FROM")
	if key == "" || from == "" {
		return errors.New("email_not_configured")
	}
	body, _ := json.Marshal(map[string]any{"from": from, "to": []string{n.Email}, "subject": "GPTibo 渠道开户邀请", "html": "<h2>欢迎加入 GPTibo 供货平台</h2><p>管理员已为你开通渠道账户。请在 48 小时内完成账户设置。</p><p><a href=\"" + html.EscapeString(n.URL) + "\">设置账户密码</a></p><p>如果你没有申请此账户，请忽略这封邮件。</p>"})
	req, e := http.NewRequestWithContext(ctx, "POST", "https://api.resend.com/emails", bytes.NewReader(body))
	if e != nil {
		return e
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", id)
	resp, e := client.Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return errors.New("mail_unauthorized")
	case http.StatusForbidden:
		return errors.New("mail_sender_rejected")
	case http.StatusTooManyRequests:
		return errors.New("mail_rate_limited")
	}
	if resp.StatusCode >= 500 {
		return errors.New("mail_service_unavailable")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New("mail_invalid_request")
	}
	var accepted struct {
		ID string `json:"id"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 65536)).Decode(&accepted) != nil || accepted.ID == "" {
		return errors.New("mail_unconfirmed")
	}
	return nil
}

// PublicIP is shared by webhook and card-provider transports.
func PublicIP(ip net.IP) bool { return security.PublicIP(ip) }
func sendWebhook(ctx context.Context, id string, n notification) error {
	u, e := url.Parse(n.URL)
	if e != nil || u.Scheme != "https" || u.User != nil {
		return errors.New("invalid_webhook")
	}
	ips, e := net.DefaultResolver.LookupIP(ctx, "ip", u.Hostname())
	if e != nil || len(ips) == 0 {
		return errors.New("dns_failed")
	}
	for _, ip := range ips {
		if !PublicIP(ip) {
			return errors.New("private_webhook")
		}
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	pinned := net.JoinHostPort(ips[0].String(), port)
	transport := &http.Transport{DialContext: func(c context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(c, "tcp", pinned)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, e := http.NewRequestWithContext(ctx, "POST", n.URL, bytes.NewReader(n.Event))
	if e != nil {
		return e
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Event-Id", id)
	req.Header.Set("X-Timestamp", ts)
	req.Header.Set("X-Signature", security.HMAC(n.Secret, ts+"."+id+"."+string(n.Event)))
	resp, e := client.Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 65536))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New("webhook_failed")
	}
	return nil
}
