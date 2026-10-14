package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"gptibo/platform/internal/commerce"
	"gptibo/platform/internal/store"
	"strings"
)

type Engine struct {
	Store          *store.Store
	NoCaptchaToken string
	NoCaptchaAuto  bool
	// Configure once before serving requests. Custom businesses reuse the same accounting service.
	Drivers map[string]Driver
}
type Result struct{ Status, Stage, Failure, Receipt string }
type Job struct {
	CheckoutCreateAttempts int     `json:"checkout_create_attempts,omitempty"`
	CheckoutCreateFailure  string  `json:"checkout_create_failure,omitempty"`
	CheckoutCreateEvidence bool    `json:"checkout_create_evidence,omitempty"`
	ManualInvoiceID        string  `json:"manual_invoice_id,omitempty"`
	Stage                  string  `json:"stage"`
	CardID                 int64   `json:"card_id"`
	UpstreamRef            string  `json:"upstream_ref"`
	PreflightToken         string  `json:"preflight_token"`
	PricingVersion         float64 `json:"pricing_version"`
	AccountID              string  `json:"account_id"`
	Account                Object  `json:"account"`
	RecipientID            string  `json:"recipient_id"`
	Session                string  `json:"session"`
	Method                 string  `json:"method"`
	Proof                  Object  `json:"proof"`
	Checksum               string  `json:"checksum"`
	CardAttempt            int     `json:"card_attempt,omitempty"`
	PoolCards              []int64 `json:"pool_cards,omitempty"`
	DeclinedIntent         string  `json:"declined_intent,omitempty"`
	DeclinedMethod         string  `json:"declined_method,omitempty"`
	DeclineCode            string  `json:"decline_code,omitempty"`
	AccountEmail           string  `json:"account_email"`
}

