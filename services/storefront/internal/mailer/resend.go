package mailer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"time"
	"unicode"
)

type Resend struct {
	key, from string
	client    *http.Client
	Origin    string
}

func NewResend(key, from string) (*Resend, error) {
	if key == "" {
		return nil, errors.New("Resend key required")
	}
	if _, e := mail.ParseAddress(from); e != nil {
		return nil, errors.New("invalid mail sender")
	}
	return &Resend{key: key, from: from, client: &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, Origin: "http://127.0.0.1:8093"}, nil
}
func (r *Resend) SendCode(ctx context.Context, role, to, code, id string) error {
	labels := map[string]string{"admin": "admin-login", "partner": "partner-login", "customer": "order-recovery"}
	label, ok := labels[role]
	if !ok {
		return errors.New("unsupported email kind")
	}
	message, e := BrandedTemplate(label, ContextBrand(ctx, r.Origin), map[string]string{"VERIFICATION_CODE": code})
	if e != nil {
		return e
	}
	_, e = r.SendMessage(ctx, to, message, "storefront-auth/"+id)
	return e
}
func (r *Resend) SendInvitation(ctx context.Context, to, origin, id string) error {
	brand := ContextBrand(ctx, r.Origin)
	brand.WorkspaceURL = strings.TrimRight(origin, "/") + "/join"
	message, e := BrandedTemplate("partner-welcome", brand, nil)
	if e != nil {
		return e
	}
	_, e = r.SendMessage(ctx, to, message, "storefront-invitation/"+id)
	return e
}
func (r *Resend) SendMessage(ctx context.Context, to string, message Message, idempotencyKey string) (string, error) {
	body, e := r.Freeze(to, message)
	if e != nil {
		return "", e
	}
	return r.SendFrozen(ctx, body, idempotencyKey)
}

type Sender interface {
	Freeze(string, Message) (string, error)
	SendFrozen(context.Context, string, string) (string, error)
}
type Frozen struct {
	From string   `json:"from"`
	To   []string `json:"to"`
	Message
}

func (r *Resend) Freeze(to string, message Message) (string, error) {
	if _, e := mail.ParseAddress(to); e != nil {
		return "", errors.New("invalid recipient")
	}
	from := r.from
	if message.SenderName != "" {
		if len(message.SenderName) > 512 || strings.ContainsFunc(message.SenderName, unicode.IsControl) {
			return "", errors.New("invalid mail display name")
		}
		address, _ := mail.ParseAddress(r.from)
		address.Name = message.SenderName
		from = address.String()
	}
	body, e := json.Marshal(Frozen{From: from, To: []string{to}, Message: message})
	return string(body), e
}

func (r *Resend) SendFrozen(ctx context.Context, body, idempotencyKey string) (string, error) {
	var saved Frozen
	if json.Unmarshal([]byte(body), &saved) != nil || !SameSender(saved.From, r.from) || len(saved.To) != 1 {
		return "", errors.New("restore original mail sender before retry")
	}
	if _, e := mail.ParseAddress(saved.To[0]); e != nil {
		return "", errors.New("invalid saved recipient")
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.resend.com/emails", bytes.NewReader([]byte(body)))
	if e != nil {
		return "", e
	}
	req.Header.Set("Authorization", "Bearer "+r.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", idempotencyKey)
	res, e := r.client.Do(req)
	if e != nil {
		return "", errors.New("mail transport failed")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", errors.New("mail provider rejected request")
	}
	data, e := io.ReadAll(io.LimitReader(res.Body, 16385))
	if e != nil || len(data) > 16384 {
		return "", errors.New("invalid mail response")
	}
	var sent struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(data, &sent) != nil || !regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`).MatchString(sent.ID) {
		return "", errors.New("mail response missing message id")
	}
	return sent.ID, nil
}

// Display names may vary per store, but the verified sending mailbox cannot change.
func SameSender(saved, configured string) bool {
	a, e := mail.ParseAddress(saved)
	if e != nil || strings.ContainsFunc(saved, unicode.IsControl) {
		return false
	}
	b, e := mail.ParseAddress(configured)
	return e == nil && a.Address == b.Address
}
