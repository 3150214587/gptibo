package recharge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPrecheckReceiptBindingAndExpiry(t *testing.T) {
	s := &Service{CredentialSecret: strings.Repeat("ab", 32)}
	now := time.Now().Truncate(time.Second)
	c := Credential{Mode: "session", Session: `{"accessToken":"private-fixture"}`}
	receipt, err := s.issuePrecheck("order", "plus", "attempt", c, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.verifyPrecheck(receipt, "order", "plus", "attempt", c, now); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id, plan, key string
		c             Credential
		at            time.Time
	}{
		{"different", "plus", "attempt", c, now}, {"order", "pro-5x", "attempt", c, now}, {"order", "plus", "different", c, now}, {"order", "plus", "attempt", Credential{Mode: "access_token", AccessToken: "changed"}, now}, {"order", "plus", "attempt", c, now.Add(2 * time.Minute)},
	} {
		if s.verifyPrecheck(receipt, tc.id, tc.plan, tc.key, tc.c, tc.at) == nil {
			t.Fatal("unbound receipt accepted")
		}
	}
	if s.verifyPrecheck(receipt+"x", "order", "plus", "attempt", c, now) == nil {
		t.Fatal("tamper accepted")
	}
	s.CredentialSecret = strings.Repeat("cd", 32)
	if s.verifyPrecheck(receipt, "order", "plus", "attempt", c, now) == nil {
		t.Fatal("rotation accepted")
	}
	if strings.Contains(receipt, "private-fixture") {
		t.Fatal("credential disclosed")
	}
}
func TestRemoteSessionCheckerMinimizesPayloadAndRejectsBadResponses(t *testing.T) {
	secret := strings.Repeat("x", 32)
	status := 200
	state := "eligible"
	target := "plus"
	date := time.Now().UTC().Format(time.RFC3339Nano)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/service/precheck" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer "+secret {
			t.Error("invalid request")
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "private-email") || strings.Contains(string(body), "private-cookie") {
			t.Error("unneeded private data sent")
		}
		var in map[string]any
		_ = json.Unmarshal(body, &in)
		if in["target"] != "plus" || in["accessToken"] != "synthetic-token" {
			t.Error("wrong payload")
		}
		if status == 302 {
			w.Header().Set("Location", serverURLPlaceholder)
			w.WriteHeader(302)
			return
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"state": state, "code": "web_subscription_ended", "target": target, "checkedAt": date, "receipt": "untrusted"})
	}))
	defer server.Close()
	client, err := NewSessionChecker(server.URL+"/api/service/precheck", secret)
	if err != nil {
		t.Fatal(err)
	}
	c := Credential{Mode: "session", Session: `{"user":{"email":"private-email"},"sessionToken":"private-cookie","accessToken":"synthetic-token","account":{"id":"00000000-0000-4000-8000-000000000001"}}`}
	out, err := client.Check(context.Background(), c, "plus")
	if err != nil || out.State != "eligible" || out.Receipt != "" {
		t.Fatal(out, err)
	}
	for _, bad := range []string{"garbage", ""} {
		state = bad
		if _, err = client.Check(context.Background(), c, "plus"); err == nil {
			t.Fatal("invalid state accepted")
		}
	}
	state = "eligible"
	target = "pro-5x"
	if _, err = client.Check(context.Background(), c, "plus"); err == nil {
		t.Fatal("wrong product accepted")
	}
	target = "plus"
	date = time.Now().Add(-3 * time.Minute).Format(time.RFC3339Nano)
	if _, err = client.Check(context.Background(), c, "plus"); err == nil {
		t.Fatal("stale accepted")
	}
	status = 429
	out, err = client.Check(context.Background(), c, "plus")
	if err != nil || out.State != "limited" {
		t.Fatal(out, err)
	}
	status = 302
	before := calls
	out, err = client.Check(context.Background(), c, "plus")
	if err != nil || out.State != "unknown" || calls != before+1 {
		t.Fatal("redirect followed", out, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	out, err = client.Check(cancelled, c, "plus")
	if err != nil || out.State != "unknown" {
		t.Fatal("cancellation misclassified as timeout", out, err)
	}
	deadline, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	out, err = client.Check(deadline, c, "plus")
	if err != nil || out.State != "timeout" {
		t.Fatal("deadline not classified as timeout", out, err)
	}
	for _, endpoint := range []string{"http://example.com/api/service/precheck", server.URL + "/wrong", server.URL + "/api/service/precheck?token=bad"} {
		if _, err = NewSessionChecker(endpoint, secret); err == nil {
			t.Fatal("bad URL accepted")
		}
	}
}

const serverURLPlaceholder = "https://must-not-follow.example.test/"

type checkerFunc func(context.Context, Credential, string) (PrecheckResult, error)

func (f checkerFunc) Check(ctx context.Context, c Credential, plan string) (PrecheckResult, error) {
	return f(ctx, c, plan)
}

func TestPrecheckDeferPolicyAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		state, code string
		allowed     bool
	}{
		{"limited", "rate_limited", true}, {"timeout", "query_timeout", true},
		{"unknown", "business_evidence_missing", true}, {"unknown", "insufficient_evidence", true}, {"unknown", "account_id_missing", true},
		{"invalid", "authentication_rejected", false}, {"invalid", "upstream_blocked", false}, {"expired", "locally_expired", false},
		{"wait", "store_wait_72h", false}, {"wait", "web_wait_expiry", false}, {"ineligible", "upgrade_requires_php", false},
		{"unknown", "conflicting_results", false}, {"unknown", "check_unavailable", false}, {"unknown", "query_service_error", false},
	} {
		t.Run(tc.code, func(t *testing.T) {
			out, err := checkForSubmission(context.Background(), checkerFunc(func(ctx context.Context, _ Credential, plan string) (PrecheckResult, error) {
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 10*time.Second || time.Until(deadline) < 9*time.Second {
					t.Error("wrong deadline")
				}
				return PrecheckResult{State: tc.state, Code: tc.code, Target: plan}, nil
			}), Credential{}, "plus")
			if err != nil || (out.State == "deferred") != tc.allowed {
				t.Fatal(out, err)
			}
		})
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := checkForSubmission(cancelled, checkerFunc(func(context.Context, Credential, string) (PrecheckResult, error) {
		return PrecheckResult{State: "timeout", Target: "plus"}, nil
	}), Credential{}, "plus"); err == nil {
		t.Fatal("client cancellation authorized")
	}
	if _, err := checkForSubmission(context.Background(), checkerFunc(func(context.Context, Credential, string) (PrecheckResult, error) {
		return PrecheckResult{State: "limited", Target: "pro-5x"}, nil
	}), Credential{}, "plus"); err == nil {
		t.Fatal("wrong target authorized")
	}
}
func TestRemotePrecheckStopsAtTenSecondsAndAllowsUpstreamReview(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	client, err := NewSessionChecker(server.URL+"/api/service/precheck", strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	out, err := checkForSubmission(context.Background(), client, Credential{Mode: "access_token", AccessToken: "synthetic"}, "plus")
	elapsed := time.Since(started)
	if err != nil || out.State != "deferred" || out.Code != "query_timeout" || elapsed < 9500*time.Millisecond || elapsed > 12*time.Second {
		t.Fatal(out, err, elapsed)
	}
}
