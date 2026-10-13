package integration_test

import (
	"context"
	"gptibo/storefront/internal/httpapi"
	"gptibo/storefront/internal/orders"
	"gptibo/storefront/internal/security"
	"gptibo/storefront/internal/store"
	"gptibo/storefront/internal/recharge"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
)

type recoveryFixture struct {
	owner                *store.Store
	service              *recharge.Service
	fake                 *rechargeFake
	handler              http.Handler
	admin, buyer         *http.Cookie
	id, attempt, session string
}

func newRecoveryFixture(t *testing.T) recoveryFixture {
	t.Helper()
	owner, app, auth, base, mail := identityFixture(t)
	admin := login(t, base, mail, "admin", "")
	client, fake := newRechargeFake(t)
	s := &recharge.Service{CredentialSecret: strings.Repeat("ab", 32), Store: app, Client: client, Checkout: true, Products: map[string]string{"plus": "plus"}, Notify: testOrigin + "/api/recharge/webhook"}
	h := httpapi.Router(app, "fixture", slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.Options{Origin: testOrigin, Auth: auth, Recharge: s})
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents,enabled) VALUES('plus','chatgpt','Plus',10000,true)`)
	buyer := &http.Cookie{Name: "__Host-sf_buyer", Value: security.Token()}
	created := object(t, request(h, "POST", "/api/orders", map[string]any{"plan": "plus", "email": "buyer@example.test", "key": security.ID(), "expectedPrice": 10000}, buyer), 201)["order"].(map[string]any)
	id := created["id"].(string)
	fake.order.Payment = "paid"
	if err := s.Sync(context.Background(), id, true); err != nil {
		t.Fatal(err)
	}
	attempt := security.ID()
	session := `{"user":{"email":"recharge@example.test"},"accessToken":"private-session-fixture"}`
	object(t, request(h, "POST", "/api/orders/"+id+"/fulfillment", map[string]any{"action": "submit", "key": attempt, "customerConfirmedEmail": true, "credential": map[string]any{"mode": "session", "session": session}}, buyer), 200)
	return recoveryFixture{owner, s, fake, h, admin, buyer, id, attempt, session}
}
func (f recoveryFixture) failed() {
	f.fake.redemption.Status = "failed"
	f.fake.redemption.Retry = true
	f.fake.redemption.Next = "resubmit"
	v := int64(2)
	f.fake.redemption.Version = &v
	f.fake.order.Fulfillment = "failed"
}
func (f recoveryFixture) post(t *testing.T, action string, code int) map[string]any {
	t.Helper()
	return object(t, request(f.handler, "POST", "/api/admin/orders/"+f.id+"/recharge-"+action, map[string]any{"attemptId": f.attempt}, f.admin), code)
}
func TestRechargeCredentialDisclosureAndEncryption(t *testing.T) {
	f := newRecoveryFixture(t)
	path := "/api/admin/orders/" + f.id + "/recharge-credential"
	response := request(f.handler, "POST", path, map[string]any{"attemptId": f.attempt}, f.admin)
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("credential response cached")
	}
	got := object(t, response, 200)["credential"].(map[string]any)
	if got["session"] != f.session {
		t.Fatal("session did not round trip")
	}
	for _, cookie := range []*http.Cookie{nil, f.buyer} {
		object(t, request(f.handler, "POST", path, map[string]any{"attemptId": f.attempt}, cookie), 401)
	}
	object(t, request(f.handler, "POST", path, map[string]any{"attemptId": security.ID()}, f.admin), 409)
	for _, p := range []string{"/api/orders/" + f.id, "/api/admin/orders/" + f.id, "/api/admin/orders"} {
		cookie := f.admin
		if p == "/api/orders/"+f.id {
			cookie = f.buyer
		}
		r := request(f.handler, "GET", p, nil, cookie)
		object(t, r, 200)
		if strings.Contains(r.Body.String(), "private-session-fixture") || strings.Contains(r.Body.String(), "ciphertext") {
			t.Fatal("ordinary response disclosed credential")
		}
	}
	var raw string
	if err := f.owner.DB.QueryRow(context.Background(), `SELECT to_jsonb(c)::text FROM recharge_credentials c WHERE attempt_id=$1`, f.attempt).Scan(&raw); err != nil || strings.Contains(raw, "private-session-fixture") {
		t.Fatal("credential not encrypted", err)
	}
	var audit int
	if err := f.owner.DB.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE action='recharge-credential-copy' AND target=$1 AND detail::text NOT LIKE '%private-session-fixture%'`, f.id).Scan(&audit); err != nil || audit != 1 {
		t.Fatal("copy audit missing", err)
	}
	// Wrong master key fails closed, with no credential in the HTTP error.
	f.service.CredentialSecret = strings.Repeat("cd", 32)
	failed := request(f.handler, "POST", path, map[string]any{"attemptId": f.attempt}, f.admin)
	object(t, failed, 503)
	if strings.Contains(failed.Body.String(), "private-session-fixture") {
		t.Fatal("decryption error disclosed credential")
	}
}
func TestRechargeFailureUpdatesEvenWhenRefundedOrBlocked(t *testing.T) {
	for _, flag := range []string{"blocked", "hold", "refunded", "financial"} {
		t.Run(flag, func(t *testing.T) {
			f := newRecoveryFixture(t)
			f.failed()
			f.fake.redemption.Status = "cancelled"
			// Equal progress versions previously left a terminal task at review stage.
			v := int64(1)
			f.fake.redemption.Version = &v
			execSQL(t, f.owner, `UPDATE recharge_redemptions SET progress_stage='review' WHERE id=$1`, f.attempt)
			switch flag {
			case "blocked":
				execSQL(t, f.owner, `UPDATE orders SET redemption_blocked=1 WHERE id=$1`, f.id)
			case "hold":
				execSQL(t, f.owner, `UPDATE orders SET refund_hold=1 WHERE id=$1`, f.id)
			case "refunded":
				execSQL(t, f.owner, `UPDATE orders SET refunded_cents=10000 WHERE id=$1`, f.id)
				fallthrough
			case "financial":
				f.fake.order.Payment = "refunded"
				f.fake.order.Refunded = "100.00"
				f.fake.order.Margin = "-60.00"
			}
			if err := f.service.Verify(context.Background(), f.id); err != nil {
				t.Fatal(err)
			}
			o, err := orders.Get(context.Background(), f.owner.DB, f.id, nil, false)
			if err != nil || o.Status != "failed" {
				t.Fatal("terminal task still processing", err)
			}
			var stage string
			if err = f.owner.DB.QueryRow(context.Background(), `SELECT progress_stage FROM recharge_redemptions WHERE id=$1`, f.attempt).Scan(&stage); err != nil || stage != "cancelled" {
				t.Fatal("terminal stage stale", stage, err)
			}
			f.post(t, "retry", 409)
			if f.fake.submitCalls != 1 {
				t.Fatal("frozen order resubmitted")
			}
		})
	}
}
func TestRechargeAdminRetryUsesOriginalOrderAndPreventsDuplicates(t *testing.T) {
	f := newRecoveryFixture(t)
	f.failed()
	f.service.Client.HTTP.Transport = rechargeTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method == "POST" && r.URL.Path == "/v1/redemptions" {
			n := int64(2)
			f.fake.redemption.Attempt = &n
			f.fake.redemption.Version = &n
			f.fake.redemption.ID = "attempt-2"
			f.fake.redemption.Status = "queued"
			f.fake.redemption.Retry = false
		}
		return f.fake.round(r)
	})
	path := "/api/admin/orders/" + f.id + "/recharge-retry"
	results := make(chan int, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			results <- request(f.handler, "POST", path, map[string]any{"attemptId": f.attempt}, f.admin).Code
		})
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for code := range results {
		if code == 200 {
			success++
		} else if code == 409 {
			conflict++
		} else {
			t.Fatalf("unexpected retry status %d", code)
		}
	}
	if success != 1 || conflict != 1 || f.fake.submitCalls != 2 || f.fake.createCalls != 1 {
		t.Fatal("duplicate retry or new paid order", success, conflict, f.fake.submitCalls)
	}
	if f.fake.bodies[0] != f.fake.bodies[1] || f.fake.keys[0] == f.fake.keys[1] {
		t.Fatal("retry lost original credential/order or reused previous attempt key")
	}
	f.post(t, "retry", 409)
	var count int
	if err := f.owner.DB.QueryRow(context.Background(), `SELECT count(*) FROM order_events WHERE order_id=$1 AND action='recharge-resend' AND actor LIKE 'admin:%'`, f.id).Scan(&count); err != nil || count != 1 {
		t.Fatal("retry audit missing", err)
	}
}
func TestRechargeRetryDeniedAndHistoricalCredentialsUnavailable(t *testing.T) {
	for _, reason := range []string{"running", "no-permission", "refund-chosen", "missing-credential", "upstream-unavailable"} {
		t.Run(reason, func(t *testing.T) {
			f := newRecoveryFixture(t)
			if reason != "running" {
				f.failed()
			}
			switch reason {
			case "no-permission":
				f.fake.redemption.Retry = false
			case "refund-chosen":
				f.fake.redemption.Recovery = "refund"
			case "missing-credential":
				execSQL(t, f.owner, `DELETE FROM recharge_credentials WHERE attempt_id=$1`, f.attempt)
				f.post(t, "credential", 409)
			case "upstream-unavailable":
				f.service.Client.HTTP.Transport = rechargeTransport(func(*http.Request) (*http.Response, error) { return nil, io.ErrUnexpectedEOF })
			}
			code := 409
			if reason == "upstream-unavailable" {
				code = 503
			}
			f.post(t, "retry", code)
			if f.fake.submitCalls != 1 {
				t.Fatal("unsafe retry")
			}
		})
	}
}
func TestRechargeDiscoversLostResponseAndNewUpstreamAttempt(t *testing.T) {
	f := newRecoveryFixture(t)
	execSQL(t, f.owner, `UPDATE recharge_redemptions SET remote_id=NULL,status='unknown',progress_version=0 WHERE id=$1`, f.attempt)
	f.failed()
	if err := f.service.Verify(context.Background(), f.id); err != nil {
		t.Fatal(err)
	}
	var remote, status string
	if err := f.owner.DB.QueryRow(context.Background(), `SELECT remote_id,status FROM recharge_redemptions WHERE id=$1`, f.attempt).Scan(&remote, &status); err != nil || remote != "attempt" || status != "failed" {
		t.Fatal("lost response not recovered", err)
	}
	n := int64(2)
	f.fake.redemption.Attempt = &n
	f.fake.redemption.ID = "attempt-2"
	f.fake.redemption.Status = "running"
	if err := f.service.Verify(context.Background(), f.id); err != nil {
		t.Fatal(err)
	}
	o, err := orders.Get(context.Background(), f.owner.DB, f.id, nil, false)
	if err != nil || o.Status != "processing" {
		t.Fatal("upstream attempt not adopted", err)
	}
	detail := object(t, request(f.handler, "GET", "/api/admin/orders/"+f.id, nil, f.admin), 200)["order"].(map[string]any)
	if detail["rechargeCredential"].(map[string]any)["saved"] != false {
		t.Fatal("old credential assigned to external attempt")
	}
	f.post(t, "retry", 409)
	if f.fake.submitCalls != 1 {
		t.Fatal("query submitted recharge")
	}
}

