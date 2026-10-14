package providers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"gptibo/platform/internal/commerce"
)

func captchaPage() Object {
	p := handoffPage()
	pi := obj(p["payment_intent"])
	pi["client_secret"] = "pi_original_secret_synthetic"
	pi["next_action"] = Object{"type": "use_stripe_sdk", "use_stripe_sdk": Object{
		"type": "intent_confirmation_challenge", "stripe_js": Object{
			"site_key": "synthetic-site-key", "rqdata": "synthetic-challenge",
			"verification_url": "/v1/payment_intents/pi_original/verify_challenge",
		},
	}}
	return p
}

func TestCaptchaRejectsOtherPaymentOrChallenge(t *testing.T) {
	o := commerce.Order{Recipient: "tester"}
	s := commerce.Snapshot{ProductConfig: Object{"months": float64(3), "amount_minor": float64(30000), "currency": "bdt", "stripe_merchant": "acct_fixture", "stripe_product": "prod_test"}}
	j := &Job{Session: "cs_live_test", Method: "pm_old"}
	for _, kind := range []string{"valid", "absolute_url", "3ds", "arkose", "wrong_amount", "wrong_method", "wrong_recipient", "wrong_session", "wrong_secret", "ssrf", "other_intent", "url_query", "no_sitekey"} {
		t.Run(kind, func(t *testing.T) {
			p := captchaPage()
			pi := obj(p["payment_intent"])
			sdk := obj(obj(pi["next_action"])["use_stripe_sdk"])
			js := obj(sdk["stripe_js"])
			switch kind {
			case "absolute_url":
				js["verification_url"] = "https://api.stripe.com/v1/payment_intents/pi_original/verify_challenge"
			case "3ds":
				sdk["type"] = "stripe_3ds2_fingerprint"
			case "arkose":
				js["captcha_vendor_name"] = "arkose"
			case "wrong_amount":
				pi["amount"] = float64(60000)
			case "wrong_method":
				pi["payment_method"] = "pm_other"
			case "wrong_recipient":
				p["success_url"] = "https://x.com/other/gift-premium/success"
			case "wrong_session":
				p["session_id"] = "cs_live_other"
			case "wrong_secret":
				pi["client_secret"] = "pi_other_secret_synthetic"
			case "ssrf":
				js["verification_url"] = "https://evil.invalid/v1/payment_intents/pi_original/verify_challenge"
			case "other_intent":
				js["verification_url"] = "/v1/payment_intents/pi_other/verify_challenge"
			case "url_query":
				js["verification_url"] = "/v1/payment_intents/pi_original/verify_challenge?redirect=1"
			case "no_sitekey":
				delete(js, "site_key")
			}
			_, code := parsePaymentChallenge(p, o, s, j)
			valid := kind == "valid" || kind == "absolute_url"
			if (code == "") != valid {
				t.Fatal(kind, code)
			}
		})
	}
}

