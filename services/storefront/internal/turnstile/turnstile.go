// Package turnstile verifies one-use Cloudflare challenges before order creation.
package turnstile

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"gptibo/storefront/internal/fault"
)

const Action = "order_create"

type Widget struct {
	SiteKey string   `json:"sitekey"`
	Secret  string   `json:"secret"`
	Hosts   []string `json:"hosts"`
}

type Service struct {
	mu       sync.Mutex
	filename string
	widgets  map[string]Widget
	client   *http.Client
	endpoint string
}

// NewFile reloads the server-managed registry without restarting checkout.
// A failed/partial file update retains the last validated snapshot; unknown
// hostnames still fail closed. The synchronizer never edits existing secrets.
func NewFile(filename string) (*Service, error) {
	raw, err := os.ReadFile(filename)
	if err != nil {
		return nil, errors.New("cannot read Turnstile configuration")
	}
	s, err := New(raw)
	if err == nil {
		s.filename = filename
	}
	return s, err
}

func (s *Service) widget(host string) (Widget, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.filename != "" {
		if raw, err := os.ReadFile(s.filename); err == nil {
			if fresh, err := New(raw); err == nil {
				s.widgets = fresh.widgets
			}
		}
	}
	w, ok := s.widgets[host]
	if !ok {
		return Widget{}, fault.New(503, "此商店的人机验证暂未就绪，请稍后重试。")
	}
	return w, nil
}

func New(raw []byte) (*Service, error) {
	var widgets []Widget
	if json.Unmarshal(raw, &widgets) != nil || len(widgets) == 0 {
		return nil, errors.New("invalid Turnstile widget configuration")
	}
	s := &Service{widgets: map[string]Widget{}, client: &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, endpoint: "https://challenges.cloudflare.com/turnstile/v0/siteverify"}
	for _, w := range widgets {
		if w.SiteKey == "" || w.Secret == "" || len(w.Hosts) == 0 {
			return nil, errors.New("Turnstile requires sitekey, secret and hostnames")
		}
		for _, host := range w.Hosts {
			u, err := url.Parse("https://" + host)
			if err != nil || host != strings.ToLower(host) || u.Hostname() != host || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(host, " *\t\r\n") {
				return nil, errors.New("Turnstile requires exact lowercase hostnames")
			}
			if _, exists := s.widgets[host]; exists {
				return nil, errors.New("duplicate Turnstile hostname")
			}
			s.widgets[host] = w
		}
	}
	return s, nil
}

func (s *Service) SiteKey(host string) (string, error) {
	w, err := s.widget(host)
	return w.SiteKey, err
}

func (s *Service) Verify(ctx context.Context, host, token string) error {
	return s.VerifyAction(ctx, host, token, Action)
}

func (s *Service) VerifyAction(ctx context.Context, host, token, action string) error {
	w, err := s.widget(host)
	if err != nil {
		return err
	}
	if len(token) == 0 || len(token) > 2048 {
		return fault.New(400, "请先完成人机验证，再继续操作。")
	}
	form := url.Values{"secret": {w.Secret}, "response": {token}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return unavailable()
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.client.Do(req)
	if err != nil {
		return unavailable()
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return unavailable()
	}
	var result struct {
		Success     bool      `json:"success"`
		Hostname    string    `json:"hostname"`
		Action      string    `json:"action"`
		ChallengeTS time.Time `json:"challenge_ts"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 16384)).Decode(&result) != nil {
		return unavailable()
	}
	if !result.Success || result.Hostname != host || result.Action != action || result.ChallengeTS.IsZero() || time.Since(result.ChallengeTS) > 5*time.Minute || time.Until(result.ChallengeTS) > time.Minute {
		return fault.New(400, "人机验证未通过或已过期，请重新验证。")
	}
	return nil
}

func unavailable() error {
	return fault.New(503, "人机验证服务暂不可用，请稍后重试。")
}
