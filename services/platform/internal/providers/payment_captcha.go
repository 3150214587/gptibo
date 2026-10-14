package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"gptibo/platform/internal/commerce"
	"gptibo/platform/internal/store"
)

const noCaptchaEndpoint = "https://api.nocaptcha.io/api/wanda/hcaptcha/universal"

type paymentChallenge struct {
	Intent, Secret, SiteKey, RQData, VerifyPath string
}

// This reads an already-created payment. Only the original intent may receive
// the captcha response; arbitrary verification URLs and other action types fail closed.
func parsePaymentChallenge(page Object, o commerce.Order, s commerce.Snapshot, j *Job) (paymentChallenge, string) {
	c := paymentChallenge{}
	pi := obj(page["payment_intent"])
	next := obj(pi["next_action"])
	sdk := obj(next["use_stripe_sdk"])
	js := obj(sdk["stripe_js"])
	if str(next["type"]) != "use_stripe_sdk" || str(sdk["type"]) != "intent_confirmation_challenge" || (str(js["captcha_vendor_name"]) != "" && str(js["captcha_vendor_name"]) != "hcaptcha") {
		return c, "captcha_unsupported"
	}
	c.Intent, c.Secret = str(pi["id"]), str(pi["client_secret"])
	method := str(pi["payment_method"])
	if method == "" {
		method = str(obj(pi["payment_method"])["id"])
	}
	if !regexp.MustCompile(`^pi_[A-Za-z0-9]+$`).MatchString(c.Intent) || !strings.HasPrefix(c.Secret, c.Intent+"_secret_") || str(pi["status"]) != "requires_action" || !truth(pi["livemode"]) || method != j.Method || method == "" || number(pi["amount"]) != number(s.ProductConfig["amount_minor"]) || str(pi["currency"]) != str(s.ProductConfig["currency"]) {
		return c, "captcha_payment_mismatch"
	}
	checked := Object{}
	for k, v := range page {
		checked[k] = v
	}
	delete(checked, "payment_intent")
	if guardPage(checked, o, s, j) != nil {
		return c, "captcha_payment_mismatch"
	}
	u, err := url.Parse(str(js["verification_url"]))
	wantPath := "/v1/payment_intents/" + c.Intent + "/verify_challenge"
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || u.Path != wantPath || (u.Host != "" && (u.Host != "api.stripe.com" || u.Scheme != "https")) || (u.Host == "" && u.Scheme != "") {
		return c, "captcha_verification_url_invalid"
	}
	c.VerifyPath = strings.TrimPrefix(wantPath, "/v1/")
	c.SiteKey, c.RQData = str(js["site_key"]), str(js["rqdata"])
	if c.SiteKey == "" || len(c.SiteKey) > 200 || len(c.RQData) > 100000 {
		return c, "captcha_parameters_missing"
	}
	return c, ""
}

func (e *Engine) cancelUnavailableCaptcha(ctx context.Context, id string) error {
	return e.Store.Tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "UPDATE payment_captcha_attempts SET status='failed',failure_code='captcha_session_unavailable',updated_at=now() WHERE order_id=$1 AND status='pending'", id)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return store.Audit(ctx, tx, "worker", "payment_captcha.failed", id, "captcha_session_unavailable")
	})
}

func (e *Engine) queuePaymentCaptcha(ctx context.Context, id string) error {
	if !e.NoCaptchaAuto || e.NoCaptchaToken == "" {
		return nil
	}
	_, err := e.Store.DB.Exec(ctx, `INSERT INTO payment_captcha_attempts(order_id)
 SELECT id FROM orders WHERE id=$1 AND business='x' AND manual_payment_at IS NOT NULL AND stage='submitted' AND status IN ('queued','running','unknown')
 ON CONFLICT(order_id) DO NOTHING`, id)
	return err
}

func (e *Engine) captchaOutcome(ctx context.Context, id, status, code string) error {
	return e.Store.Tx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "UPDATE payment_captcha_attempts SET status=$2,failure_code=$3,updated_at=now() WHERE order_id=$1", id, status, code)
		if err != nil {
			return err
		}
		return store.Audit(ctx, tx, "worker", "payment_captcha."+status, id, code)
	})
}

