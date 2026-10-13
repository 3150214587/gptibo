package integration_test

import (
	"context"
	"gptibo/storefront/internal/security"
	"gptibo/storefront/internal/recharge"
	"net/http"
	"strings"
	"testing"
)

type checkStub struct {
	state string
	code  string
	calls int
}

func (s *checkStub) Check(_ context.Context, c recharge.Credential, plan string) (recharge.PrecheckResult, error) {
	s.calls++
	return recharge.PrecheckResult{State: s.state, Code: s.code, Target: plan}, nil
}
func TestSessionPrecheckGuardsSubmissionAndPreservesRecovery(t *testing.T) {
	f := newRecoveryFixture(t)
	f.failed()
	if err := f.service.Sync(context.Background(), f.id, true); err != nil {
		t.Fatal(err)
	}
	checker := &checkStub{state: "wait"}
	f.service.Checker = checker
	key := security.ID()
	credential := map[string]any{"mode": "session", "session": f.session}
	input := map[string]any{"action": "submit", "key": key, "customerConfirmedEmail": true, "credential": credential}
	path := "/api/orders/" + f.id
	object(t, request(f.handler, "POST", path+"/fulfillment", input, f.buyer), 409)
	if f.fake.submitCalls != 1 {
		t.Fatal("unguarded submission")
	}
	check := map[string]any{"key": key, "credential": credential, "target": "pro-25x"}
	// Unknown JSON properties are rejected, so the browser cannot choose the product.
	delete(check, "target")
	out := object(t, request(f.handler, "POST", path+"/session-precheck", check, f.buyer), 200)["precheck"].(map[string]any)
	if out["state"] != "wait" || out["receipt"] != nil || out["target"] != "plus" {
		t.Fatal("wait incorrectly authorized", out)
	}
	before := checker.calls
	object(t, request(f.handler, "POST", path+"/session-precheck", check, &http.Cookie{Name: "__Host-sf_buyer", Value: security.Token()}), 404)
	if checker.calls != before {
		t.Fatal("foreign order queried")
	}
	checker.state = "eligible"
	out = object(t, request(f.handler, "POST", path+"/session-precheck", check, f.buyer), 200)["precheck"].(map[string]any)
	receipt, ok := out["receipt"].(string)
	if !ok || receipt == "" {
		t.Fatal("missing receipt")
	}
	input["precheckReceipt"] = receipt
	input["credential"] = map[string]any{"mode": "session", "session": strings.ReplaceAll(f.session, "private-session-fixture", "changed-token")}
	object(t, request(f.handler, "POST", path+"/fulfillment", input, f.buyer), 409)
	input["credential"] = credential
	n := int64(2)
	f.fake.redemption.Attempt = &n
	f.fake.redemption.ID = "attempt-2"
	f.fake.redemption.Status = "running"
	f.fake.order.Fulfillment = "processing"
	object(t, request(f.handler, "POST", path+"/fulfillment", input, f.buyer), 200)
	if f.fake.submitCalls != 2 {
		t.Fatal("approved request not submitted")
	}
	// Same request remains recoverable even when the checker would reject new work.
	checker.state = "invalid"
	before = checker.calls
	delete(input, "precheckReceipt")
	out = object(t, request(f.handler, "POST", path+"/session-precheck", check, f.buyer), 200)["precheck"].(map[string]any)
	if out["state"] != "recovery" || checker.calls != before {
		t.Fatal("recovery rechecked")
	}
	object(t, request(f.handler, "POST", path+"/fulfillment", input, f.buyer), 200)
	if f.fake.submitCalls != 2 {
		t.Fatal("recovery created duplicate recharge")
	}
}
func TestSessionPrecheckAdminRetryCannotBypassAndDoesNotSaveFailedChecks(t *testing.T) {
	f := newRecoveryFixture(t)
	f.failed()
	checker := &checkStub{state: "ineligible"}
	f.service.Checker = checker
	f.post(t, "retry", 409)
	if checker.calls != 1 || f.fake.submitCalls != 1 {
		t.Fatal("admin bypass")
	}
	var count int
	if err := f.owner.DB.QueryRow(context.Background(), "SELECT count(*) FROM recharge_redemptions WHERE order_id=$1", f.id).Scan(&count); err != nil || count != 1 {
		t.Fatal("failed check saved attempt", err)
	}
}

func TestSessionPrecheckDeferredSubmissionStillRequiresBoundReceipt(t *testing.T) {
	for _, tc := range []struct{ state, code string }{{"limited", "rate_limited"}, {"timeout", "query_timeout"}, {"unknown", "business_evidence_missing"}} {
		t.Run(tc.state, func(t *testing.T) {
			f := newRecoveryFixture(t)
			f.failed()
			if err := f.service.Sync(context.Background(), f.id, true); err != nil {
				t.Fatal(err)
			}
			f.service.Checker = &checkStub{state: tc.state, code: tc.code}
			key := security.ID()
			credential := map[string]any{"mode": "session", "session": f.session}
			path := "/api/orders/" + f.id
			out := object(t, request(f.handler, "POST", path+"/session-precheck", map[string]any{"key": key, "credential": credential}, f.buyer), 200)["precheck"].(map[string]any)
			receipt, ok := out["receipt"].(string)
			if out["state"] != "deferred" || !ok || receipt == "" {
				t.Fatal("missing deferred permission", out)
			}
			input := map[string]any{"action": "submit", "key": key, "customerConfirmedEmail": true, "credential": credential}
			object(t, request(f.handler, "POST", path+"/fulfillment", input, f.buyer), 409)
			input["precheckReceipt"] = receipt
			input["key"] = security.ID()
			object(t, request(f.handler, "POST", path+"/fulfillment", input, f.buyer), 409)
			input["key"] = key
			if f.fake.submitCalls != 1 {
				t.Fatal("unbound defer submitted")
			}
			n := int64(2)
			f.fake.redemption.Attempt = &n
			f.fake.redemption.ID = "deferred-2"
			f.fake.redemption.Status = "running"
			f.fake.order.Fulfillment = "processing"
			object(t, request(f.handler, "POST", path+"/fulfillment", input, f.buyer), 200)
			object(t, request(f.handler, "POST", path+"/fulfillment", input, f.buyer), 200)
			if f.fake.submitCalls != 2 {
				t.Fatal("deferred recharge missing or duplicated")
			}
		})
	}
}
func TestSessionPrecheckAdminRetryAllowsDeferredReview(t *testing.T) {
	f := newRecoveryFixture(t)
	f.failed()
	f.service.Checker = &checkStub{state: "limited", code: "rate_limited"}
	f.service.Client.HTTP.Transport = rechargeTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method == "POST" && r.URL.Path == "/v1/redemptions" {
			n := int64(2)
			f.fake.redemption.Attempt = &n
			f.fake.redemption.Version = &n
			f.fake.redemption.ID = "deferred-admin-2"
			f.fake.redemption.Status = "queued"
			f.fake.redemption.Retry = false
		}
		return f.fake.round(r)
	})
	f.post(t, "retry", 200)
	if f.fake.submitCalls != 2 {
		t.Fatal("admin deferred review not submitted")
	}
}
