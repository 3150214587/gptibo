package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"gptibo/platform/internal/commerce"
)

func handoffPage() Object {
	var page Object
	_ = json.Unmarshal([]byte(`{"session_id":"cs_live_test","account_settings":{"account_id":"acct_fixture"},"livemode":true,"mode":"payment","currency":"bdt","success_url":"https://x.com/tester/gift-premium/success","cancel_url":"https://x.com/tester/gift-premium","total_summary":{"total":30000,"subtotal":30000,"due":30000},"line_item_group":{"currency":"bdt","total":30000,"subtotal":30000,"due":30000,"line_items":[{"name":"Premium Gift - 3 months","quantity":1,"total":30000,"subtotal":30000,"price":{"currency":"bdt","type":"one_time","unit_amount":30000,"product":{"id":"prod_test","name":"Premium Gift - 3 months","livemode":true}}}]},"status":"open","payment_status":"unpaid","init_checksum":"test-checksum","url":"https://checkout.stripe.com/c/pay/cs_live_test#original-capability","payment_intent":{"id":"pi_original","status":"requires_action","livemode":true,"amount":30000,"currency":"bdt","payment_method":"pm_old","next_action":{"type":"use_stripe_sdk"}}}`), &page)
	return page
}

func handoffFixture(t *testing.T) (*Engine, commerce.Order, commerce.Snapshot, Job) {
	t.Helper()
	e, o, s := orderFixture(t)
	o.ID = "handoff-order"
	o.Business, o.Recipient, o.Status, o.Stage = "x", "tester", "unknown", "submitted"
	s.Business, s.Provider.Environment, s.Provider.StripeKey = "x", "production", "pk_live_synthetic"
	s.ProductConfig = Object{"months": float64(3), "amount_minor": float64(30000), "currency": "bdt", "stripe_merchant": "acct_fixture", "stripe_product": "prod_test"}
	proof := handoffPage()
	delete(proof, "payment_intent")
	j := Job{Stage: "submitted", Session: "cs_live_test", Method: "pm_old", CardID: 42, Proof: proof}
	config, _ := json.Marshal(s)
	job, _ := json.Marshal(j)
	_, err := e.Store.DB.Exec(context.Background(), "INSERT INTO orders(id,user_id,merchant_order_no,idempotency_key,request_hash,product_code,business,supply_minor,recipient,status,stage,config_cipher,job_cipher) VALUES($1,'user','handoff','handoff','hash','x-3m','x',1700,'tester','unknown','submitted',$2,$3)", o.ID, e.Store.Vault.Seal("order:"+o.ID, config), e.Store.Vault.Seal("job:"+o.ID, job))
	if err != nil {
		t.Fatal(err)
	}
	return e, o, s, j
}

func TestHandoffOnlyOriginalPendingPayment(t *testing.T) {
	for _, kind := range []string{"valid", "card_reused", "busy", "terminal", "wrong_session", "wrong_recipient", "wrong_amount", "wrong_method", "already_paid", "evil_link", "different_session_link"} {
		t.Run(kind, func(t *testing.T) {
			e, o, _, _ := handoffFixture(t)
			ctx := context.Background()
			page := handoffPage()
			switch kind {
			case "card_reused":
				_, err := e.Store.DB.Exec(ctx, "INSERT INTO resource_locks(resource,order_id) VALUES('card:production:42','test-order')")
				if err != nil {
					t.Fatal(err)
				}
			case "busy":
				_, _ = e.Store.DB.Exec(ctx, "UPDATE orders SET lease_until=now()+interval '1 minute' WHERE id=$1", o.ID)
			case "terminal":
				_, _ = e.Store.DB.Exec(ctx, "UPDATE orders SET status='failed' WHERE id=$1", o.ID)
			case "wrong_session":
				page["session_id"] = "cs_live_other"
			case "wrong_recipient":
				page["success_url"] = "https://x.com/other/gift-premium/success"
			case "wrong_amount":
				obj(page["payment_intent"])["amount"] = float64(60000)
			case "wrong_method":
				obj(page["payment_intent"])["payment_method"] = "pm_other"
			case "already_paid":
				obj(page["payment_intent"])["status"] = "succeeded"
			case "evil_link":
				page["url"] = "https://checkout.stripe.com.evil.test/c/pay/cs_live_test#secret"
			case "different_session_link":
				page["url"] = "https://checkout.stripe.com/c/pay/cs_live_other#secret"
			}
			old := client
			defer func() { client = old }()
			requests := 0
			client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				requests++
				if r.Method != "POST" || r.URL.Path != "/v1/payment_pages/cs_live_test/init" {
					t.Fatal("created or confirmed payment", r.URL.Path)
				}
				b, _ := json.Marshal(page)
				return response(string(b)), nil
			})}
			link, err := e.ManualCheckout(ctx, "admin", o.ID)
			valid := kind == "valid" || kind == "card_reused"
			if valid {
				if err != nil || link != str(page["url"]) {
					t.Fatal(link, err)
				}
			} else if err == nil {
				t.Fatal("unsafe handoff", kind)
			}
			var manual bool
			var count int
			_ = e.Store.DB.QueryRow(ctx, "SELECT manual_payment_at IS NOT NULL FROM orders WHERE id=$1", o.ID).Scan(&manual)
			_ = e.Store.DB.QueryRow(ctx, "SELECT count(*) FROM audit WHERE action='payment_handoff'").Scan(&count)
			if manual != valid || count != map[bool]int{true: 1, false: 0}[valid] {
				t.Fatal("handoff not atomic", manual, count)
			}
			if kind == "card_reused" {
				var owner string
				if err := e.Store.DB.QueryRow(ctx, "SELECT order_id FROM resource_locks WHERE resource='card:production:42'").Scan(&owner); err != nil || owner != "test-order" {
					t.Fatal("manual checkout changed another order's card lock", owner, err)
				}
			}
			if (kind == "busy" || kind == "terminal") && requests != 0 {
				t.Fatal("queried invalid order")
			}
		})
	}
}

