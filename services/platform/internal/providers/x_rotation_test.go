package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gptibo/platform/internal/commerce"
	"net/http"
	"strings"
	"testing"
)

func declineFixture() Object {
	last := Object{"type": "card_error", "code": "card_declined", "decline_code": "insufficient_funds", "payment_method": Object{"id": "pm_old"}}
	pi := Object{"id": "pi_original", "livemode": true, "status": "requires_payment_method", "amount": float64(30000), "amount_received": float64(0), "currency": "bdt", "last_payment_error": last}
	return Object{"type": "card_error", "code": "card_declined", "decline_code": "insufficient_funds", "payment_intent": pi}
}

func TestDeclineNeedsBoundUnpaidEvidence(t *testing.T) {
	s := commerce.Snapshot{ProductConfig: Object{"amount_minor": float64(30000), "currency": "bdt", "stripe_merchant": "acct_fixture"}}
	for _, kind := range []string{"valid", "received", "missing_received", "wrong_amount", "wrong_currency", "wrong_method", "action", "processing", "fraud", "duplicate"} {
		t.Run(kind, func(t *testing.T) {
			d := declineFixture()
			pi := obj(d["payment_intent"])
			last := obj(pi["last_payment_error"])
			switch kind {
			case "received":
				pi["amount_received"] = float64(1)
			case "missing_received":
				delete(pi, "amount_received")
			case "wrong_amount":
				pi["amount"] = float64(60000)
			case "wrong_currency":
				pi["currency"] = "usd"
			case "wrong_method":
				last["payment_method"] = "pm_other"
			case "action":
				pi["next_action"] = Object{"type": "use_stripe_sdk"}
			case "processing":
				pi["status"] = "processing"
			case "fraud":
				last["decline_code"] = "fraudulent"
				d["decline_code"] = "fraudulent"
			case "duplicate":
				last["decline_code"] = "duplicate_transaction"
				d["decline_code"] = "duplicate_transaction"
			}
			j := Job{Stage: "submitted", Method: "pm_old"}
			if got := recordDecline(d, s, &j); got != (kind == "valid") {
				t.Fatalf("unsafe decline classification: %s %v", kind, got)
			}
			if kind != "valid" && j.Stage != "submitted" {
				t.Fatal("unknown payment checkpoint changed")
			}
		})
	}
}

func TestRefillSevenDollarBoundaryAndTenDollarAmount(t *testing.T) {
	for _, initial := range []float64{0, 6.99, 6.999999, 7, 7.000001, 7.01} {
		t.Run(fmt.Sprint(initial), func(t *testing.T) {
			e, o, s := orderFixture(t)
			s.Rule.RefillThresholdMinor = 700
			s.Rule.FundingMinor = 1000
			s.Rule.OpenAmountMinor = 2000
			current := initial
			posts := 0
			old := client
			defer func() { client = old }()
			client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				if strings.HasSuffix(r.URL.Path, "/products") {
					return response(`{"code":0,"data":[{"product_code":"TEST-BIN","min_amount":20,"open_fee":0.5,"recharge_fee":0}]}`), nil
				}
				if strings.HasSuffix(r.URL.Path, "/balance") {
					return response(`{"code":0,"data":{"currency":"USD","spendable_balance":100}}`), nil
				}
				if r.Method == "POST" {
					posts++
					var body Object
					_ = json.NewDecoder(r.Body).Decode(&body)
					if r.URL.Path != "/openapi/v1/cards/recharge" || number(body["amount"]) != 10 || !matchesID(body["card_id"], 42) {
						t.Fatal("wrong refill request")
					}
					current += 10
					return response(`{"code":0,"data":{"card_id":42}}`), nil
				}
				return response(fmt.Sprintf(`{"code":0,"data":{"id":42,"status":"ACTIVE","product_code":"TEST-BIN","available_amount":%g,"transaction_limit":10,"transaction_limit_type":"limited"}}`, current)), nil
			})}
			j := Job{Stage: "card"}
			id, err := e.card(context.Background(), o, s, &j, func() error { return nil })
			want := 0
			if initial < 7 {
				want = 1
			}
			if err != nil || id != 42 || posts != want {
				t.Fatalf("boundary failed: %v %d", err, posts)
			}
			if _, err = e.card(context.Background(), o, s, &j, func() error { return nil }); err != nil || posts != want {
				t.Fatal("resume repeated funding", err)
			}
		})
	}
}