func TestRechargeRetryUnknownResponseNeverCreatesAnotherAttempt(t *testing.T) {
	f := newRecoveryFixture(t)
	f.failed()
	f.service.Client.HTTP.Transport = rechargeTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method == "POST" && r.URL.Path == "/v1/redemptions" {
			f.fake.ambiguous = true
		}
		return f.fake.round(r)
	})
	f.post(t, "retry", 503)
	calls := f.fake.submitCalls
	if calls != 3 || f.fake.keys[1] != f.fake.keys[2] || f.fake.bodies[1] != f.fake.bodies[2] {
		t.Fatal("transport retry changed business identity")
	}
	f.post(t, "retry", 503)
	if f.fake.submitCalls != calls {
		t.Fatal("unknown request submitted again")
	}
	var attempts int
	if err := f.owner.DB.QueryRow(context.Background(), `SELECT count(*) FROM recharge_redemptions WHERE order_id=$1`, f.id).Scan(&attempts); err != nil || attempts != 2 {
		t.Fatal("duplicate attempt persisted", err)
	}
}

func TestRechargeActiveQueuePrioritizesProcessingOverHistoricalNotifications(t *testing.T) {
	f := newRecoveryFixture(t)
	f.failed()
	execSQL(t, f.owner, `INSERT INTO orders(id,owner_hash,idempotency_key,plan,plan_name,price_cents,mode,payment_status,status,created_at,updated_at,last_event_id)
 SELECT 'YMX-OLD-'||i,'old','old-'||i,'plus','Plus',10000,'live','paid','success',1,1,'fixture' FROM generate_series(1,12) i`)
	execSQL(t, f.owner, `INSERT INTO recharge_orders(order_id,account,product_code,collection_mode,create_body,remote_id,updated_at,notify_version)
 SELECT 'YMX-OLD-'||i,$1,'plus','platform_collect','{}','old-'||i,0,1 FROM generate_series(1,12) i`, f.service.Client.Account())
	execSQL(t, f.owner, `UPDATE recharge_orders SET updated_at=100 WHERE order_id=$1`, f.id)
	_ = f.service.ReconcileActive(context.Background()) // Historical fake IDs intentionally fail lookup.
	o, err := orders.Get(context.Background(), f.owner.DB, f.id, nil, false)
	if err != nil || o.Status != "failed" {
		t.Fatal("historical notifications starved current task", err)
	}
}

func TestRechargeOlderUpstreamTaskCannotResolveNewUnknownSubmission(t *testing.T) {
	f := newRecoveryFixture(t)
	f.failed()
	next := security.ID()
	execSQL(t, f.owner, `INSERT INTO recharge_redemptions(id,order_id,attempt_no,request_hash,credential_mode,created_at) VALUES($1,$2,2,'unknown','session',1)`, next, f.id)
	if err := f.service.Verify(context.Background(), f.id); err != nil {
		t.Fatal(err)
	}
	var unresolved bool
	if err := f.owner.DB.QueryRow(context.Background(), `SELECT status='unknown' AND remote_id IS NULL AND fallback=0 FROM recharge_redemptions WHERE id=$1`, next).Scan(&unresolved); err != nil || !unresolved {
		t.Fatal("previous result changed newer unknown submission", err)
	}
	f.attempt = next
	f.post(t, "retry", 409)
	if f.fake.submitCalls != 1 {
		t.Fatal("unknown submission was resent")
	}
}
