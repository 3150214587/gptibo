package integration_test

import (
	"context"
	"gptibo/storefront/internal/httpapi"
	"gptibo/storefront/internal/importer"
	"gptibo/storefront/internal/payments"
	"io"
	"log/slog"
	"testing"
)

func TestRehearsalBlocksTransactionsCallbacksAndNonAdminReads(t *testing.T) {
	owner, app, a, normal, mail := identityFixture(t)
	admin := login(t, normal, mail, "admin", "")
	h := httpapi.Router(app, "fixture", slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.Options{Origin: testOrigin, Auth: a, Rehearsal: true})
	object(t, request(h, "GET", "/api/admin/overview", nil, admin), 200)
	object(t, request(h, "GET", "/api/admin/orders", nil, nil), 401)
	for _, path := range []string{"/api/orders", "/api/orders/client", "/api/admin/recharge/reconcile", "/api/admin/login/email-code", "/api/orders/recovery/send", "/api/payments/notify/alipay", "/api/payments/notify/gatewaya-wxpay", "/api/recharge/webhook", "/api/telegram/webhook", "/api/invoices/notify/alipay"} {
		object(t, request(h, "POST", path, map[string]any{}, admin), 403)
	}
	for _, path := range []string{"/api/orders", "/api/partner/orders", "/api/invoices/notify/gatewaya-wxpay", "/api/payments/notify/gatewaya-wxpay", "/api/telegram/webhook"} {
		object(t, request(h, "GET", path, nil, admin), 403)
	}
	var n int
	if e := owner.DB.QueryRow(context.Background(), `SELECT count(*) FROM orders`).Scan(&n); e != nil || n != 0 {
		t.Fatal("rehearsal created order", e)
	}
	// Legacy demo rows normally settle during GET; rehearsal must preserve them too.
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents) VALUES('plus','chatgpt','Plus',10000)`)
	execSQL(t, owner, `INSERT INTO orders(id,owner_hash,idempotency_key,plan,plan_name,price_cents,mode,payment_status,status,created_at,updated_at,last_event_id,started_at) VALUES('YMX-OLD-DEMO','demo','demo','plus','Plus',10000,'demo','simulated','processing',1,1,'initial',1)`)
	object(t, request(h, "GET", "/api/admin/overview", nil, admin), 200)
	object(t, request(h, "GET", "/api/admin/orders/YMX-OLD-DEMO", nil, admin), 200)
	var status string
	if e := owner.DB.QueryRow(context.Background(), `SELECT status FROM orders WHERE id='YMX-OLD-DEMO'`).Scan(&status); e != nil || status != "processing" {
		t.Fatal("GET settled imported demo order", e)
	}
}

func TestImportBindingsDetectOriginalMerchantAndMailSender(t *testing.T) {
	owner, app := fixture(t, true)
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents) VALUES('plus','chatgpt','Plus',10000)`)
	execSQL(t, owner, `INSERT INTO orders(id,owner_hash,idempotency_key,plan,plan_name,price_cents,mode,payment_status,status,created_at,updated_at,last_event_id) VALUES('YMX-REVIEW','review','review','plus','Plus',10000,'live','paid','session',1,1,'initial')`)
	execSQL(t, owner, `INSERT INTO payments(order_id,provider,account,amount_cents,expires_at,created_at,updated_at) VALUES('YMX-REVIEW','alipay','old',10000,1,1,1)`)
	execSQL(t, owner, `INSERT INTO email_deliveries(order_id,kind,value,mail_body,request_key,created_at,updated_at) VALUES('YMX-REVIEW','cdk','fixture','{"from":"old@example.test","to":["buyer@example.test"]}','key',1,1)`)
	for _, match := range []bool{false, true} {
		account, from := "new", "new@example.test"
		if match {
			account, from = "old", "old@example.test"
		}
		result, e := importer.CheckBindings(context.Background(), app, payments.Service{Provider: &paymentFake{account: account}}, nil, nil, nil, from)
		if e != nil {
			t.Fatal(e)
		}
		want := int64(1)
		if match {
			want = 0
		}
		if result["payment_accounts_mismatched"] != want || result["unsent_mail_sender_mismatched"] != want {
			t.Fatal("incorrect binding check", result)
		}
	}
}