func TestManualPaymentPollsWithoutReconfirmingOrRotating(t *testing.T) {
	for _, state := range []string{"requires_action", "requires_payment_method", "succeeded"} {
		t.Run(state, func(t *testing.T) {
			e, o, s, j := handoffFixture(t)
			ctx := context.Background()
			_, err := e.Store.DB.Exec(ctx, "UPDATE orders SET manual_payment_at=now() WHERE id=$1", o.ID)
			if err != nil {
				t.Fatal(err)
			}
			old := client
			defer func() { client = old }()
			client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" || !strings.HasSuffix(r.URL.Path, "/poll") {
					t.Fatal("manual handoff submitted another payment")
				}
				poll := Object{"session_id": j.Session, "livemode": true, "is_sandbox_merchant": false, "mode": "payment", "success_url": "https://x.com/tester/gift-premium/success", "state": "active", "payment_object_status": state, "payment_intent": declineFixture()["payment_intent"]}
				if state == "succeeded" {
					poll["state"] = "succeeded"
				}
				b, _ := json.Marshal(poll)
				return response(string(b)), nil
			})}
			result, err := e.x(ctx, o, s, &j, func() error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]string{"requires_action": "payment_manual_action", "requires_payment_method": "payment_manual_review", "succeeded": ""}[state]
			if result.Failure != want || j.CardAttempt != 0 || (result.Status == "succeeded") != (state == "succeeded") {
				t.Fatal(result, j.CardAttempt)
			}
		})
	}
}

func TestCheckoutURLRejectsUnboundCapabilities(t *testing.T) {
	for _, raw := range []string{"http://checkout.stripe.com/c/pay/cs_live_test#x", "https://user@checkout.stripe.com/c/pay/cs_live_test#x", "https://checkout.stripe.com:443/c/pay/cs_live_test#x", "https://checkout.stripe.com/c/pay/cs_live_test", "https://checkout.stripe.com/c/pay/cs_live_other#x"} {
		if validCheckoutURL(raw, "cs_live_test") {
			t.Fatal(raw)
		}
	}
}

func TestExpiredChallengeKeepsOriginalManualCheckout(t *testing.T) {
	for _, manual := range []bool{false, true} {
		e, o, _, _ := handoffFixture(t)
		ctx := context.Background()
		if manual {
			if _, err := e.Store.DB.Exec(ctx, "UPDATE orders SET manual_payment_at=now() WHERE id=$1", o.ID); err != nil {
				t.Fatal(err)
			}
		}
		page := handoffPage()
		pi := obj(page["payment_intent"])
		pi["status"] = "requires_payment_method"
		pi["payment_method"] = nil
		pi["next_action"] = nil
		old := client
		client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
			if !strings.HasSuffix(r.URL.Path, "/init") {
				t.Fatal("payment submitted")
			}
			b, _ := json.Marshal(page)
			return response(string(b)), nil
		})}
		link, err := e.ManualCheckout(ctx, "admin", o.ID)
		client = old
		if manual && (err != nil || link != str(page["url"])) {
			t.Fatal("expired manual challenge stranded", err)
		}
		if !manual && err == nil {
			t.Fatal("unconfirmed payment was handed off")
		}
	}
}
