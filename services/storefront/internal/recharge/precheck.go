package recharge

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/orders"
	"gptibo/storefront/internal/security"
)

type PrecheckResult struct {
	State      string  `json:"state"`
	Code       string  `json:"code"`
	Target     string  `json:"target"`
	EligibleAt *string `json:"eligibleAt"`
	CheckedAt  string  `json:"checkedAt"`
	Receipt    string  `json:"receipt,omitempty"`
}
type SessionChecker interface {
	Check(context.Context, Credential, string) (PrecheckResult, error)
}
type RemoteSessionChecker struct {
	URL, Secret string
	HTTP        *http.Client
}

func NewSessionChecker(endpoint, secret string) (*RemoteSessionChecker, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/api/service/precheck" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"))) || len(secret) < 32 {
		return nil, errors.New("invalid SESSION_PRECHECK_URL or SESSION_PRECHECK_SECRET")
	}
	return &RemoteSessionChecker{URL: endpoint, Secret: secret, HTTP: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (r *RemoteSessionChecker) Check(ctx context.Context, c Credential, plan string) (PrecheckResult, error) {
	var payload struct {
		AccessToken string `json:"accessToken"`
		Expires     string `json:"expires,omitempty"`
		AccountID   string `json:"accountId,omitempty"`
		Target      string `json:"target"`
	}
	payload.AccessToken = c.AccessToken
	payload.Target = plan
	if c.Mode == "session" {
		var s struct {
			AccessToken string `json:"accessToken"`
			Expires     string `json:"expires"`
			Account     struct {
				ID string `json:"id"`
			} `json:"account"`
		}
		if json.Unmarshal([]byte(c.Session), &s) != nil {
			return PrecheckResult{}, errors.New("invalid session")
		}
		payload.AccessToken = s.AccessToken
		payload.Expires = s.Expires
		payload.AccountID = s.Account.ID
	}
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.URL, bytes.NewReader(raw))
	if err != nil {
		return PrecheckResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.Secret)
	res, err := r.HTTP.Do(req)
	if err != nil {
		var networkErr net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &networkErr) && networkErr.Timeout()) {
			return PrecheckResult{State: "timeout", Code: "query_timeout", Target: plan}, nil
		}
		return PrecheckResult{State: "unknown", Code: "check_unavailable", Target: plan}, nil
	}
	defer res.Body.Close()
	if res.StatusCode == 429 {
		return PrecheckResult{State: "limited", Code: "rate_limited", Target: plan}, nil
	}
	if res.StatusCode != 200 {
		return PrecheckResult{State: "unknown", Code: "check_unavailable", Target: plan}, nil
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 8193))
	if err != nil || len(body) > 8192 {
		return PrecheckResult{}, errors.New("invalid precheck response")
	}
	var out PrecheckResult
	if json.Unmarshal(body, &out) != nil || out.Target != plan {
		return PrecheckResult{}, errors.New("invalid precheck response")
	}
	switch out.State {
	case "eligible", "wait", "invalid", "expired", "ineligible", "unknown", "limited", "timeout":
	default:
		return PrecheckResult{}, errors.New("invalid precheck state")
	}
	checked, err := time.Parse(time.RFC3339Nano, out.CheckedAt)
	if err != nil || time.Since(checked) > time.Minute || time.Until(checked) > 5*time.Second {
		return PrecheckResult{}, errors.New("stale precheck response")
	}
	if out.EligibleAt != nil {
		if _, err = time.Parse(time.RFC3339Nano, *out.EligibleAt); err != nil {
			return PrecheckResult{}, errors.New("invalid eligibility time")
		}
	}
	out.Receipt = "" // Only this server can issue a receipt bound to an order.
	return out, nil
}
func normalizeCredential(c Credential) (Credential, error) {
	if c.Mode == "session" && len(c.Session) > 0 && len(c.Session) <= 32000 {
		var s struct {
			User struct {
				Email string `json:"email"`
			} `json:"user"`
			Token string `json:"accessToken"`
		}
		if json.Unmarshal([]byte(c.Session), &s) != nil || s.User.Email == "" || s.Token == "" {
			return c, fault.New(400, "Session 内容不完整，请重新获取。")
		}
		c.AccessToken = ""
		return c, nil
	}
	if c.Mode == "access_token" && strings.TrimSpace(c.AccessToken) != "" && len(c.AccessToken) <= 32000 {
		c.Session = ""
		return c, nil
	}
	return c, fault.New(400, "Session 内容无效，请重新获取。")
}

