package integration_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gptibo/storefront/internal/httpapi"
	"gptibo/storefront/internal/recharge"
)

func TestLegacyReferralAndAuditSnapshots(t *testing.T) {
	owner, _, _, h, mail := identityFixture(t)
	execSQL(t, owner, `INSERT INTO partners(id,slug,name,email,active) VALUES('legacy','ABCDEFGH','Legacy','legacy@example.test',true)`)
	object(t, request(h, "GET", "/api/catalog?partner=ABCDEFGH", nil, nil), 200)
	admin := login(t, h, mail, "admin", "")
	object(t, request(h, "POST", "/api/admin/partners/legacy", map[string]any{"name": "Updated", "email": "new@example.test", "active": true, "version": 0}, admin), 200)
	var before, after string
	err := owner.DB.QueryRow(context.Background(), `SELECT detail->'before'->>'email',detail->'after'->>'email' FROM audit_events WHERE action='partner-update'`).Scan(&before, &after)
	if err != nil || before != "legacy@example.test" || after != "new@example.test" {
		t.Fatal("email audit snapshots missing", err)
	}
}

func TestTrustedIngressRateLimitsRemainPerClient(t *testing.T) {
	_, app, identity, _, _ := identityFixture(t)
	h := httpapi.Router(app, "fixture-worker", slog.New(slog.NewJSONHandler(io.Discard, nil)), httpapi.Options{Origin: testOrigin, Auth: identity, TrustedProxies: []string{"127.0.0.1/32"}})
	send := func(i int, remote, forwarded string) int {
		r := httptest.NewRequest("POST", "/api/orders/recovery/send", strings.NewReader(fmt.Sprintf(`{"email":"ingress%d@example.test"}`, i)))
		r.RemoteAddr = remote
		r.Header.Set("Origin", testOrigin)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Forwarded-For", forwarded)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	for i := 1; i <= 11; i++ {
		if status := send(i, "127.0.0.1:1234", fmt.Sprintf("192.0.2.%d", i)); status != 200 {
			t.Fatalf("trusted client %d: %d", i, status)
		}
	}
	for i := 1; i <= 11; i++ {
		want := 200
		if i == 11 {
			want = 429
		}
		if status := send(i+20, "192.0.2.100:1234", fmt.Sprintf("198.51.100.%d", i)); status != want {
			t.Fatalf("untrusted header bypass: %d", status)
		}
	}
}

func TestRetentionPreservesConcurrentlyRenewedLimit(t *testing.T) {
	owner, app := fixture(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	execSQL(t, owner, `INSERT INTO auth_limits(key,attempts,reset_at) VALUES('cleanup-race',4,now()-interval '1 minute')`)
	tx, e := app.DB.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(context.Background())
	if _, e = tx.Exec(ctx, `UPDATE auth_limits SET attempts=1,reset_at=now()+interval '1 hour' WHERE key='cleanup-race'`); e != nil {
		t.Fatal(e)
	}
	var role string
	if e = tx.QueryRow(ctx, "SELECT current_user").Scan(&role); e != nil {
		t.Fatal(e)
	}
	result := make(chan error, 1)
	go func() { result <- app.PruneAuth(ctx) }()
	waiting := false
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		if e = owner.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE usename=$1 AND query LIKE 'DELETE FROM auth_limits%' AND wait_event_type='Lock')`, role).Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("cleanup did not wait on concurrent update")
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	if e = <-result; e != nil {
		t.Fatal(e)
	}
	var n int
	if e = app.DB.QueryRow(ctx, `SELECT count(*) FROM auth_limits WHERE key='cleanup-race' AND reset_at>now()`).Scan(&n); e != nil || n != 1 {
		t.Fatal("fresh limit deleted", e)
	}
}

func TestRechargeFailedHistoryDoesNotStarveLaterOrders(t *testing.T) {
	owner, app := fixture(t, true)
	ctx := context.Background()
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents) VALUES('plus','chatgpt','Plus',10000)`)
	client, fake := newRechargeFake(t)
	svc := &recharge.Service{CredentialSecret: strings.Repeat("ab", 32), Store: app, Client: client, Checkout: true}
	for i := 0; i < 11; i++ {
		id := fmt.Sprintf("YMX-REVIEW-%02d", i)
		execSQL(t, owner, `INSERT INTO orders(id,owner_hash,idempotency_key,plan,plan_name,price_cents,mode,payment_status,status,created_at,updated_at,last_event_id) VALUES($1,'review',$1,'plus','Plus',10000,'live','paid','session',1,1,$1)`, id)
		account := client.Account()
		remote := "original"
		if i < 10 {
			remote = id
		}
		if i < 10 {
			account = "https://retired.example.test:old-tenant"
		}
		execSQL(t, owner, `INSERT INTO recharge_orders(order_id,account,product_code,collection_mode,create_body,remote_id,payment_status,updated_at) VALUES($1,$2,'plus','platform_collect','{}',$4,'paid',$3)`, id, account, int64(i+1), remote)
	}
	fake.order.Merchant = "YMX-REVIEW-10"
	fake.order.Payment = "paid"
	for pass := 0; pass < 3; pass++ {
		report, err := svc.ReconcileReport(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("pass %d: %+v", pass+1, report)
	}
	var updated int64
	if err := owner.DB.QueryRow(ctx, `SELECT updated_at FROM recharge_orders WHERE order_id='YMX-REVIEW-10'`).Scan(&updated); err != nil {
		t.Fatal(err)
	}
	if updated == 11 {
		t.Fatal("confirmed: later valid paid order was never reconciled after 3 batches of identical 10 failed historical orders")
	}
}
