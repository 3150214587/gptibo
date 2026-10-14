package providers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"gptibo/platform/internal/commerce"
)

func invoiceFixture(t *testing.T) (*Engine, commerce.Order, commerce.Snapshot, Job) {
	t.Helper()
	e, o, s, j := handoffFixture(t)
	s.Provider.WritesEnabled, s.Provider.XBearer = true, "synthetic-bearer"
	s.ConfirmedRecipientID, j.RecipientID = "12345", "12345"
	j.Account = Object{"auth_token": "synthetic-auth", "ct0": "synthetic-csrf"}
	ctx := context.Background()
	config, _ := json.Marshal(s)
	job, _ := json.Marshal(j)
	provider, _ := json.Marshal(s.Provider)
	if _, err := e.Store.DB.Exec(ctx, "ALTER TABLE orders DISABLE TRIGGER order_guard"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.DB.Exec(ctx, "UPDATE orders SET status='queued',config_cipher=$2,job_cipher=$3,manual_payment_at=now() WHERE id=$1", o.ID, e.Store.Vault.Seal("order:"+o.ID, config), e.Store.Vault.Seal("job:"+o.ID, job)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.DB.Exec(ctx, "ALTER TABLE orders ENABLE TRIGGER order_guard"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.DB.Exec(ctx, "INSERT INTO ledger(id,user_id,order_id,kind,available_delta,frozen_delta,reference,note,actor_id) VALUES('invoice-reserve','user',$1,'reserve',-1700,1700,$1||':reserve','test','admin')", o.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.DB.Exec(ctx, "UPDATE orders SET status='unknown' WHERE id=$1", o.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.DB.Exec(ctx, "INSERT INTO settings(id,payload_cipher) VALUES('provider',$1)", e.Store.Vault.Seal("setting:provider", provider)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.DB.Exec(ctx, "INSERT INTO resource_locks(resource,order_id) VALUES('x-recipient:12345',$1);", o.ID); err != nil {
		t.Fatal(err)
	}
	return e, o, s, j
}

type invoiceMock struct {
	kind     string
	creates  atomic.Int32
	newReads atomic.Int32
	paid     bool
	started  chan struct{}
	proceed  chan struct{}
}

func (m *invoiceMock) install(t *testing.T) {
	t.Helper()
	old, oldX := client, xClient
	t.Cleanup(func() { client, xClient = old, oldX })
	client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "cs_live_test/") {
			if m.kind == "old_network" {
				return nil, errors.New("synthetic timeout")
			}
			if m.kind == "active" && strings.HasSuffix(r.URL.Path, "/init") {
				b, _ := json.Marshal(handoffPage())
				return response(string(b)), nil
			}
			if m.kind == "paid_poll" && strings.HasSuffix(r.URL.Path, "/poll") {
				return response(`{"state":"succeeded","payment_object_status":"succeeded"}`), nil
			}
			x := response(`{"error":{"code":"checkout_not_active_session"}}`)
			x.StatusCode = 400
			return x, nil
		}
		if r.URL.Host == "x.com" {
			switch {
			case strings.HasSuffix(r.URL.Path, "/PremiumGiftingQuery"):
				eligible := m.kind != "ineligible"
				b, _ := json.Marshal(Object{"data": Object{"user": Object{"result": Object{"rest_id": "12345", "core": Object{"screen_name": "tester"}, "premium_gifting_eligible": eligible}}}})
				return response(string(b)), nil
			case strings.HasSuffix(r.URL.Path, "/useSubscriptionProductDetailsByRestIdQuery"):
				amount := 300000000
				if m.kind == "price_changed" {
					amount++
				}
				b, _ := json.Marshal(Object{"data": Object{"web_subscription_product_details_by_rest_id": Object{"rest_id": "prod_test", "prices": []Object{{"amount_local_micro": amount, "currency_code": "BDT", "price_type": "OneTime"}}}}})
				return response(string(b)), nil
			case strings.HasSuffix(r.URL.Path, "/useOneTimePurchaseGiftMutation"):
				m.creates.Add(1)
				var body Object
				_ = json.NewDecoder(r.Body).Decode(&body)
				v := obj(body["variables"])
				if len(v) != 4 || str(v["gift_recipient"]) != "12345" || str(v["external_product_id"]) != "prod_test" || str(v["success_url"]) != "https://x.com/tester/gift-premium/success" {
					t.Error("wrong invoice request")
				}
				if m.started != nil {
					close(m.started)
					<-m.proceed
				}
				if m.kind == "creation_timeout" {
					return nil, errors.New("synthetic lost response")
				}
				if m.kind == "invalid_creation" {
					return response(`{"data":{"onetimepurchase_gift":{"session_id":"cs_live_test","session_status":"Unpaid"}}}`), nil
				}
				return response(`{"data":{"onetimepurchase_gift":{"session_id":"cs_live_new","session_status":"Unpaid","session_url":"https://checkout.stripe.com/c/pay/cs_live_new#new-capability"}}}`), nil
			}
		}
		if r.URL.Path == "/v1/payment_pages/cs_live_new/init" {
			reads := m.newReads.Add(1)
			if m.kind == "recover" && reads == 1 {
				return nil, errors.New("synthetic init timeout")
			}
			p := handoffPage()
			p["session_id"], p["url"] = "cs_live_new", "https://checkout.stripe.com/c/pay/cs_live_new#new-capability"
			delete(p, "payment_intent")
			if m.kind == "wrong_recipient" {
				p["success_url"] = "https://x.com/other/gift-premium/success"
			}
			b, _ := json.Marshal(p)
			return response(string(b)), nil
		}
		if r.URL.Path == "/v1/payment_pages/cs_live_new/poll" {
			p := Object{"session_id": "cs_live_new", "livemode": true, "is_sandbox_merchant": false, "mode": "payment", "success_url": "https://x.com/tester/gift-premium/success", "state": "active", "payment_object_status": "requires_action"}
			if m.paid {
				p["state"], p["payment_object_status"] = "succeeded", "succeeded"
			}
			b, _ := json.Marshal(p)
			return response(string(b)), nil
		}
		t.Error("unexpected charge, card, captcha, or new request", r.Method, r.URL.Host, r.URL.Path)
		return nil, errors.New("unexpected request")
	})}
	xClient = func(Object) (*http.Client, error) { return client, nil }
}

func TestManualInvoiceGuardsAndDurableRecovery(t *testing.T) {
	for _, kind := range []string{"success", "unconfirmed_unpaid", "active", "old_network", "paid_poll", "ineligible", "price_changed", "creation_timeout", "invalid_creation", "recover", "wrong_recipient", "banned", "busy", "recipient_busy", "terminal"} {
		t.Run(kind, func(t *testing.T) {
			e, o, _, original := invoiceFixture(t)
			ctx := context.Background()
			m := &invoiceMock{kind: kind}
			m.install(t)
			switch kind {
			case "banned":
				_, _ = e.Store.DB.Exec(ctx, "UPDATE users SET status='banned' WHERE id=$1", o.UserID)
			case "busy":
				_, _ = e.Store.DB.Exec(ctx, "UPDATE orders SET lease_until=now()+interval '1 minute' WHERE id=$1", o.ID)
			case "recipient_busy":
				_, _ = e.Store.DB.Exec(ctx, "UPDATE resource_locks SET order_id='test-order' WHERE resource='x-recipient:12345'")
			case "terminal":
				_, _ = e.Store.DB.Exec(ctx, "UPDATE orders SET status='failed' WHERE id=$1", o.ID)
			}
			link, err := e.RegenerateManualCheckout(ctx, "admin", o.ID, kind != "unconfirmed_unpaid")
			if (err == nil) != (kind == "success") {
				t.Fatal("unexpected first result", kind, err)
			}
			if kind == "recover" || kind == "success" {
				link, err = e.RegenerateManualCheckout(ctx, "admin", o.ID, true)
				if err != nil || link != "https://checkout.stripe.com/c/pay/cs_live_new#new-capability" {
					t.Fatal("did not reuse/recover original creation", err)
				}
				if reopened, err := e.ManualCheckout(ctx, "admin", o.ID); err != nil || reopened != link {
					t.Fatal("manual invoice could not reopen", err)
				}
			}
			if kind == "creation_timeout" || kind == "invalid_creation" || kind == "wrong_recipient" {
				if _, err = e.RegenerateManualCheckout(ctx, "admin", o.ID, true); err == nil {
					t.Fatal("uncertain/mismatched creation retried")
				}
			}
			wantCreates := int32(0)
			if kind == "success" || kind == "recover" || kind == "creation_timeout" || kind == "invalid_creation" || kind == "wrong_recipient" {
				wantCreates = 1
			}
			if m.creates.Load() != wantCreates {
				t.Fatal("duplicate creation", m.creates.Load())
			}
			var cipher string
			if err := e.Store.DB.QueryRow(ctx, "SELECT job_cipher FROM orders WHERE id=$1", o.ID).Scan(&cipher); err != nil {
				t.Fatal(err)
			}
			raw, _ := e.Store.Vault.Open("job:"+o.ID, cipher)
			var j Job
			_ = json.Unmarshal(raw, &j)
			if kind == "success" || kind == "recover" {
				if j.Session != "cs_live_new" || j.Method != "" || j.ManualInvoiceID == "" {
					t.Fatal("new invoice not bound for manual payment")
				}
				var previous string
				if err := e.Store.DB.QueryRow(ctx, "SELECT previous_job_cipher FROM manual_payment_invoices WHERE id=$1", j.ManualInvoiceID).Scan(&previous); err != nil {
					t.Fatal(err)
				}
				old, _ := e.Store.Vault.Open("job:"+o.ID, previous)
				if !strings.Contains(string(old), original.Session) || !strings.Contains(string(old), original.Method) {
					t.Fatal("original payment archive lost")
				}
				public, err := commerce.ScanOrder(e.Store.DB.QueryRow(ctx, "SELECT "+commerce.OrderColumns+" FROM orders WHERE id=$1", o.ID))
				if err != nil || public.ManualInvoiceStatus != "ready" {
					t.Fatal("invoice state not public", err)
				}
				data, _ := json.Marshal(public)
				if strings.Contains(string(data), "cs_live_") || strings.Contains(string(data), "capability") {
					t.Fatal("private invoice leaked into DTO")
				}
			} else if j.Session != original.Session {
				t.Fatal("unverified invoice replaced original")
			}
		})
	}
}

func TestManualInvoiceConcurrencyAndPaymentSettlesOriginalOnce(t *testing.T) {
	e, o, s, _ := invoiceFixture(t)
	ctx := context.Background()
	m := &invoiceMock{started: make(chan struct{}), proceed: make(chan struct{})}
	m.install(t)
	finished := make(chan error, 1)
	go func() { _, err := e.RegenerateManualCheckout(ctx, "admin", o.ID, true); finished <- err }()
	<-m.started
	if _, err := e.RegenerateManualCheckout(ctx, "admin", o.ID, true); err == nil {
		t.Error("concurrent generation was not blocked")
	}
	close(m.proceed)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if m.creates.Load() != 1 {
		t.Fatal("concurrent duplicate creation")
	}
	var cipher string
	_ = e.Store.DB.QueryRow(ctx, "SELECT job_cipher FROM orders WHERE id=$1", o.ID).Scan(&cipher)
	raw, _ := e.Store.Vault.Open("job:"+o.ID, cipher)
	var j Job
	_ = json.Unmarshal(raw, &j)
	e.NoCaptchaAuto, e.NoCaptchaToken = true, "synthetic-key"
	if result, err := e.x(ctx, o, s, &j, func() error { return nil }); err != nil || result.Status != "unknown" {
		t.Fatal(result, err)
	}
	m.paid = true
	result, err := e.x(ctx, o, s, &j, func() error { return nil })
	if err != nil || result.Status != "succeeded" || result.Receipt != "cs_live_new" {
		t.Fatal("manual payment did not settle the new session", result, err)
	}
	if _, err := e.Store.DB.Exec(ctx, "UPDATE orders SET lease_token='settle',lease_until=now()+interval '1 minute' WHERE id=$1", o.ID); err != nil {
		t.Fatal(err)
	}
	svc := &commerce.Service{Store: e.Store}
	if err := svc.Finish(ctx, o.ID, "settle", result.Status, result.Stage, result.Failure, result.Receipt); err != nil {
		t.Fatal(err)
	}
	if err := svc.Finish(ctx, o.ID, "", result.Status, result.Stage, result.Failure, result.Receipt); err != nil {
		t.Fatal(err)
	}
	var count, locks int
	_ = e.Store.DB.QueryRow(ctx, "SELECT count(*) FROM ledger WHERE order_id=$1 AND kind='consume'", o.ID).Scan(&count)
	_ = e.Store.DB.QueryRow(ctx, "SELECT count(*) FROM resource_locks WHERE order_id=$1", o.ID).Scan(&locks)
	if count != 1 || locks != 0 {
		t.Fatal("manual payment not settled once", count, locks)
	}
}