func (e *Engine) checkpoint(ctx context.Context, id, token string, j Job) error {
	if j.Stage == "creating" || j.Stage == "funding" || j.Stage == "tokenizing" || j.Stage == "submitting" || j.Stage == "submitted" && j.UpstreamRef == "" {
		if err := e.dispatchAllowed(ctx, id, token); err != nil {
			return err
		}
	}
	b, _ := json.Marshal(j)
	tag, err := e.Store.DB.Exec(ctx, "UPDATE orders SET job_cipher=$3,stage=$4,upstream_ref=$5,account_label=CASE WHEN account_label='' THEN $6 ELSE account_label END,updated_at=now() WHERE id=$1 AND lease_token=$2 AND status NOT IN ('succeeded','failed') AND lease_until>now()", id, token, e.Store.Vault.Seal("job:"+id, b), j.Stage, j.UpstreamRef, maskEmail(j.AccountEmail))
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("lease_lost")
	}
	return nil
}
func (e *Engine) resource(ctx context.Context, id, resource string) error {
	_, err := e.Store.DB.Exec(ctx, "INSERT INTO resource_locks(resource,order_id) VALUES($1,$2) ON CONFLICT(resource) DO NOTHING", resource, id)
	if err != nil {
		return err
	}
	var owner string
	if err = e.Store.DB.QueryRow(ctx, "SELECT order_id FROM resource_locks WHERE resource=$1", resource).Scan(&owner); err != nil {
		return err
	}
	if owner != id {
		return &UpstreamError{"resource_busy", false}
	}
	return nil
}
func (e *Engine) Execute(ctx context.Context, o commerce.Order, token, configCipher, credentialCipher, jobCipher string) Result {
	unknown := func(code string) Result { return Result{Status: "unknown", Stage: o.Stage, Failure: code} }
	configData, err := e.Store.Vault.Open("order:"+o.ID, configCipher)
	if err != nil {
		return unknown("configuration_unavailable")
	}
	var s commerce.Snapshot
	if json.Unmarshal(configData, &s) != nil {
		return unknown("configuration_unavailable")
	}
	var credential commerce.Credential
	if credentialCipher != "" {
		data, err := e.Store.Vault.Open("credential:"+o.ID, credentialCipher)
		if err != nil {
			return unknown("credential_unavailable")
		}
		if json.Unmarshal(data, &credential) != nil {
			return unknown("credential_unavailable")
		}
	}
	j := Job{Stage: "preflight"}
	if jobCipher != "" {
		data, err := e.Store.Vault.Open("job:"+o.ID, jobCipher)
		if err != nil || json.Unmarshal(data, &j) != nil {
			return unknown("checkpoint_unavailable")
		}
	}
	var channelStatus string
	if err = e.Store.DB.QueryRow(ctx, "SELECT status FROM users WHERE id=$1", o.UserID).Scan(&channelStatus); err != nil {
		return unknown("channel_unavailable")
	}
	if channelStatus != "active" && j.Stage != "submitting" && j.Stage != "submitted" && j.Stage != "paid" {
		if j.Stage == "preflight" {
			return Result{Status: "failed", Stage: j.Stage, Failure: "channel_disabled"}
		}
		return Result{Status: "unknown", Stage: j.Stage, Failure: "channel_disabled"}
	}
	// Pausing affects new side effects. Original submission queries remain available.
	if !strings.Contains(j.Stage, "submitt") && j.Stage != "paid" {
		var cipher string
		var current commerce.ProviderConfig
		if err = e.Store.DB.QueryRow(ctx, "SELECT payload_cipher FROM settings WHERE id='provider'").Scan(&cipher); err != nil {
			return unknown("execution_paused")
		}
		data, err := e.Store.Vault.Open("setting:provider", cipher)
		if err != nil || json.Unmarshal(data, &current) != nil || !current.WritesEnabled {
			return Result{Status: "running", Stage: j.Stage, Failure: "execution_paused"}
		}
	}
	save := func() error { return e.checkpoint(ctx, o.ID, token, j) }
	var result Result
	drivers := e.Drivers
	if drivers == nil {
		drivers = e.builtinDrivers()
	}
	driver, exists := drivers[s.Business]
	if !exists {
		return unknown("business_driver_missing")
	}
	result, err = driver.Execute(ctx, o, s, credential, &j, save)
	if err != nil {
		var upstream *UpstreamError
		if errors.As(err, &upstream) {
			if upstream.Code == "resource_busy" || upstream.Code == "execution_paused" || upstream.Code == "channel_disabled" {
				return Result{Status: "running", Stage: j.Stage, Failure: upstream.Code}
			}
			if upstream.Definitive && j.Stage != "submitting" && j.Stage != "submitted" && j.Stage != "creating" && j.Stage != "paid" && j.Stage != "tokenizing" && (j.Stage != "funding" || upstream.Code == "card_operation_not_charged") {
				return Result{Status: "failed", Stage: j.Stage, Failure: upstream.Code}
			}
			return Result{Status: "unknown", Stage: j.Stage, Failure: upstream.Code}
		}
		return Result{Status: "unknown", Stage: j.Stage, Failure: "execution_requires_reconciliation"}
	}
	return result
}
func (e *Engine) gpt(ctx context.Context, o commerce.Order, s commerce.Snapshot, c commerce.Credential, j *Job, save func() error) (Result, error) {
	if j.Stage == "submitting" || j.Stage == "submitted" {
		return gptQuery(ctx, s, *j, o.ID)
	}
	if j.Stage == "preflight" {
		data, err := gptPreflight(ctx, s, c)
		if err != nil {
			return Result{}, err
		}
		catalog, err := plans(ctx, s)
		if err != nil {
			return Result{}, err
		}
		j.PreflightToken = str(data["preflight_token"])
		j.AccountEmail = accountEmail(data["email"])
		if s.ConfirmedAccount != "" && s.ConfirmedAccount != j.AccountEmail {
			return Result{}, &UpstreamError{"account_changed", true}
		}
		if err = e.resource(ctx, o.ID, "gpt-recipient:"+j.AccountEmail); err != nil {
			return Result{}, err
		}
		j.PricingVersion = number(catalog["version"])
		j.Stage = "card"
		if err = save(); err != nil {
			return Result{}, err
		}
	}
	card, err := e.card(ctx, o, s, j, save)
	if err != nil {
		return Result{}, err
	}
	j.CardID = card
	data, err := gptPreflight(ctx, s, c)
	if err != nil {
		return Result{}, err
	}
	j.PreflightToken = str(data["preflight_token"])
	if accountEmail(data["email"]) != j.AccountEmail {
		return Result{}, &UpstreamError{"account_changed", true}
	}
	j.Stage = "submitting"
	if err = save(); err != nil {
		return Result{}, err
	}
	body := Object{"product": "gpt", "plan": s.Plan, "credential": c, "card_id": card, "preflight_token": j.PreflightToken, "client_request_id": o.ID, "payment_country": s.Provider.PaymentCountry, "payment_currency": s.Provider.PaymentCurrency}
	if j.PricingVersion > 0 {
		body["pricing_version"] = j.PricingVersion
	}
	created, err := cardAPI(ctx, s.Provider, "POST", "/gpt-direct/orders", body, o.ID)
	if err != nil {
		return Result{}, err
	}
	row := obj(created["data"])
	j.UpstreamRef = str(row["id"])
	if j.UpstreamRef == "" {
		j.UpstreamRef = str(row["order_id"])
	}
	j.Stage = "submitted"
	if err = save(); err != nil {
		return Result{}, err
	}
	return gptQuery(ctx, s, *j, o.ID)
}
func (e *Engine) card(ctx context.Context, o commerce.Order, s commerce.Snapshot, j *Job, save func() error) (int64, error) {
	rule := s.Rule
	threshold := rule.RefillThresholdMinor
	if threshold == 0 {
		threshold = rule.FundingMinor
	}
	reference := cardReference(o.ID, j.CardAttempt)
	if rule.ReplaceOnDecline {
		if err := e.lockCardRule(ctx, o, s); err != nil {
			return 0, err
		}
	}
	// Unknown card operations cannot be bypassed by a later balance change.
	var operationStatus string
	var operationKind string
	var operationCard int64
	lookupErr := e.Store.DB.QueryRow(ctx, "SELECT status,card_id,operation FROM card_operations WHERE reference=$1", reference).Scan(&operationStatus, &operationCard, &operationKind)
	if lookupErr == nil {
		if operationStatus == "failed" && operationKind != "fund" {
			return 0, &UpstreamError{"card_operation_not_charged", true}
		}
		if operationStatus != "succeeded" && operationStatus != "failed" {
			return 0, &UpstreamError{"card_funding_unconfirmed", false}
		}
		j.CardID = operationCard
	} else if !errors.Is(lookupErr, pgx.ErrNoRows) {
		return 0, lookupErr
	}
	if rule.BackupCardID > 0 {
		if err := e.selectPairCard(ctx, o, s, j, save); err != nil {
			return 0, err
		}
	}
	if rule.BackupCardID == 0 && j.CardID == 0 && j.CardAttempt == 0 {
		j.CardID = rule.CardID
		if rule.ReplaceOnDecline {
			var active int64
			err := e.Store.DB.QueryRow(ctx, "SELECT card_id FROM card_rule_runtime WHERE rule_id=$1 AND rule_version=$2 AND environment=$3 AND card_product=$4", rule.ID, rule.Version, s.Provider.Environment, rule.CardProduct).Scan(&active)
			if err == nil {
				j.CardID = active
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return 0, err
			}
		}
	}
	if j.CardID > 0 {
		var retired bool
		if err := e.Store.DB.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM card_retirements WHERE environment=$1 AND card_id=$2)", s.Provider.Environment, j.CardID).Scan(&retired); err != nil {
			return 0, err
		}
		if retired {
			if operationStatus != "" || !rule.ReplaceOnDecline || rule.BackupCardID > 0 {
				return 0, &UpstreamError{"card_retired", false}
			}
			j.CardID = 0
		}
	}
	if j.CardID > 0 {
		if err := e.bindCard(ctx, s.Provider.Environment, j.CardID, s.Business); err != nil {
			return 0, err
		}
		if err := e.resource(ctx, o.ID, fmt.Sprintf("card:%s:%d", s.Provider.Environment, j.CardID)); err != nil {
			return 0, err
		}
		resp, err := cardAPI(ctx, s.Provider, "GET", fmt.Sprintf("/cards/%d?sync=1", j.CardID), nil, "")
		if err != nil {
			return 0, err
		}
		row := obj(resp["data"])
		if !matchesID(row["id"], j.CardID) || str(row["product_code"]) != rule.CardProduct || str(row["status"]) != "ACTIVE" {
			return 0, &UpstreamError{"card_product_mismatch", true}
		}
		if !cardLimitsReady(row, rule) {
			return 0, &UpstreamError{"card_spend_limit_unconfirmed", true}
		}
		balance, valid := usdBalanceMinor(row["available_amount"])
		if valid && balance >= threshold {
			if err := e.rememberCard(ctx, s, j.CardID); err != nil {
				return 0, err
			}
			return j.CardID, nil
		}
		// A confirmed failed top-up is never sent again. An operator may have
		// separately funded this same card; only its verified usable balance resumes delivery.
		if operationStatus == "failed" {
			return 0, &UpstreamError{"card_operation_not_charged", true}
		}
		if !valid || operationStatus == "succeeded" {
			return 0, &UpstreamError{"card_not_ready", false}
		}
		if !rule.AutoOpen {
			return 0, &UpstreamError{"card_balance_insufficient", true}
		}
		if row["min_recharge"] != nil {
			minimum, valid := usdMinor(row["min_recharge"])
			if !valid || rule.FundingMinor < minimum {
				return 0, &UpstreamError{"card_funding_minimum", true}
			}
		}
	}
	if !rule.AutoOpen {
		return 0, &UpstreamError{"card_missing", true}
	}
	funding := rule.FundingMinor
	if j.CardID == 0 && rule.OpenAmountMinor > 0 {
		funding = rule.OpenAmountMinor
	}
	fundingSnapshot := s
	fundingSnapshot.Rule.FundingMinor = funding
	fee, err := cardFundingPrecheck(ctx, fundingSnapshot, j.CardID)
	if err != nil {
		return 0, err
	}
	operation := "open"
	if j.CardID > 0 {
		operation = "fund"
	}
	err = e.Store.Tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", rule.ID); err != nil {
			return err
		}
		var state string
		var id int64
		err := tx.QueryRow(ctx, "SELECT status,card_id FROM card_operations WHERE reference=$1", reference).Scan(&state, &id)
		if err == nil {
			if state == "succeeded" {
				j.CardID = id
				return nil
			}
			return &UpstreamError{"card_funding_unconfirmed", false}
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var spent int64
		if err = tx.QueryRow(ctx, "SELECT COALESCE(sum(amount_minor+fee_minor),0) FROM card_operations WHERE rule_id=$1 AND status<>'failed' AND created_at>date_trunc('day',now() AT TIME ZONE 'Asia/Shanghai') AT TIME ZONE 'Asia/Shanghai'", rule.ID).Scan(&spent); err != nil {
			return err
		}
		if spent+funding+fee > rule.DailyLimitMinor {
			return &UpstreamError{"card_daily_limit", true}
		}
		_, err = tx.Exec(ctx, "INSERT INTO card_operations(reference,order_id,rule_id,operation,amount_minor,fee_minor,card_id,status) VALUES($1,$2,$3,$4,$5,$6,$7,'submitting')", reference, o.ID, rule.ID, operation, funding, fee, j.CardID)
		return err
	})
	if err != nil {
		return 0, err
	}
	var state string
	if err = e.Store.DB.QueryRow(ctx, "SELECT status FROM card_operations WHERE reference=$1", reference).Scan(&state); err != nil {
		return 0, err
	}
	if state == "succeeded" {
		return 0, &UpstreamError{"card_not_ready", false}
	}
	j.Stage = "funding"
	if err = save(); err != nil {
		return 0, err
	}
	body := Object{"product_code": rule.CardProduct, "init_amount": float64(funding) / 100, "first_name": s.Provider.Billing.FirstName, "last_name": s.Provider.Billing.LastName}
	if !rule.UseProviderLimits {
		limit := float64(rule.MaxOrderMinor) / 100
		for _, key := range []string{"max_on_daily", "max_on_monthly", "max_on_percent", "transaction_limit"} {
			body[key] = limit
		}
		body["transaction_limit_type"] = "limited"
	}
	path := "/cards/open"
	if operation == "fund" {
		path = "/cards/recharge"
		body = Object{"card_id": j.CardID, "amount": float64(funding) / 100}
	}
	resp, err := cardAPI(ctx, s.Provider, "POST", path, body, reference)
	if err != nil {
		_, _ = e.Store.DB.Exec(ctx, "UPDATE card_operations SET status='unknown' WHERE reference=$1", reference)
		return 0, err
	}
	row := obj(resp["data"])
	id := int64(number(row["id"]))
	if id == 0 {
		id = int64(number(row["card_id"]))
	}
	if operation == "fund" {
		if row["card_id"] != nil && !matchesID(row["card_id"], j.CardID) {
			return 0, &UpstreamError{"card_evidence_mismatch", false}
		}
		id = j.CardID
	} else if !matchesID(row["id"], id) && !matchesID(row["card_id"], id) {
		return 0, &UpstreamError{"card_evidence_mismatch", false}
	}
	if id <= 0 {
		return 0, &UpstreamError{"card_funding_unconfirmed", false}
	}
	if _, err = e.Store.DB.Exec(ctx, "UPDATE card_operations SET status='succeeded',card_id=$2 WHERE reference=$1", reference, id); err != nil {
		return 0, err
	}
	j.CardID = id
	if err = e.bindCard(ctx, s.Provider.Environment, id, s.Business); err != nil {
		return 0, err
	}
	j.Stage = "card"
	if err = save(); err != nil {
		return 0, err
	}
	if err = e.resource(ctx, o.ID, fmt.Sprintf("card:%s:%d", s.Provider.Environment, id)); err != nil {
		return 0, err
	}
	resp, err = cardAPI(ctx, s.Provider, "GET", fmt.Sprintf("/cards/%d?sync=1", id), nil, "")
	if err != nil {
		return 0, err
	}
	row = obj(resp["data"])
	balance, valid := usdBalanceMinor(row["available_amount"])
	if !matchesID(row["id"], id) || str(row["product_code"]) != rule.CardProduct || str(row["status"]) != "ACTIVE" || !valid || balance < funding || balance < threshold || !cardLimitsReady(row, rule) {
		return 0, &UpstreamError{"card_not_ready", false}
	}
	if err := e.rememberCard(ctx, s, id); err != nil {
		return 0, err
	}
	return id, nil
}

