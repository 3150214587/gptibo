package providers

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"gptibo/platform/internal/commerce"
	"gptibo/platform/internal/store"
)

// Creating a hosted checkout cannot charge a card. Only a task with no captured
// checkout or payment evidence may dispatch one replacement creation request.
// The attempt budget is persisted BEFORE dispatch, including across crashes.
func unpaidXCreation(j *Job) bool {
	return !j.CheckoutCreateEvidence && j.ManualInvoiceID == "" && j.Session == "" && j.Method == "" && j.CardID == 0 &&
		j.UpstreamRef == "" && len(j.Proof) == 0 && j.Checksum == "" && j.CardAttempt == 0 &&
		len(j.PoolCards) == 0 && j.DeclinedIntent == "" && j.DeclinedMethod == ""
}

func (e *Engine) createXCheckout(ctx context.Context, o commerce.Order, s commerce.Snapshot, j *Job, save func() error) error {
	if !unpaidXCreation(j) || j.CheckoutCreateAttempts < 0 || j.CheckoutCreateAttempts >= 2 {
		return &UpstreamError{"original_request_unconfirmed", false}
	}
	j.CheckoutCreateAttempts++
	j.Stage = "creating"
	if err := save(); err != nil {
		return err
	}
	if err := e.Store.Tx(ctx, func(tx pgx.Tx) error {
		return store.Audit(ctx, tx, "worker", "x_checkout_creation_attempt", o.ID, fmt.Sprintf("attempt=%d; checkout creation only; no card payment dispatched", j.CheckoutCreateAttempts))
	}); err != nil {
		return err
	}
	success := "https://x.com/" + o.Recipient + "/gift-premium/success"
	created, err := xQuery(ctx, s, j.Account, "useOneTimePurchaseGiftMutation", "GqTVJ4S1526tLkxj69xIZw", Object{"cancel_url": strings.TrimSuffix(success, "/success"), "success_url": success, "external_product_id": str(s.ProductConfig["stripe_product"]), "gift_recipient": j.RecipientID})
	row := obj(created["onetimepurchase_gift"])
	j.CheckoutCreateEvidence = len(row) > 0
	j.Session = str(row["session_id"])
	if err != nil {
		// Persist a safe code, never X's raw response or account credentials.
		j.CheckoutCreateFailure = "x_query_unconfirmed"
		if saveErr := save(); saveErr != nil {
			return saveErr
		}
		return err
	}
	target, parseErr := url.Parse(str(row["session_url"]))
	if parseErr != nil || str(row["session_status"]) != "Unpaid" || target.Host != "checkout.stripe.com" || target.Scheme != "https" || target.User != nil || target.RawPath != "" || (target.Path != "/pay/"+j.Session && target.Path != "/c/pay/"+j.Session) || !regexp.MustCompile(`^cs_live_[A-Za-z0-9]+$`).MatchString(j.Session) {
		// Even an invalid response may identify a checkout. Keep that evidence
		// durably so the next run cannot silently create a replacement.
		j.CheckoutCreateFailure = "checkout_unconfirmed"
		if saveErr := save(); saveErr != nil {
			return saveErr
		}
		return &UpstreamError{"checkout_unconfirmed", false}
	}
	j.Stage = "checkout"
	return save()
}

func (e *Engine) retryXCheckout(ctx context.Context, o commerce.Order, s commerce.Snapshot, j *Job, save func() error) error {
	// Legacy checkpoints have no reliable attempt count; do not guess their
	// history. Manual invoices and tokenization/payment recovery are separate.
	if j.Stage != "creating" || j.CheckoutCreateAttempts != 1 || !unpaidXCreation(j) ||
		(j.CheckoutCreateFailure != "" && j.CheckoutCreateFailure != "x_query_unconfirmed") ||
		!regexp.MustCompile(`^\d{1,25}$`).MatchString(j.RecipientID) || j.AccountID == "" {
		return &UpstreamError{"original_request_unconfirmed", false}
	}
	var safe bool
	if err := e.Store.DB.QueryRow(ctx, `SELECT EXISTS(
 SELECT 1 FROM orders o WHERE o.id=$1 AND o.business='x' AND o.status IN ('queued','running','unknown')
 AND o.stage='creating' AND o.receipt='' AND o.upstream_ref='' AND o.manual_payment_at IS NULL
 AND NOT EXISTS(SELECT 1 FROM card_operations c WHERE c.order_id=o.id)
 AND NOT EXISTS(SELECT 1 FROM ledger l WHERE l.order_id=o.id AND l.kind IN ('consume','release'))
 AND EXISTS(SELECT 1 FROM ledger l WHERE l.order_id=o.id AND l.kind='reserve' AND l.frozen_delta=o.supply_minor)
 )`, o.ID).Scan(&safe); err != nil {
		return err
	}
	if !safe {
		return &UpstreamError{"original_request_unconfirmed", false}
	}
	for _, resource := range []string{"x-recipient-name:" + o.Recipient, "x-recipient:" + j.RecipientID} {
		if err := e.resource(ctx, o.ID, resource); err != nil {
			return err
		}
	}
	check, err := xEligibility(ctx, s, j.Account, o.Recipient)
	if err != nil {
		return err
	}
	if !truth(check["eligible"]) || str(check["recipient_id"]) != j.RecipientID || (s.ConfirmedRecipientID != "" && str(check["recipient_id"]) != s.ConfirmedRecipientID) {
		return &UpstreamError{"recipient_changed", false}
	}
	if err := xQuote(ctx, s, j.Account); err != nil {
		return err
	}
	return e.createXCheckout(ctx, o, s, j, save)
}
