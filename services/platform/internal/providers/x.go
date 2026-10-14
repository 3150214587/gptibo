package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"golang.org/x/net/proxy"
	"gptibo/platform/internal/commerce"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var xClient = makeXClient

func makeXClient(account Object) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	p := str(account["proxy"])
	if p != "" {
		u, e := url.Parse(p)
		if e != nil {
			return nil, e
		}
		if u.Scheme == "http" {
			transport.Proxy = http.ProxyURL(u)
		} else if u.Scheme == "socks5" {
			d, e := proxy.FromURL(u, proxy.Direct)
			if e != nil {
				return nil, e
			}
			if cd, ok := d.(proxy.ContextDialer); ok {
				transport.DialContext = cd.DialContext
			} else {
				return nil, errors.New("proxy lacks context support")
			}
		} else {
			return nil, errors.New("invalid proxy")
		}
	}
	return &http.Client{Transport: transport, Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}
func xQuery(ctx context.Context, s commerce.Snapshot, account Object, operation, query string, variables Object) (Object, error) {
	if s.Provider.XBearer == "" {
		return nil, &UpstreamError{"x_bearer_missing", true}
	}
	features := Object{"subscriptions_marketing_page_fetch_promotions": true}
	mutation := strings.Contains(operation, "Mutation")
	target := "https://x.com/i/api/graphql/" + query + "/" + operation
	method := "GET"
	var body []byte
	if mutation {
		method = "POST"
		body, _ = json.Marshal(Object{"variables": variables, "features": features, "queryId": query})
	} else {
		v, _ := json.Marshal(variables)
		f, _ := json.Marshal(features)
		target += "?" + url.Values{"variables": {string(v)}, "features": {string(f)}}.Encode()
	}
	headers := map[string]string{"Authorization": s.Provider.XBearer, "Cookie": "auth_token=" + str(account["auth_token"]) + "; ct0=" + str(account["ct0"]), "X-Csrf-Token": str(account["ct0"]), "X-Twitter-Auth-Type": "OAuth2Session", "X-Twitter-Active-User": "yes", "X-Twitter-Client-Language": "en", "Content-Type": "application/json", "Origin": "https://x.com", "Referer": "https://x.com/", "User-Agent": "Mozilla/5.0"}
	c, e := xClient(account)
	if e != nil {
		return nil, e
	}
	o, status, e := readJSON(ctx, c, method, target, headers, body)
	errs, errorsArray := o["errors"].([]any)
	if e != nil || status != 200 || len(errs) > 0 || o["errors"] != nil && !errorsArray || o["data"] == nil {
		return obj(o["data"]), &UpstreamError{"x_query_unconfirmed", false}
	}
	return obj(o["data"]), nil
}
func xEligibility(ctx context.Context, s commerce.Snapshot, account Object, recipient string) (Object, error) {
	o, e := xQuery(ctx, s, account, "PremiumGiftingQuery", "kn8hCE6bHstQV2MtfYDTKg", Object{"screenName": recipient})
	if e != nil {
		return nil, e
	}
	row := obj(obj(o["user"])["result"])
	id := str(row["rest_id"])
	if !regexp.MustCompile(`^\d{1,25}$`).MatchString(id) || strings.ToLower(str(obj(row["core"])["screen_name"])) != recipient {
		return nil, &UpstreamError{"recipient_unconfirmed", false}
	}
	eligible, ok := row["premium_gifting_eligible"].(bool)
	if !ok {
		return nil, &UpstreamError{"eligibility_unconfirmed", false}
	}
	return Object{"recipient": recipient, "recipient_id": id, "eligible": eligible}, nil
}
func stripe(ctx context.Context, s commerce.Snapshot, method, path string, fields url.Values, idem string) (Object, error) {
	fields.Set("key", s.Provider.StripeKey)
	target := "https://api.stripe.com/v1/" + path
	var body []byte
	if method == "GET" {
		target += "?" + fields.Encode()
	} else {
		body = []byte(fields.Encode())
	}
	headers := map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
	if idem != "" {
		headers["Idempotency-Key"] = idem
	}
	o, status, e := readJSON(ctx, client, method, target, headers, body)
	if e == nil && status == 400 && str(obj(o["error"])["code"]) == "checkout_not_active_session" && (strings.HasSuffix(path, "/init") || strings.HasSuffix(path, "/poll")) {
		return nil, &UpstreamError{"checkout_inactive", false}
	}
	if e == nil && status == 402 && method == "POST" && strings.HasSuffix(path, "/confirm") {
		detail := obj(o["error"])
		if str(detail["type"]) == "card_error" && str(detail["code"]) == "card_declined" {
			return nil, &stripeCardDecline{detail: detail}
		}
	}
	if e != nil || status != 200 || o["error"] != nil {
		return nil, &UpstreamError{"stripe_result_unconfirmed", false}
	}
	return o, nil
}
func guardPage(p Object, o commerce.Order, s commerce.Snapshot, j *Job) error {
	cfg := s.ProductConfig
	amount := number(cfg["amount_minor"])
	currency := str(cfg["currency"])
	months := int(number(cfg["months"]))
	success := "https://x.com/" + o.Recipient + "/gift-premium/success"
	total, group := obj(p["total_summary"]), obj(p["line_item_group"])
	items, _ := group["line_items"].([]any)
	if str(s.ProductConfig["stripe_merchant"]) == "" || str(p["session_id"]) != j.Session || str(obj(p["account_settings"])["account_id"]) != str(s.ProductConfig["stripe_merchant"]) || !truth(p["livemode"]) || str(p["mode"]) != "payment" || str(p["currency"]) != currency || str(group["currency"]) != currency || str(p["success_url"]) != success || str(p["cancel_url"]) != strings.TrimSuffix(success, "/success") || p["setup_future_usage"] != nil || p["subscription_data"] != nil || p["setup_intent"] != nil || number(total["total"]) != amount || number(total["subtotal"]) != amount || number(group["total"]) != amount || number(group["subtotal"]) != amount || len(items) != 1 {
		return &UpstreamError{"checkout_identity_mismatch", false}
	}
	item := obj(items[0])
	price := obj(item["price"])
	product := obj(price["product"])
	name := fmt.Sprintf("Premium Gift - %d months", months)
	if str(item["name"]) != name || number(item["quantity"]) != 1 || number(item["total"]) != amount || number(item["subtotal"]) != amount || str(price["currency"]) != currency || str(price["type"]) != "one_time" || number(price["unit_amount"]) != amount || price["recurring"] != nil || str(product["id"]) != str(cfg["stripe_product"]) || str(product["name"]) != name || !truth(product["livemode"]) || str(p["status"]) != "open" || str(p["payment_status"]) != "unpaid" || !retryPageIntentMatches(p["payment_intent"], s, j) || number(total["due"]) != amount || number(group["due"]) != amount || str(p["init_checksum"]) == "" {
		return &UpstreamError{"checkout_amount_mismatch", false}
	}
	return nil
}
func (e *Engine) assignAccount(ctx context.Context, o commerce.Order, j *Job) error {
	return e.Store.Tx(ctx, func(tx pgx.Tx) error {
		var id, cipher string
		previous := tx.QueryRow(ctx, "SELECT account_id,payload_cipher FROM account_usage WHERE order_id=$1", o.ID).Scan(&id, &cipher)
		if previous == nil {
			data, err := e.Store.Vault.Open("x-account:"+id, cipher)
			if err != nil {
				return err
			}
			j.AccountID = id
			return json.Unmarshal(data, &j.Account)
		}
		if !errors.Is(previous, pgx.ErrNoRows) {
			return previous
		}
		err := tx.QueryRow(ctx, "SELECT a.id,a.payload_cipher FROM x_accounts a WHERE a.enabled AND NOT EXISTS(SELECT 1 FROM resource_locks WHERE resource='x-account:'||a.id) AND (SELECT count(*) FROM account_usage u WHERE u.account_id=a.id AND u.day=(now() AT TIME ZONE 'Asia/Shanghai')::date)<a.daily_limit ORDER BY a.id FOR UPDATE SKIP LOCKED LIMIT 1").Scan(&id, &cipher)
		if err != nil {
			return &UpstreamError{"resource_busy", false}
		}
		if _, err = tx.Exec(ctx, "INSERT INTO resource_locks(resource,order_id) VALUES($1,$2)", "x-account:"+id, o.ID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO account_usage(account_id,order_id,payload_cipher) VALUES($1,$2,$3)", id, o.ID, cipher); err != nil {
			return err
		}
		data, err := e.Store.Vault.Open("x-account:"+id, cipher)
		if err != nil {
			return err
		}
		j.AccountID = id
		return json.Unmarshal(data, &j.Account)
	})
}
func (e *Engine) x(ctx context.Context, o commerce.Order, s commerce.Snapshot, j *Job, save func() error) (Result, error) {
	if j.ManualInvoiceID != "" && j.Stage != "submitted" && j.Stage != "paid" {
		return Result{}, &UpstreamError{"manual_invoice_state_invalid", false}
	}
	if str(s.ProductConfig["stripe_merchant"]) == "" {
		return Result{}, &UpstreamError{"x_configuration_missing", true}
	}
	if s.Provider.Environment != "production" || !regexp.MustCompile(`^pk_live_[A-Za-z0-9]+$`).MatchString(s.Provider.StripeKey) {
		return Result{}, &UpstreamError{"x_configuration_missing", true}
	}
	cfg := s.ProductConfig
	months := int(number(cfg["months"]))
	amount := number(cfg["amount_minor"])
	currency := str(cfg["currency"])
	if currency != "bdt" || months != 3 && months != 6 || amount != float64(months*10000) {
		return Result{}, &UpstreamError{"x_product_unconfirmed", true}
	}
	success := "https://x.com/" + o.Recipient + "/gift-premium/success"
	if j.Stage == "declined" {
		return e.replaceDeclinedCard(ctx, o, s, j, save)
	}
	if j.Stage == "creating" {
		if err := e.retryXCheckout(ctx, o, s, j, save); err != nil {
			return Result{}, err
		}
	}
	if j.Stage == "preflight" {
		if err := e.resource(ctx, o.ID, "x-recipient-name:"+o.Recipient); err != nil {
			return Result{}, err
		}
		if j.AccountID == "" {
			if err := e.assignAccount(ctx, o, j); err != nil {
				return Result{}, err
			}
			if err := save(); err != nil {
				return Result{}, err
			}
		}
		check, err := xEligibility(ctx, s, j.Account, o.Recipient)
		if err != nil {
			return Result{}, err
		}
		if !truth(check["eligible"]) {
			return Result{}, &UpstreamError{"not_eligible", true}
		}
		if s.ConfirmedRecipientID != "" && str(check["recipient_id"]) != s.ConfirmedRecipientID {
			return Result{}, &UpstreamError{"recipient_changed", true}
		}
		j.RecipientID = str(check["recipient_id"])
		if err = e.resource(ctx, o.ID, "x-recipient:"+j.RecipientID); err != nil {
			return Result{}, err
		}
		if err = xQuote(ctx, s, j.Account); err != nil {
			return Result{}, err
		}
		if err = e.createXCheckout(ctx, o, s, j, save); err != nil {
			return Result{}, err
		}
	}
	if j.Stage == "creating" || j.Stage == "tokenizing" {
		return Result{}, &UpstreamError{"original_request_unconfirmed", false}
	}
	if j.Stage == "checkout" {
		page, err := stripe(ctx, s, "POST", "payment_pages/"+j.Session+"/init", url.Values{"browser_locale": {"en"}, "redirect_type": {"url"}}, "")
		if err != nil {
			return Result{}, err
		}
		if err = guardPage(page, o, s, j); err != nil {
			return Result{}, err
		}
		j.Proof = page
		j.Checksum = str(page["init_checksum"])
		j.Stage = "card"
		if err = save(); err != nil {
			return Result{}, err
		}
	}
	if j.Stage == "card" || j.Stage == "funding" {
		card, err := e.card(ctx, o, s, j, save)
		if err != nil {
			return Result{}, err
		}
		j.CardID = card
		j.Stage = "funded"
		if err = save(); err != nil {
			return Result{}, err
		}
	}
	if j.Stage == "funded" {
		card, err := cardAPI(ctx, s.Provider, "GET", fmt.Sprintf("/cards/%d?sync=1", j.CardID), nil, "")
		if err != nil {
			return Result{}, err
		}
		row := obj(card["data"])
		expiry := strings.Split(str(row["expire"]), "/")
		if len(expiry) != 2 || !matchesID(row["id"], j.CardID) || !regexp.MustCompile(`^\d{12,19}$`).MatchString(str(row["card_number"])) || !regexp.MustCompile(`^\d{3,4}$`).MatchString(str(row["cvv"])) || !regexp.MustCompile(`^(0[1-9]|1[0-2])$`).MatchString(expiry[0]) || !regexp.MustCompile(`^(\d{2}|\d{4})$`).MatchString(expiry[1]) || str(row["product_code"]) != s.Rule.CardProduct || str(row["status"]) != "ACTIVE" || !cardLimitsReady(row, s.Rule) {
			return Result{}, &UpstreamError{"card_not_ready", false}
		}
		b := s.Provider.Billing
		j.Stage = "tokenizing"
		if err = save(); err != nil {
			return Result{}, err
		}
		method, err := stripe(ctx, s, "POST", "payment_methods", url.Values{"type": {"card"}, "card[number]": {str(row["card_number"])}, "card[cvc]": {str(row["cvv"])}, "card[exp_month]": {expiry[0]}, "card[exp_year]": {expiry[1]}, "billing_details[name]": {b.FirstName + " " + b.LastName}, "billing_details[email]": {b.Email}, "billing_details[address][country]": {b.Country}, "billing_details[address][line1]": {b.Line1}, "billing_details[address][city]": {b.City}, "billing_details[address][state]": {b.State}, "billing_details[address][postal_code]": {b.Postal}}, paymentAttemptKey(o.ID, "method", j.CardAttempt))
		if err != nil {
			return Result{}, err
		}
		j.Method = str(method["id"])
		if !truth(method["livemode"]) || str(method["type"]) != "card" || !strings.HasPrefix(j.Method, "pm_") {
			return Result{}, &UpstreamError{"payment_method_unconfirmed", false}
		}
		j.Stage = "ready"
		if err = save(); err != nil {
			return Result{}, err
		}
	}
	if j.Stage == "ready" {
		check, err := xEligibility(ctx, s, j.Account, o.Recipient)
		if err != nil {
			return Result{}, err
		}
		if !truth(check["eligible"]) || str(check["recipient_id"]) != j.RecipientID {
			return Result{}, &UpstreamError{"recipient_changed", true}
		}
		if err = xQuote(ctx, s, j.Account); err != nil {
			return Result{}, err
		}
		page, err := stripe(ctx, s, "POST", "payment_pages/"+j.Session+"/init", url.Values{"browser_locale": {"en"}, "redirect_type": {"url"}}, "")
		if err != nil {
			return Result{}, err
		}
		if err = guardPage(page, o, s, j); err != nil {
			return Result{}, err
		}
		j.Proof = page
		j.Checksum = str(page["init_checksum"])
		j.Stage = "submitted"
		if err = save(); err != nil {
			return Result{}, err
		}
		_, err = stripe(ctx, s, "POST", "payment_pages/"+j.Session+"/confirm", url.Values{"payment_method": {j.Method}, "expected_amount": {fmt.Sprintf("%.0f", amount)}, "expected_payment_method_type": {"card"}, "init_checksum": {j.Checksum}, "return_url": {success}}, paymentAttemptKey(o.ID, "confirm", j.CardAttempt))
		if err != nil {
			var declined *stripeCardDecline
			if errors.As(err, &declined) && recordDecline(declined.detail, s, j) {
				if err = save(); err != nil {
					return Result{}, err
				}
				return e.replaceDeclinedCard(ctx, o, s, j, save)
			}
			return Result{}, err
		}
	}
	if j.Stage == "submitted" || j.Stage == "paid" {
		if err := guardPage(j.Proof, o, s, j); err != nil {
			return Result{}, err
		}
		poll, err := stripe(ctx, s, "GET", "payment_pages/"+j.Session+"/poll", url.Values{}, "")
		if err != nil {
			if cancelErr := e.cancelUnavailableCaptcha(ctx, o.ID); cancelErr != nil {
				return Result{}, cancelErr
			}
			return Result{}, err
		}
		if str(poll["session_id"]) != j.Session || !truth(poll["livemode"]) || poll["is_sandbox_merchant"] != false || str(poll["mode"]) != "payment" || str(poll["success_url"]) != success {
			return Result{}, &UpstreamError{"payment_evidence_mismatch", false}
		}
		if poll["currency"] != nil && str(poll["currency"]) != currency || poll["amount"] != nil && number(poll["amount"]) != amount || poll["account_id"] != nil && str(poll["account_id"]) != str(s.ProductConfig["stripe_merchant"]) {
			return Result{}, &UpstreamError{"payment_evidence_mismatch", false}
		}
		if str(poll["state"]) != "succeeded" || str(poll["payment_object_status"]) != "succeeded" {
			if str(poll["payment_object_status"]) == "requires_action" || str(poll["payment_object_status"]) == "requires_payment_method" {
				var manual bool
				var previousFailure string
				if err := e.Store.DB.QueryRow(ctx, "SELECT manual_payment_at IS NOT NULL,failure_code FROM orders WHERE id=$1", o.ID).Scan(&manual, &previousFailure); err != nil {
					return Result{}, err
				}
				if manual || str(poll["payment_object_status"]) == "requires_action" {
					if err := e.isolatePayment(ctx, o.ID, j.CardID); err != nil {
						return Result{}, err
					}
					if str(poll["payment_object_status"]) == "requires_action" && j.ManualInvoiceID == "" {
						if err := e.queuePaymentCaptcha(ctx, o.ID); err != nil {
							return Result{}, err
						}
					}
				}
				if manual {
					if str(poll["payment_object_status"]) == "requires_action" && j.ManualInvoiceID == "" {
						if err := e.solvePaymentCaptcha(ctx, o, s, j, save); err != nil {
							return Result{}, err
						}
					}
					failure := "payment_manual_review"
					if str(poll["payment_object_status"]) == "requires_action" {
						failure = "payment_manual_action"
						if previousFailure == "payment_action_required" {
							failure = previousFailure
						}
					}
					return Result{Status: "unknown", Stage: j.Stage, Failure: failure}, nil
				}
				if str(poll["payment_object_status"]) == "requires_action" {
					return Result{Status: "unknown", Stage: j.Stage, Failure: "payment_action_required"}, nil
				}
			}
			if str(poll["payment_object_status"]) == "requires_payment_method" {
				pi := obj(poll["payment_intent"])
				detail := Object{}
				for key, value := range obj(pi["last_payment_error"]) {
					detail[key] = value
				}
				detail["payment_intent"] = pi
				if recordDecline(detail, s, j) {
					if err := save(); err != nil {
						return Result{}, err
					}
					return e.replaceDeclinedCard(ctx, o, s, j, save)
				}
			}
			return Result{Status: "unknown", Stage: j.Stage, Failure: "payment_pending"}, nil
		}
		j.Stage = "paid"
		if err = save(); err != nil {
			return Result{}, err
		}
		return Result{Status: "succeeded", Stage: "paid", Receipt: j.Session}, nil
	}
	return Result{Status: "running", Stage: j.Stage}, nil
}
