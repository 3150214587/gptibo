package recharge

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSubmissionRecoveryKeepsOriginalAuthorization(t *testing.T) {
	for _, scenario := range []string{"transport", "rate-limit", "retry-failed", "rejected", "long-backoff", "invalid-long-backoff", "cancelled", "frozen"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			c, _ := NewClient("https://supplier.example.test/v1", "tenant", "key", "secret")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			raw, key := `{"credential":{"mode":"session","session":"fixture-only"}}`, "recharge:original"
			calls, guards := 0, 0
			firstNonce := ""
			var failedAt time.Time
			c.HTTP.Transport = transport(func(r *http.Request) (*http.Response, error) {
				calls++
				body, _ := io.ReadAll(r.Body)
				if string(body) != raw || r.Header.Get("Idempotency-Key") != key || r.URL.Path != "/v1/redemptions" {
					t.Fatal("retry changed original authorization")
				}
				if calls > 1 {
					if time.Since(failedAt) < 15*time.Second {
						t.Fatal("retried before the 15-second wait elapsed")
					}
					if r.Header.Get("X-Nonce") == firstNonce {
						t.Fatal("signing nonce reused")
					}
					if scenario == "retry-failed" {
						return nil, errors.New("connection still interrupted")
					}
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"data":{"redemption_id":"original-task","status":"queued"}}`))}, nil
				}
				failedAt = time.Now()
				firstNonce = r.Header.Get("X-Nonce")
				if scenario == "cancelled" {
					cancel()
				}
				if scenario == "transport" || scenario == "retry-failed" || scenario == "cancelled" || scenario == "frozen" {
					return nil, errors.New("connection interrupted")
				}
				status, bodyText, delay := 429, `{"error":{"code":"rate_limited"}}`, "1"
				if scenario == "rejected" {
					status, bodyText = 400, `{"error":{"code":"session_invalid","retryable":false}}`
				}
				if scenario == "long-backoff" {
					delay = "16"
				}
				if scenario == "invalid-long-backoff" {
					bodyText, delay = "invalid response", "120"
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": {delay}}, Body: io.NopCloser(strings.NewReader(bodyText))}, nil
			})
			var out Redemption
			err := c.SubmitRedemption(ctx, raw, key, &out, func() error {
				guards++
				if scenario == "frozen" {
					return errors.New("refund now pending")
				}
				return nil
			})
			recovered := scenario == "transport" || scenario == "rate-limit"
			if recovered {
				if err != nil || calls != 2 || guards != 1 || out.ID != "original-task" {
					t.Fatal("original request not recovered", err, calls, guards)
				}
			} else if scenario == "retry-failed" {
				if err == nil || calls != 2 || guards != 1 {
					t.Fatal("retry limit not preserved", err, calls, guards)
				}
			} else if err == nil || calls != 1 {
				t.Fatal("unsafe replay", err, calls)
			}
		})
	}
}

func TestRetryAfter(t *testing.T) {
	if retryAfter("120") != 120*time.Second || retryAfter("bad") != 0 || retryAfter("-1") != 0 {
		t.Fatal("invalid retry delay")
	}
	delay := retryAfter(time.Now().Add(time.Minute).UTC().Format(http.TimeFormat))
	if delay < 58*time.Second || delay > time.Minute {
		t.Fatal("HTTP date backoff ignored", delay)
	}
}