// Called only in the manual lane, never while holding the normal order queue.
// One durable attempt per order, including timeouts and process restarts.
func (e *Engine) solvePaymentCaptcha(ctx context.Context, o commerce.Order, s commerce.Snapshot, j *Job, save func() error) error {
	if !e.NoCaptchaAuto || e.NoCaptchaToken == "" {
		return nil
	}
	current, err := (&commerce.Service{Store: e.Store}).Provider(ctx)
	if err != nil {
		return err
	}
	var active bool
	if err = e.Store.DB.QueryRow(ctx, "SELECT status='active' FROM users WHERE id=$1", o.UserID).Scan(&active); err != nil {
		return err
	}
	if !active || !current.WritesEnabled || current.StripeKey != s.Provider.StripeKey {
		return nil
	}
	// If a process died after dispatch, do not pay for another solve or replay a verification.
	if _, err = e.Store.DB.Exec(ctx, "UPDATE payment_captcha_attempts SET status='unconfirmed',failure_code='captcha_interrupted',updated_at=now() WHERE order_id=$1 AND status IN ('checking','solving') AND updated_at<now()-interval '5 minutes'", o.ID); err != nil {
		return err
	}
	tag, err := e.Store.DB.Exec(ctx, "UPDATE payment_captcha_attempts SET status='checking',updated_at=now() WHERE order_id=$1 AND status='pending'", o.ID)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	fail := func(code string) error { return e.captchaOutcome(ctx, o.ID, "failed", code) }
	page, err := stripe(ctx, s, "POST", "payment_pages/"+j.Session+"/init", url.Values{"browser_locale": {"en"}, "redirect_type": {"url"}}, "")
	if err != nil {
		return fail("captcha_session_unavailable")
	}
	challenge, code := parsePaymentChallenge(page, o, s, j)
	// The encrypted snapshot preserves the actual subtype without publishing credentials.
	evidence, _ := json.Marshal(page)
	sdkType := str(obj(obj(obj(page["payment_intent"])["next_action"])["use_stripe_sdk"])["type"])
	if !regexp.MustCompile(`^[a-z0-9_]{0,80}$`).MatchString(sdkType) {
		sdkType = "unknown"
	}
	_, err = e.Store.DB.Exec(ctx, "UPDATE payment_captcha_attempts SET challenge_type=$2,evidence_cipher=$3,updated_at=now() WHERE order_id=$1", o.ID, sdkType, e.Store.Vault.Seal("captcha:"+o.ID, evidence))
	if err != nil {
		return err
	}
	if code != "" {
		return fail(code)
	}
	if err := save(); err != nil {
		return fail("captcha_execution_paused")
	}
	if _, err = e.Store.DB.Exec(ctx, "UPDATE payment_captcha_attempts SET status='solving',updated_at=now() WHERE order_id=$1", o.ID); err != nil {
		return err
	}
	// Only captcha inputs leave for NoCaptcha, never PAN/CVC, X cookies, checkout
	// capability links, Stripe client secrets or the verification URL.
	body, _ := json.Marshal(Object{"sitekey": challenge.SiteKey, "rqdata": challenge.RQData, "referer": "https://b.stripecdn.com/", "invisible": true, "need_ekey": true})
	solverClient := &http.Client{Transport: client.Transport, Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	reply, status, err := readJSON(ctx, solverClient, "POST", noCaptchaEndpoint, map[string]string{"User-Token": e.NoCaptchaToken, "Content-Type": "application/json"}, body)
	requestID := str(reply["id"])
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`).MatchString(requestID) {
		requestID = ""
	}
	// Preserve failures as well as successes for private diagnosis. The API key
	// and request headers are never part of the evidence or public order DTO.
	evidence, _ = json.Marshal(Object{"payment_page": page, "solver": Object{"http_status": status, "response": reply}})
	if _, saveErr := e.Store.DB.Exec(ctx, "UPDATE payment_captcha_attempts SET solver_request_id=$2,evidence_cipher=$3,updated_at=now() WHERE order_id=$1", o.ID, requestID, e.Store.Vault.Seal("captcha:"+o.ID, evidence)); saveErr != nil {
		return saveErr
	}
	if err != nil {
		return e.captchaOutcome(ctx, o.ID, "unconfirmed", "captcha_solver_unconfirmed")
	}
	if status != http.StatusOK || number(reply["status"]) != 1 {
		return fail("captcha_solver_failed")
	}
	data := obj(reply["data"])
	responseToken, ekey := str(data["generated_pass_UUID"]), str(data["ekey"])
	if responseToken == "" || len(responseToken) > 100000 || len(ekey) > 100000 {
		return fail("captcha_solver_invalid_response")
	}
	// Re-read immediately before verification: an operator could have paid or the
	// challenge could have expired while solving. Old tokens must not be applied.
	latest, err := stripe(ctx, s, "POST", "payment_pages/"+j.Session+"/init", url.Values{"browser_locale": {"en"}, "redirect_type": {"url"}}, "")
	if err != nil {
		return fail("captcha_session_unavailable")
	}
	nowChallenge, code := parsePaymentChallenge(latest, o, s, j)
	if code != "" || nowChallenge != challenge {
		return fail("captcha_challenge_changed")
	}
	current, err = (&commerce.Service{Store: e.Store}).Provider(ctx)
	if err != nil {
		return err
	}
	if err = e.Store.DB.QueryRow(ctx, "SELECT status='active' FROM users WHERE id=$1", o.UserID).Scan(&active); err != nil {
		return err
	}
	if !active || !current.WritesEnabled || current.StripeKey != s.Provider.StripeKey {
		return fail("captcha_execution_paused")
	}
	if err := save(); err != nil {
		return fail("captcha_execution_paused")
	}
	fields := url.Values{"client_secret": {challenge.Secret}, "challenge_response_token": {responseToken}, "captcha_vendor_name": {"hcaptcha"}}
	if ekey != "" {
		fields.Set("challenge_response_ekey", ekey)
	}
	// No confirm, new checkout, new payment method, or card rotation here. Existing
	// payment polling and Finish remain the only authority for order settlement.
	verified, err := stripe(ctx, s, "POST", challenge.VerifyPath, fields, o.ID+":captcha:verify")
	if err != nil {
		return e.captchaOutcome(ctx, o.ID, "unconfirmed", "captcha_verification_unconfirmed")
	}
	if str(verified["status"]) == "requires_action" || str(verified["status"]) == "requires_payment_method" || verified["error"] != nil {
		return fail("captcha_not_accepted")
	}
	return e.captchaOutcome(ctx, o.ID, "submitted", "")
}