// Called only after the HTTP handler has checked order ownership. No order writes.
func (s *Service) Precheck(ctx context.Context, id, attempt string, c Credential) (PrecheckResult, error) {
	if s.Checker == nil {
		return PrecheckResult{}, fault.New(503, "账号检测暂不可用，请稍后重试。")
	}
	c, err := normalizeCredential(c)
	if err != nil {
		return PrecheckResult{}, err
	}
	o, err := orders.Get(ctx, s.Store.DB, id, nil, false)
	if err != nil {
		return PrecheckResult{}, err
	}
	r, err := Find(ctx, s.Store.DB, id)
	if err != nil {
		return PrecheckResult{}, err
	}
	if o.Blocked != 0 || o.Hold != 0 || o.Refunded != 0 || r.Hold != 0 || r.Remote == nil || r.Payment != "paid" || o.Payment != "paid" || o.Status == "success" {
		return PrecheckResult{}, orders.Conflict()
	}
	previous, err := latest(ctx, s.Store.DB, id)
	if err != nil {
		return PrecheckResult{}, err
	}
	if previous != nil && previous.ID == attempt {
		raw := Raw(struct {
			Mode       string     `json:"mode"`
			Order      string     `json:"order_id"`
			Credential Credential `json:"credential"`
			Confirmed  bool       `json:"customer_confirmed_email"`
		}{"auto_recharge", *r.Remote, c, true})
		if expired(*previous) || previous.Hash != security.Hash(raw) {
			return PrecheckResult{}, orders.Conflict()
		}
		return PrecheckResult{State: "recovery", Code: "original_submission", Target: o.Plan}, nil
	}
	if previous != nil && previous.Status != "rejected" && previous.Fallback == 0 {
		return PrecheckResult{}, orders.Conflict()
	}
	if err = credentialExpiry(c, time.Now()); err != nil {
		return PrecheckResult{State: "expired", Code: "locally_expired", Target: o.Plan}, nil
	}
	out, err := checkForSubmission(ctx, s.Checker, c, o.Plan)
	if err != nil {
		return PrecheckResult{}, fault.New(503, "账号检测暂不可用，请稍后重试。")
	}
	out.Receipt = ""
	if (out.State == "eligible" || out.State == "deferred") && out.Target == o.Plan {
		out.Receipt, err = s.issuePrecheck(id, o.Plan, attempt, c, time.Now())
	}
	return out, err
}

// Only the server may authorize upstream review. Never turn an explicit denial
// or an invalid checker response into permission to submit.
func checkForSubmission(ctx context.Context, checker SessionChecker, c Credential, plan string) (PrecheckResult, error) {
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := checker.Check(checkCtx, c, plan)
	if ctx.Err() != nil {
		return PrecheckResult{}, ctx.Err()
	}
	if errors.Is(checkCtx.Err(), context.DeadlineExceeded) && out.State != "invalid" && out.State != "expired" && out.State != "wait" && out.State != "ineligible" {
		out, err = PrecheckResult{State: "timeout", Code: "query_timeout", Target: plan}, nil
	}
	if err != nil {
		return PrecheckResult{}, err
	}
	if out.Target != plan {
		return PrecheckResult{}, errors.New("invalid precheck target")
	}
	if out.State == "limited" || out.State == "timeout" || (out.State == "unknown" && (out.Code == "business_evidence_missing" || out.Code == "insufficient_evidence" || out.Code == "account_id_missing")) {
		out.State = "deferred"
		out.EligibleAt = nil
	}
	return out, nil
}

type precheckTicket struct {
	Order, Plan, Attempt, Credential string
	Expires                          int64
}

func (s *Service) checkMAC(value string) (string, error) {
	key, err := hex.DecodeString(s.CredentialSecret)
	if err != nil || len(key) != 32 {
		return "", fault.New(503, "账号检测暂不可用，请稍后重试。")
	}
	h := hmac.New(sha256.New, key)
	h.Write([]byte("storefront:session-precheck:v1\x00" + value))
	return hex.EncodeToString(h.Sum(nil)), nil
}
func (s *Service) issuePrecheck(id, plan, attempt string, c Credential, now time.Time) (string, error) {
	binding, err := s.checkMAC("credential:" + Raw(c))
	if err != nil {
		return "", err
	}
	raw, _ := json.Marshal(precheckTicket{id, plan, attempt, binding, now.Add(2 * time.Minute).Unix()})
	payload := base64.RawURLEncoding.EncodeToString(raw)
	mac, err := s.checkMAC("receipt:" + payload)
	return payload + "." + mac, err
}
func (s *Service) verifyPrecheck(receipt, id, plan, attempt string, c Credential, now time.Time) error {
	denied := fault.New(409, "账号校验已失效，请重新检测后提交。")
	parts := strings.Split(receipt, ".")
	if len(parts) != 2 || len(receipt) > 2048 {
		return denied
	}
	mac, err := s.checkMAC("receipt:" + parts[0])
	if err != nil {
		return err
	}
	if !hmac.Equal([]byte(mac), []byte(parts[1])) {
		return denied
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return denied
	}
	var t precheckTicket
	if json.Unmarshal(raw, &t) != nil {
		return denied
	}
	binding, err := s.checkMAC("credential:" + Raw(c))
	if err != nil {
		return err
	}
	if t.Order != id || t.Plan != plan || t.Attempt != attempt || !hmac.Equal([]byte(t.Credential), []byte(binding)) || t.Expires <= now.Unix() || t.Expires > now.Add(2*time.Minute).Unix() {
		return denied
	}
	return nil
}