func cardReference(order string, attempt int) string {
	if attempt == 0 {
		return order + ":card"
	}
	return fmt.Sprintf("%s:card:%d", order, attempt)
}

func (e *Engine) rememberCard(ctx context.Context, s commerce.Snapshot, id int64) error {
	if !s.Rule.ReplaceOnDecline || s.Rule.BackupCardID > 0 {
		return nil
	}
	_, err := e.Store.DB.Exec(ctx, "INSERT INTO card_rule_runtime(rule_id,rule_version,environment,card_product,card_id) VALUES($1,$2,$3,$4,$5) ON CONFLICT(rule_id,rule_version,environment) DO UPDATE SET card_id=excluded.card_id,updated_at=now()", s.Rule.ID, s.Rule.Version, s.Provider.Environment, s.Rule.CardProduct, id)
	return err
}

func cardLimitsReady(row Object, rule commerce.CardRule) bool {
	// This explicit, snapshotted policy permits the provider's existing defaults.
	// Card identity, product, ACTIVE status and funds are verified by each caller.
	return rule.UseProviderLimits || cardLimitMatches(row, rule.MaxOrderMinor)
}

func cardLimitMatches(row Object, max int64) bool {
	limit, ok := usdMinor(row["transaction_limit"])
	if row["transaction_limit"] != nil || row["transaction_limit_type"] != nil {
		return str(row["transaction_limit_type"]) == "limited" && ok && limit > 0 && limit <= max
	}
	// Production Card provider responses use limit.single_limit, expressed in USD.
	// A zero or absent limit is not proof of a spending cap.
	limit, ok = usdMinor(obj(row["limit"])["single_limit"])
	return row["spend_limits_supported"] == true && ok && limit > 0 && limit <= max
}