func TestXDeclineRotatesOnceAndReusesNewCard(t *testing.T) {
	e, o, s := orderFixture(t)
	ctx := context.Background()
	o.Recipient = "tester"
	s.Business = "x"
	s.ProductConfig = Object{"months": float64(3), "amount_minor": float64(30000), "currency": "bdt", "stripe_merchant": "acct_fixture", "stripe_product": "prod_test"}
	s.Provider.Environment = "production"
	s.Provider.StripeKey = "pk_live_synthetic"
	s.Provider.XBearer = "Bearer synthetic"
	s.Provider.Billing.FirstName = "Test"
	s.Provider.Billing.LastName = "Owner"
	s.Rule.ReplaceOnDecline = true
	s.Rule.MaxReplacements = 2
	s.Rule.RefillThresholdMinor = 700
	s.Rule.FundingMinor = 1000
	s.Rule.OpenAmountMinor = 2000
	s.Rule.MaxOpenFeeMinor = 50
	s.Rule.DailyLimitMinor = 10000
	s.Rule.UseProviderLimits = true
	var page Object
	_ = json.Unmarshal([]byte(`{"session_id":"cs_live_test","account_settings":{"account_id":"acct_fixture"},"livemode":true,"mode":"payment","currency":"bdt","success_url":"https://x.com/tester/gift-premium/success","cancel_url":"https://x.com/tester/gift-premium","total_summary":{"total":30000,"subtotal":30000,"due":30000},"line_item_group":{"currency":"bdt","total":30000,"subtotal":30000,"due":30000,"line_items":[{"name":"Premium Gift - 3 months","quantity":1,"total":30000,"subtotal":30000,"price":{"currency":"bdt","type":"one_time","unit_amount":30000,"product":{"id":"prod_test","name":"Premium Gift - 3 months","livemode":true}}}]},"status":"open","payment_status":"unpaid","payment_intent":null,"init_checksum":"test-checksum"}`), &page)
	useXTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != "GET" {
			t.Fatal("created a new gift checkout on decline")
		}
		if strings.Contains(r.URL.Path, "useSubscriptionProductDetails") {
			fmt.Fprint(w, `{"data":{"web_subscription_product_details_by_rest_id":{"rest_id":"prod_test","prices":[{"amount_local_micro":300000000,"price_type":"OneTime","currency_code":"BDT"}]}}}`)
		} else {
			fmt.Fprint(w, `{"data":{"user":{"result":{"rest_id":"12345","core":{"screen_name":"tester"},"premium_gifting_eligible":true}}}}`)
		}
	})
	old := client
	defer func() { client = old }()
	opens, confirms := 0, 0
	client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/products"):
			return response(`{"code":0,"data":[{"product_code":"TEST-BIN","min_amount":20,"open_fee":0.5,"recharge_fee":0}]}`), nil
		case strings.HasSuffix(r.URL.Path, "/balance"):
			return response(`{"code":0,"data":{"currency":"USD","spendable_balance":100}}`), nil
		case strings.HasSuffix(r.URL.Path, "/cards/open"):
			opens++
			var body Object
			_ = json.NewDecoder(r.Body).Decode(&body)
			if number(body["init_amount"]) != 20 || body["transaction_limit"] != nil || r.Header.Get("Idempotency-Key") != o.ID+":card:1" {
				t.Fatal("replacement card policy not applied")
			}
			return response(`{"code":0,"data":{"id":43}}`), nil
		case strings.HasSuffix(r.URL.Path, "/cards/43"):
			return response(`{"code":0,"data":{"id":43,"status":"ACTIVE","product_code":"TEST-BIN","available_amount":20,"card_number":"4242424242424242","cvv":"123","expire":"12/30","limit":{"single_limit":0}}}`), nil
		case strings.HasSuffix(r.URL.Path, "/payment_methods"):
			if r.Header.Get("Idempotency-Key") != o.ID+":method:1" {
				t.Fatal("payment method key reused")
			}
			return response(`{"id":"pm_new","type":"card","livemode":true}`), nil
		case strings.HasSuffix(r.URL.Path, "/init"):
			if confirms > 0 {
				page["payment_intent"] = declineFixture()["payment_intent"]
			}
			b, _ := json.Marshal(page)
			return response(string(b)), nil
		case strings.HasSuffix(r.URL.Path, "/confirm"):
			confirms++
			_ = r.ParseForm()
			if confirms == 1 {
				b, _ := json.Marshal(Object{"error": declineFixture()})
				reply := response(string(b))
				reply.StatusCode = 402
				return reply, nil
			}
			if r.Header.Get("Idempotency-Key") != o.ID+":confirm:1" || r.Form.Get("payment_method") != "pm_new" {
				t.Fatal("confirm retry reused old card/key")
			}
			return response(`{}`), nil
		case strings.HasSuffix(r.URL.Path, "/poll"):
			return response(`{"session_id":"cs_live_test","livemode":true,"is_sandbox_merchant":false,"mode":"payment","success_url":"https://x.com/tester/gift-premium/success","state":"succeeded","payment_object_status":"succeeded"}`), nil
		default:
			t.Fatalf("unexpected provider request %s", r.URL.Path)
			return nil, errors.New("unexpected")
		}
	})}
	j := Job{Stage: "ready", CardID: 42, Method: "pm_old", Session: "cs_live_test", RecipientID: "12345", Proof: page, Account: Object{"auth_token": "synthetic", "ct0": "synthetic"}}
	save := func() error {
		b, err := json.Marshal(j)
		if err != nil {
			return err
		}
		return json.Unmarshal(b, &j)
	}
	result, err := e.x(ctx, o, s, &j, save)
	if err != nil || result.Status != "running" || j.CardAttempt != 1 || j.Stage != "card" {
		t.Fatalf("decline recovery not checkpointed: %+v %v", result, err)
	}
	result, err = e.x(ctx, o, s, &j, save)
	if err != nil || result.Status != "succeeded" || opens != 1 || confirms != 2 {
		t.Fatalf("replacement failed: %+v %v opens=%d", result, err, opens)
	}
	var active, retired int64
	if err = e.Store.DB.QueryRow(ctx, "SELECT card_id FROM card_rule_runtime").Scan(&active); err != nil || active != 43 {
		t.Fatal("replacement not reused", err)
	}
	if err = e.Store.DB.QueryRow(ctx, "SELECT card_id FROM card_retirements").Scan(&retired); err != nil || retired != 42 {
		t.Fatal("declined card not retired", err)
	}
	// A future order with the old configured card uses the persisted replacement.
	id, err := e.card(ctx, o, s, &Job{}, func() error { return nil })
	if err != nil || id != 43 || opens != 1 {
		t.Fatal("future card selection returned to declined card", err)
	}
}

func TestReplacementLimitRetiresCardWithoutFurtherSpending(t *testing.T) {
	e, o, s := orderFixture(t)
	s.Rule.ReplaceOnDecline = true
	s.Rule.MaxReplacements = 2
	j := Job{Stage: "declined", CardID: 42, CardAttempt: 2, Method: "pm_old", DeclinedMethod: "pm_old", DeclinedIntent: "pi_original", DeclineCode: "insufficient_funds"}
	for i := 0; i < 2; i++ {
		result, err := e.replaceDeclinedCard(context.Background(), o, s, &j, func() error { t.Fatal("exhausted attempt advanced"); return nil })
		if err != nil || result.Status != "unknown" || result.Failure != "card_replacement_limit" || j.CardID != 42 {
			t.Fatal("unbounded replacement", err)
		}
	}
	var count int
	if err := e.Store.DB.QueryRow(context.Background(), "SELECT count(*) FROM card_retirements").Scan(&count); err != nil || count != 1 {
		t.Fatal("retirement is not idempotent", err)
	}
}
