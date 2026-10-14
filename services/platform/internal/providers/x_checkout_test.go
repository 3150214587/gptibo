package providers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"gptibo/platform/internal/commerce"
)

func checkoutFixture(t *testing.T, stage string, attempts int) (*Engine, commerce.Order, commerce.Snapshot, Job) {
	t.Helper()
	e, o, s, _ := invoiceFixture(t)
	o.Stage = stage
	j := Job{Stage: stage, CheckoutCreateAttempts: attempts, AccountID: "synthetic-account", Account: Object{"auth_token": "synthetic-auth", "ct0": "synthetic-csrf"}, RecipientID: "12345"}
	setCheckoutJob(t, e, o.ID, j)
	return e, o, s, j
}

func setCheckoutJob(t *testing.T, e *Engine, id string, j Job) {
	t.Helper()
	b, _ := json.Marshal(j)
	if _, err := e.Store.DB.Exec(context.Background(), "UPDATE orders SET stage=$2,job_cipher=$3,manual_payment_at=NULL,lease_token='checkout-test',lease_until=now()+interval '5 minutes' WHERE id=$1", id, j.Stage, e.Store.Vault.Seal("job:"+id, b)); err != nil {
		t.Fatal(err)
	}
}

func loadCheckoutJob(t *testing.T, e *Engine, id string) Job {
	t.Helper()
	var cipher string
	if err := e.Store.DB.QueryRow(context.Background(), "SELECT job_cipher FROM orders WHERE id=$1", id).Scan(&cipher); err != nil {
		t.Fatal(err)
	}
	b, err := e.Store.Vault.Open("job:"+id, cipher)
	var j Job
	if err != nil || json.Unmarshal(b, &j) != nil {
		t.Fatal("checkpoint cannot be read", err)
	}
	return j
}

type checkoutMock struct {
	kind    string
	creates int
	reads   int
}

func (m *checkoutMock) install(t *testing.T, e *Engine, id string) {
	t.Helper()
	old, oldX := client, xClient
	t.Cleanup(func() { client, xClient = old, oldX })
	client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "x.com" {
			switch {
			case strings.HasSuffix(r.URL.Path, "/PremiumGiftingQuery"):
				m.reads++
				if m.kind == "ineligible" {
					return response(`{"data":{"user":{"result":{"rest_id":"12345","core":{"screen_name":"tester"},"premium_gifting_eligible":false}}}}`), nil
				}
				if m.kind == "recipient_changed" {
					return response(`{"data":{"user":{"result":{"rest_id":"99999","core":{"screen_name":"tester"},"premium_gifting_eligible":true}}}}`), nil
				}
				return response(`{"data":{"user":{"result":{"rest_id":"12345","core":{"screen_name":"tester"},"premium_gifting_eligible":true}}}}`), nil
			case strings.HasSuffix(r.URL.Path, "/useSubscriptionProductDetailsByRestIdQuery"):
				if m.kind == "price_changed" {
					return response(`{"data":{"web_subscription_product_details_by_rest_id":{"rest_id":"prod_test","prices":[{"amount_local_micro":600000000,"currency_code":"BDT","price_type":"OneTime"}]}}}`), nil
				}
				return response(`{"data":{"web_subscription_product_details_by_rest_id":{"rest_id":"prod_test","prices":[{"amount_local_micro":300000000,"currency_code":"BDT","price_type":"OneTime"}]}}}`), nil
			case strings.HasSuffix(r.URL.Path, "/useOneTimePurchaseGiftMutation"):
				m.creates++
				persisted := loadCheckoutJob(t, e, id)
				if persisted.Stage != "creating" || persisted.CheckoutCreateAttempts != m.creates {
					t.Fatal("creation dispatched without a durable attempt budget", persisted.CheckoutCreateAttempts)
				}
				var body Object
				_ = json.NewDecoder(r.Body).Decode(&body)
				v := obj(body["variables"])
				if str(v["gift_recipient"]) != "12345" || str(v["external_product_id"]) != "prod_test" || str(v["success_url"]) != "https://x.com/tester/gift-premium/success" {
					t.Fatal("retry changed recipient or product")
				}
				if m.kind == "partial" {
					return response(`{"errors":[{"code":131}],"data":{"onetimepurchase_gift":{"session_id":"cs_live_original"}}}`), nil
				}
				if m.kind == "malformed" {
					return response(`{"data":{"onetimepurchase_gift":{"session_id":"cs_live_original","session_status":"Unpaid","session_url":"https://evil.invalid/pay/cs_live_original"}}}`), nil
				}
				if (m.creates == 1 && m.kind != "initial_success") || m.kind == "twice_failed" {
					return nil, errors.New("synthetic lost creation response")
				}
				return response(`{"data":{"onetimepurchase_gift":{"session_id":"cs_live_retry","session_status":"Unpaid","session_url":"https://checkout.stripe.com/c/pay/cs_live_retry#synthetic"}}}`), nil
			}
		}
		// Stop after checkout recovery: these tests must never issue card or
		// payment writes. Existing payment integration tests cover that path.
		if r.URL.Path == "/v1/payment_pages/cs_live_retry/init" {
			return nil, errors.New("synthetic init unavailable")
		}
		t.Error("unexpected payment or card request", r.Method, r.URL.Host, r.URL.Path)
		return nil, errors.New("unexpected request")
	})}
	xClient = func(Object) (*http.Client, error) { return client, nil }
}