func TestCaptchaAutoRunsOnceInManualLaneAndNeverSettlesFromSolver(t *testing.T) {
	for _, kind := range []string{"success", "solver_failure", "timeout", "changed", "verify_timeout", "rejected", "paused", "expired", "unsupported"} {
		t.Run(kind, func(t *testing.T) {
			e, o, s, j := handoffFixture(t)
			ctx := context.Background()
			e.NoCaptchaAuto = true
			e.NoCaptchaToken = "synthetic-nocaptcha-token"
			s.Provider.WritesEnabled = true
			b, _ := json.Marshal(s.Provider)
			if _, err := e.Store.DB.Exec(ctx, "INSERT INTO settings(id,payload_cipher) VALUES('provider',$1)", e.Store.Vault.Seal("setting:provider", b)); err != nil {
				t.Fatal(err)
			}
			old := client
			defer func() { client = old }()
			solves, verifies, inits := 0, 0, 0
			client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == "api.nocaptcha.io" {
					solves++
					if r.URL.String() != noCaptchaEndpoint || r.Header.Get("User-Token") != e.NoCaptchaToken {
						t.Fatal("solver destination or credential")
					}
					body, _ := io.ReadAll(r.Body)
					var payload Object
					_ = json.Unmarshal(body, &payload)
					if len(payload) != 5 || strings.Contains(string(body), "secret") || strings.Contains(string(body), "pm_old") || str(payload["sitekey"]) != "synthetic-site-key" || payload["need_ekey"] != true {
						t.Fatal("unexpected solver data")
					}
					if kind == "timeout" {
						return nil, errors.New("synthetic timeout")
					}
					if kind == "solver_failure" {
						return response(`{"status":0,"id":"failed-request-synthetic","msg":"synthetic private upstream error"}`), nil
					}
					if kind == "paused" {
						_, err := e.Store.DB.Exec(ctx, "UPDATE users SET status='banned' WHERE id=$1", o.UserID)
						if err != nil {
							t.Fatal(err)
						}
					}
					return response(`{"status":1,"id":"request-synthetic","data":{"generated_pass_UUID":"P1_synthetic","ekey":"E0_synthetic"}}`), nil
				}
				if r.URL.Host != "api.stripe.com" {
					t.Fatal("unexpected host")
				}
				switch {
				case strings.HasSuffix(r.URL.Path, "/poll"):
					return response(`{"session_id":"cs_live_test","livemode":true,"is_sandbox_merchant":false,"mode":"payment","success_url":"https://x.com/tester/gift-premium/success","state":"active","payment_object_status":"requires_action"}`), nil
				case strings.HasSuffix(r.URL.Path, "/init"):
					inits++
					if kind == "expired" {
						r := response(`{"error":{"code":"checkout_not_active_session"}}`)
						r.StatusCode = 400
						return r, nil
					}
					p := captchaPage()
					if kind == "changed" && inits == 2 {
						obj(obj(obj(p["payment_intent"])["next_action"])["use_stripe_sdk"])["stripe_js"].(map[string]any)["rqdata"] = "new-challenge"
					}
					if kind == "unsupported" {
						obj(obj(obj(p["payment_intent"])["next_action"])["use_stripe_sdk"])["type"] = "stripe_3ds2_fingerprint"
					}
					b, _ := json.Marshal(p)
					return response(string(b)), nil
				case r.URL.Path == "/v1/payment_intents/pi_original/verify_challenge":
					verifies++
					_ = r.ParseForm()
					if r.Form.Get("challenge_response_token") != "P1_synthetic" || r.Form.Get("challenge_response_ekey") != "E0_synthetic" || r.Form.Get("client_secret") != "pi_original_secret_synthetic" || r.Form.Get("captcha_vendor_name") != "hcaptcha" {
						t.Fatal("incorrect verification binding")
					}
					if kind == "verify_timeout" {
						return nil, errors.New("synthetic lost response")
					}
					if kind == "rejected" {
						return response(`{"status":"requires_action"}`), nil
					}
					return response(`{"status":"succeeded"}`), nil
				default:
					t.Fatal("unexpected new payment or card operation", r.URL.Path)
					return nil, nil
				}
			})}
			// First automatic pass only releases resources and queues the solve.
			if _, err := e.x(ctx, o, s, &j, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			if solves != 0 || inits != 0 {
				t.Fatal("solver blocked normal lane")
			}
			for i := 0; i < 3; i++ {
				r, err := e.x(ctx, o, s, &j, func() error { return nil })
				if err != nil || r.Status != "unknown" {
					t.Fatal("solver response settled order", r, err)
				}
			}
			wantSolves, wantVerifies := 1, 0
			if kind == "expired" || kind == "unsupported" {
				wantSolves = 0
			}
			if kind == "success" || kind == "verify_timeout" || kind == "rejected" {
				wantVerifies = 1
			}
			if solves != wantSolves || verifies != wantVerifies {
				t.Fatal("duplicated attempt", solves, verifies)
			}
			var state, evidence string
			if err := e.Store.DB.QueryRow(ctx, "SELECT status,evidence_cipher FROM payment_captcha_attempts WHERE order_id=$1", o.ID).Scan(&state, &evidence); err != nil {
				t.Fatal(err)
			}
			want := "failed"
			if kind == "success" {
				want = "submitted"
			}
			if kind == "timeout" || kind == "verify_timeout" {
				want = "unconfirmed"
			}
			if state != want {
				t.Fatal(state, want)
			}
			if kind != "expired" {
				if raw, err := e.Store.Vault.Open("captcha:"+o.ID, evidence); err != nil || !strings.Contains(string(raw), "next_action") {
					t.Fatal("challenge evidence missing", err)
				}
			}
			if kind == "solver_failure" {
				raw, err := e.Store.Vault.Open("captcha:"+o.ID, evidence)
				var requestID string
				if queryErr := e.Store.DB.QueryRow(ctx, "SELECT solver_request_id FROM payment_captcha_attempts WHERE order_id=$1", o.ID).Scan(&requestID); queryErr != nil {
					t.Fatal(queryErr)
				}
				if err != nil || !strings.Contains(string(raw), "synthetic private upstream error") || strings.Contains(string(raw), e.NoCaptchaToken) || requestID != "failed-request-synthetic" {
					t.Fatal("failed response or request ID not safely preserved", err)
				}
			}
			order, err := commerce.ScanOrder(e.Store.DB.QueryRow(ctx, "SELECT "+commerce.OrderColumns+" FROM orders WHERE id=$1", o.ID))
			if err != nil || order.CaptchaStatus != want {
				t.Fatal("details lost solver status", err)
			}
			public, _ := json.Marshal(order)
			if strings.Contains(string(public), "secret") || strings.Contains(string(public), "synthetic-site-key") || strings.Contains(string(public), e.NoCaptchaToken) || strings.Contains(string(public), "private upstream error") {
				t.Fatal("captcha credentials leaked")
			}
		})
	}
}
