package providers

import (
	"context"
	"fmt"
	"gptibo/platform/internal/commerce"
	"regexp"
)

type stripeCardDecline struct{ detail Object }

func (d *stripeCardDecline) Error() string { return "stripe_card_declined" }

func paymentAttemptKey(order, operation string, attempt int) string {
	if attempt == 0 {
		return order + ":" + operation
	}
	return fmt.Sprintf("%s:%s:%d", order, operation, attempt)
}

func retryableDecline(code string) bool {
	// Authentication, duplicate payments and fraud/security blocks need reconciliation.
	switch code {
	case "insufficient_funds", "expired_card", "card_not_supported", "currency_not_supported", "do_not_honor", "generic_decline", "card_velocity_exceeded", "withdrawal_count_limit_exceeded":
		return true
	}
	return false
}

func unpaidDeclinedIntent(pi Object, s commerce.Snapshot, method string) bool {
	received, receivedOK := exactNumber(pi["amount_received"])
	amount, amountOK := exactNumber(pi["amount"])
	last := obj(pi["last_payment_error"])
	pm := str(last["payment_method"])
	if pm == "" {
		pm = str(obj(last["payment_method"])["id"])
	}
	if pm == "" {
		pm = str(pi["payment_method"])
	}
	if !regexp.MustCompile(`^pi_[A-Za-z0-9]+$`).MatchString(str(pi["id"])) || pi["livemode"] != true || str(pi["status"]) != "requires_payment_method" || !receivedOK || received != 0 || !amountOK || amount != number(s.ProductConfig["amount_minor"]) || str(pi["currency"]) != str(s.ProductConfig["currency"]) || pm == "" || pm != method || str(last["code"]) != "card_declined" || !retryableDecline(str(last["decline_code"])) || pi["next_action"] != nil {
		return false
	}
	if pi["amount_capturable"] != nil {
		n, ok := exactNumber(pi["amount_capturable"])
		if !ok || n != 0 {
			return false
		}
	}
	return true
}

func retryPageIntentMatches(value any, s commerce.Snapshot, j *Job) bool {
	if value == nil {
		return true
	}
	pi := obj(value)
	return j.CardAttempt > 0 && j.DeclinedIntent != "" && str(pi["id"]) == j.DeclinedIntent && unpaidDeclinedIntent(pi, s, j.DeclinedMethod)
}

func recordDecline(detail Object, s commerce.Snapshot, j *Job) bool {
	pi := obj(detail["payment_intent"])
	if str(detail["type"]) != "card_error" || str(detail["code"]) != "card_declined" || !retryableDecline(str(detail["decline_code"])) || !unpaidDeclinedIntent(pi, s, j.Method) || j.DeclinedIntent != "" && str(pi["id"]) != j.DeclinedIntent {
		return false
	}
	j.DeclinedIntent = str(pi["id"])
	j.DeclinedMethod = j.Method
	j.DeclineCode = str(detail["decline_code"])
	j.Stage = "declined"
	return true
}

func (e *Engine) replaceDeclinedCard(ctx context.Context, o commerce.Order, s commerce.Snapshot, j *Job, save func() error) (Result, error) {
	if j.DeclinedIntent == "" || j.DeclinedMethod != j.Method || j.CardID <= 0 || !retryableDecline(j.DeclineCode) {
		return Result{}, &UpstreamError{"decline_evidence_missing", false}
	}
	if s.Rule.BackupCardID > 0 {
		return e.declinePairCard(ctx, o, s, j, save)
	}
	if _, err := e.Store.DB.Exec(ctx, "INSERT INTO card_retirements(environment,card_id,order_id,reason) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING", s.Provider.Environment, j.CardID, o.ID, j.DeclineCode); err != nil {
		return Result{}, err
	}
	if !s.Rule.ReplaceOnDecline || !s.Rule.AutoOpen {
		return Result{Status: "unknown", Stage: j.Stage, Failure: "card_declined"}, nil
	}
	if j.CardAttempt >= s.Rule.MaxReplacements {
		return Result{Status: "unknown", Stage: j.Stage, Failure: "card_replacement_limit"}, nil
	}
	j.CardAttempt++
	j.CardID = 0
	j.Method = ""
	j.Stage = "card"
	if err := save(); err != nil {
		return Result{}, err
	}
	return Result{Status: "running", Stage: j.Stage, Failure: "card_replacing"}, nil
}