func TestXCheckoutRetriesOnceAcrossWorkerRestarts(t *testing.T) {
	for _, kind := range []string{"initial_success", "success", "twice_failed", "partial", "malformed"} {
		t.Run(kind, func(t *testing.T) {
			e, o, _, _ := checkoutFixture(t, "preflight", 0)
			m := &checkoutMock{kind: kind}
			m.install(t, e, o.ID)
			for run := 0; run < 3; run++ {
				var config, job string
				if err := e.Store.DB.QueryRow(context.Background(), "SELECT config_cipher,job_cipher FROM orders WHERE id=$1", o.ID).Scan(&config, &job); err != nil {
					t.Fatal(err)
				}
				if _, err := e.Store.DB.Exec(context.Background(), "UPDATE orders SET lease_token='checkout-test',lease_until=now()+interval '5 minutes' WHERE id=$1", o.ID); err != nil {
					t.Fatal(err)
				}
				// Reconstruct the engine and reload only the stored checkpoint.
				restarted := &Engine{Store: e.Store}
				result := restarted.Execute(context.Background(), o, "checkout-test", config, "", job)
				if result.Status != "unknown" {
					t.Fatal("unknown result settled as terminal", result)
				}
				if err := (&commerce.Service{Store: e.Store}).Finish(context.Background(), o.ID, "checkout-test", result.Status, result.Stage, result.Failure, result.Receipt); err != nil {
					t.Fatal(err)
				}
			}
			want := 2
			if kind == "partial" || kind == "malformed" || kind == "initial_success" {
				want = 1
			}
			j := loadCheckoutJob(t, e, o.ID)
			if m.creates != want || j.CheckoutCreateAttempts != want {
				t.Fatal("creation budget reset or extra checkout created", m.creates, j.CheckoutCreateAttempts)
			}
			if kind == "success" && (j.Stage != "checkout" || j.Session != "cs_live_retry" || m.reads != 2) {
				t.Fatal("retry did not preserve checkout or recheck eligibility", j.Stage, m.reads)
			}
			if (kind == "partial" || kind == "malformed") && (!j.CheckoutCreateEvidence || j.Session != "cs_live_original") {
				t.Fatal("captured partial checkout evidence lost")
			}
			var attempts, settlements int
			_ = e.Store.DB.QueryRow(context.Background(), "SELECT count(*) FROM audit WHERE target=$1 AND action='x_checkout_creation_attempt'", o.ID).Scan(&attempts)
			_ = e.Store.DB.QueryRow(context.Background(), "SELECT count(*) FROM ledger WHERE order_id=$1 AND kind IN ('consume','release')", o.ID).Scan(&settlements)
			if attempts != want || settlements != 0 {
				t.Fatal("missing attempt audit or premature settlement", attempts, settlements)
			}
		})
	}
}

