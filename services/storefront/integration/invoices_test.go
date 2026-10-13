package integration_test

import (
	"context"
	"gptibo/storefront/internal/httpapi"
	"gptibo/storefront/internal/payments"
	"gptibo/storefront/internal/security"
	"io"
	"log/slog"
	"testing"
)

func TestInvoiceFeePaymentIsolationAndManualIssuance(t *testing.T) {
	owner, app, a, base, mail := identityFixture(t)
	admin := login(t, base, mail, "admin", "")
	object(t, request(base, "POST", "/api/admin/partners", map[string]any{"email": "invoice-a@example.test"}, admin), 200)
	object(t, request(base, "POST", "/api/admin/partners", map[string]any{"email": "invoice-b@example.test"}, admin), 200)
	partner := login(t, base, mail, "partner", "invoice-a@example.test")
	other := login(t, base, mail, "partner", "invoice-b@example.test")
	fake := &paymentFake{account: "invoice-merchant"}
	pay := payments.Service{Store: app, Provider: fake, NotifyOrigin: testOrigin}
	h := httpapi.Router(app, "fixture", slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.Options{Origin: testOrigin, Auth: a, Payments: &pay})
	id := security.ID()
	in := map[string]any{"key": id, "baseCents": 101, "includeFee": true, "title": "发票测试", "taxNumber": "TEST-TAX", "invoiceType": "ordinary", "email": "invoice@example.test", "note": "fixture"}
	created := object(t, request(h, "POST", "/api/partner/invoices", in, partner), 201)["invoice"].(map[string]any)
	if created["fee_cents"] != float64(5) || created["invoice_cents"] != float64(106) {
		t.Fatal("invoice fee rounding wrong")
	}
	object(t, request(h, "POST", "/api/partner/invoices", in, partner), 201)
	in["baseCents"] = 102
	object(t, request(h, "POST", "/api/partner/invoices", in, partner), 409)
	object(t, request(h, "GET", "/api/partner/invoices/"+id, nil, other), 404)
	object(t, request(h, "POST", "/api/partner/invoices/"+id+"/payment", map[string]any{"action": "create"}, other), 404)
	object(t, request(h, "POST", "/api/admin/invoices/"+id+"/submit", map[string]any{"version": 0}, admin), 409)
	object(t, request(h, "POST", "/api/partner/invoices/"+id+"/payment", map[string]any{"action": "create"}, partner), 200)
	var paymentID string
	e := owner.DB.QueryRow(context.Background(), `SELECT id FROM invoice_payments WHERE invoice_id=$1`, id).Scan(&paymentID)
	if e != nil {
		t.Fatal(e)
	}
	fake.query = payments.Result{OrderID: paymentID, Amount: 6, TradeNo: "INVOICE-TRADE", Status: "paid"}
	object(t, request(h, "POST", "/api/partner/invoices/"+id+"/payment", map[string]any{"action": "query"}, partner), 409)
	fake.query.Amount = 5
	paid := object(t, request(h, "POST", "/api/partner/invoices/"+id+"/payment", map[string]any{"action": "query"}, partner), 200)["invoice"].(map[string]any)
	if paid["status"] != "awaiting_submission" || paid["version"] != float64(1) {
		t.Fatal("verified fee not advanced")
	}
	object(t, request(h, "POST", "/api/admin/invoices/"+id+"/submit", map[string]any{"version": 1, "reference": "MANUAL-SUBMISSION"}, admin), 200)
	object(t, request(h, "POST", "/api/admin/invoices/"+id+"/complete", map[string]any{"version": 2, "note": "issued"}, admin), 200)
	detail := object(t, request(h, "GET", "/api/admin/invoices/"+id, nil, admin), 200)
	if detail["submission"].(map[string]any)["amountCents"] != float64(106) || len(detail["events"].([]any)) != 4 {
		t.Fatal("submission contract or audit missing")
	}
	for _, key := range []string{"lease_token", "lease_until"} {
		if _, ok := detail["invoice"].(map[string]any)[key]; ok {
			t.Fatal("lease leaked")
		}
	}
	object(t, request(h, "GET", "/api/partner/invoices?status=completed&q=发票", nil, partner), 200)
}