func (e *Engine) bindCard(ctx context.Context, environment string, id int64, business string) error {
	if _, err := e.Store.DB.Exec(ctx, "INSERT INTO card_bindings(environment,card_id,business) VALUES($1,$2,$3) ON CONFLICT DO NOTHING", environment, id, business); err != nil {
		return err
	}
	var owner string
	if err := e.Store.DB.QueryRow(ctx, "SELECT business FROM card_bindings WHERE environment=$1 AND card_id=$2", environment, id).Scan(&owner); err != nil {
		return err
	}
	if owner != business {
		return &UpstreamError{"card_business_mismatch", true}
	}
	return nil
}
func (e *Engine) Preflight(ctx context.Context, code, recipient string, c commerce.Credential) (any, error) {
	var s commerce.Snapshot
	var productConfig []byte
	if err := e.Store.DB.QueryRow(ctx, "SELECT business,upstream_plan,config FROM products WHERE code=$1 AND enabled", code).Scan(&s.Business, &s.Plan, &productConfig); err != nil {
		return nil, commerce.Fail("product_disabled", "商品暂未开放", 409)
	}
	if err := json.Unmarshal(productConfig, &s.ProductConfig); err != nil {
		return nil, err
	}
	var err error
	s.Provider, err = (&commerce.Service{Store: e.Store}).Provider(ctx)
	if err != nil {
		return nil, err
	}
	if s.Business == "gpt" {
		data, err := gptPreflight(ctx, s, c)
		if err != nil {
			return nil, commerce.Fail("preflight_failed", "账号预检暂未通过，请检查资料或稍后重试", 409)
		}
		current := str(data["current_plan"])
		if current == "" {
			current = str(data["currentPlan"])
		}
		return Object{"email": accountEmail(data["email"]), "current_plan": current, "target_plan": s.Plan, "eligible": true}, nil
	}
	if err := commerce.ValidateXProduct(code, s.ProductConfig); err != nil {
		return nil, err
	}
	var id, cipher string
	if err = e.Store.DB.QueryRow(ctx, "SELECT id,payload_cipher FROM x_accounts WHERE enabled ORDER BY id LIMIT 1").Scan(&id, &cipher); err != nil {
		return nil, commerce.Fail("x_account_missing", "暂无可用 X 账号", 409)
	}
	data, err := e.Store.Vault.Open("x-account:"+id, cipher)
	if err != nil {
		return nil, err
	}
	var account Object
	if json.Unmarshal(data, &account) != nil {
		return nil, errors.New("account unavailable")
	}
	if err := xQuote(ctx, s, account); err != nil {
		return nil, commerce.Fail("preflight_failed", "X 实时套餐报价暂未确认", 409)
	}
	result, err := xEligibility(ctx, s, account, recipient)
	if err != nil {
		return nil, commerce.Fail("preflight_failed", "X 账号资格暂未确认", 409)
	}
	return result, nil
}