func TestXCheckoutRetryDispatchInterruptionConsumesBudget(t *testing.T) {
	e, o, s, j := checkoutFixture(t, "creating", 1)
	m := &checkoutMock{kind: "success"}
	m.install(t, e, o.ID)
	ctx := context.Background()
	err := e.retryXCheckout(ctx, o, s, &j, func() error {
		if err := e.checkpoint(ctx, o.ID, "checkout-test", j); err != nil {
			return err
		}
		return errors.New("synthetic interruption after checkpoint")
	})
	if err == nil || m.creates != 0 {
		t.Fatal("interrupted dispatch created a checkout", err, m.creates)
	}
	reloaded := loadCheckoutJob(t, e, o.ID)
	if reloaded.CheckoutCreateAttempts != 2 {
		t.Fatal("retry budget not saved before dispatch")
	}
	if (&Engine{Store: e.Store}).retryXCheckout(ctx, o, s, &reloaded, func() error { t.Fatal("saved after exhausted budget"); return nil }) == nil || m.creates != 0 {
		t.Fatal("restart dispatched an additional retry")
	}
}

func TestXCheckoutRetryGuardsPaymentEvidenceAndRevalidation(t *testing.T) {
	for _, kind := range []string{"legacy", "budget", "session", "partial", "method", "card", "proof", "upstream", "manual_invoice", "malformed", "manual", "card_operation", "settled", "ineligible", "recipient_changed", "price_changed", "lease_lost", "paused"} {
		t.Run(kind, func(t *testing.T) {
			e, o, s, j := checkoutFixture(t, "creating", 1)
			switch kind {
			case "legacy":
				j.CheckoutCreateAttempts = 0
			case "budget":
				j.CheckoutCreateAttempts = 2
			case "session":
				j.Session = "cs_live_original"
			case "partial":
				j.CheckoutCreateEvidence = true
			case "method":
				j.Method = "pm_original"
			case "card":
				j.CardID = 42
			case "proof":
				j.Proof = handoffPage()
			case "upstream":
				j.UpstreamRef = "pi_original"
			case "manual_invoice":
				j.ManualInvoiceID = "invoice_original"
			case "malformed":
				j.CheckoutCreateFailure = "checkout_unconfirmed"
			}
			setCheckoutJob(t, e, o.ID, j)
			ctx := context.Background()
			switch kind {
			case "manual":
				_, _ = e.Store.DB.Exec(ctx, "UPDATE orders SET manual_payment_at=now() WHERE id=$1", o.ID)
			case "card_operation":
				_, _ = e.Store.DB.Exec(ctx, "INSERT INTO card_operations(reference,order_id,rule_id,operation,amount_minor,status) VALUES('checkout-card',$1,'rule','open',2000,'unknown')", o.ID)
			case "settled":
				_, _ = e.Store.DB.Exec(ctx, "UPDATE orders SET status='failed' WHERE id=$1", o.ID)
			case "lease_lost":
				_, _ = e.Store.DB.Exec(ctx, "UPDATE orders SET lease_until=now()-interval '1 second' WHERE id=$1", o.ID)
			case "paused":
				s.Provider.WritesEnabled = false
				b, _ := json.Marshal(s.Provider)
				_, _ = e.Store.DB.Exec(ctx, "UPDATE settings SET payload_cipher=$1 WHERE id='provider'", e.Store.Vault.Seal("setting:provider", b))
			}
			m := &checkoutMock{kind: kind}
			m.install(t, e, o.ID)
			if err := e.retryXCheckout(ctx, o, s, &j, func() error { return e.checkpoint(ctx, o.ID, "checkout-test", j) }); err == nil {
				t.Fatal("unsafe retry accepted")
			}
			if m.creates != 0 {
				t.Fatal("unsafe checkout request dispatched", m.creates)
			}
		})
	}
}
