package providers

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"gptibo/platform/internal/commerce"
	"gptibo/platform/internal/store"
)

// Keep recipient deduplication, but release all execution resources. Manual
// payments do not reserve card funds or prevent another order using the card.
// Lock the order so isolation and worker leasing stay atomic.
func isolatePaymentTx(ctx context.Context, tx pgx.Tx, id string, card int64) error {
	var status, stage string
	var manual bool
	if err := tx.QueryRow(ctx, "SELECT status,stage,manual_payment_at IS NOT NULL FROM orders WHERE id=$1 FOR UPDATE", id).Scan(&status, &stage, &manual); err != nil {
		return err
	}
	if (status != "unknown" && status != "running" && status != "queued") || stage != "submitted" || card <= 0 {
		return commerce.Fail("handoff_unavailable", "原支付状态已变化，请重新核对", 409)
	}
	if _, err := tx.Exec(ctx, "UPDATE orders SET manual_payment_at=COALESCE(manual_payment_at,now()) WHERE id=$1", id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM resource_locks WHERE order_id=$1 AND
 (resource LIKE 'x-account:%' OR resource LIKE 'card-rule:%' OR resource LIKE 'card:%')`, id); err != nil {
		return err
	}
	if !manual {
		if err := store.EnqueueManualAlert(ctx, tx, id, "payment_action_required"); err != nil {
			return err
		}
		return store.Audit(ctx, tx, "worker", "payment_isolated", id, "支付验证挂起待人工；释放付款卡、X 账号及卡规则，不预留卡片金额；保留接收账号防重复锁，只查询原支付")
	}
	return nil
}

func (e *Engine) isolatePayment(ctx context.Context, id string, card int64) error {
	return e.Store.Tx(ctx, func(tx pgx.Tx) error { return isolatePaymentTx(ctx, tx, id, card) })
}

// The URL is a payment capability. Return it only through the authenticated
// administrator endpoint, never in order DTOs, notifications, or audit notes.
func validCheckoutURL(raw, session string) bool {
	u, err := url.Parse(raw)
	return err == nil && regexp.MustCompile(`^cs_live_[A-Za-z0-9]+$`).MatchString(session) && u.Scheme == "https" && u.Host == "checkout.stripe.com" && u.User == nil && u.RawPath == "" && (u.Path == "/c/pay/"+session || u.Path == "/pay/"+session) && u.Fragment != ""
}

func (e *Engine) ManualCheckout(ctx context.Context, actor, id string) (string, error) {
	var checkout string
	err := e.Store.Tx(ctx, func(tx pgx.Tx) error {
		var o commerce.Order
		var config, job string
		var lease *time.Time
		var manual bool
		// Serialize handoff with worker leasing: an already running attempt must finish first.
		err := tx.QueryRow(ctx, "SELECT id,business,recipient,status,stage,config_cipher,job_cipher,lease_until,manual_payment_at IS NOT NULL FROM orders WHERE id=$1 FOR UPDATE", id).Scan(&o.ID, &o.Business, &o.Recipient, &o.Status, &o.Stage, &config, &job, &lease, &manual)
		if errors.Is(err, pgx.ErrNoRows) {
			return commerce.Fail("order_missing", "订单不存在", 404)
		}
		if err != nil {
			return err
		}
		if o.Business != "x" || o.Status != "unknown" && o.Status != "running" || o.Stage != "submitted" {
			return commerce.Fail("handoff_unavailable", "仅已提交且待支付验证的 X 原订单可以转人工；请刷新订单", 409)
		}
		if lease != nil && lease.After(time.Now()) {
			return commerce.Fail("operation_busy", "正在查询原支付结果，请稍后重试", 409)
		}
		var s commerce.Snapshot
		var j Job
		data, err := e.Store.Vault.Open("order:"+id, config)
		if err != nil {
			return err
		}
		if err = json.Unmarshal(data, &s); err != nil {
			return err
		}
		data, err = e.Store.Vault.Open("job:"+id, job)
		if err != nil {
			return err
		}
		if err = json.Unmarshal(data, &j); err != nil {
			return err
		}
		if s.Business != "x" || s.Provider.Environment != "production" || j.Stage != "submitted" || !regexp.MustCompile(`^cs_live_[A-Za-z0-9]+$`).MatchString(j.Session) || (j.Method == "" && j.ManualInvoiceID == "") {
			return commerce.Fail("handoff_unavailable", "原支付凭证不完整，不能开放人工付款", 409)
		}
		// Init reads the existing hosted page. It never creates or confirms a payment.
		page, err := stripe(ctx, s, "POST", "payment_pages/"+j.Session+"/init", url.Values{"browser_locale": {"en"}, "redirect_type": {"url"}}, "")
		if err != nil {
			if inactiveCheckout(err) {
				return commerce.Fail("checkout_inactive", "原账单已失效，请核对旧账单未付款后使用重新生成账单", 409)
			}
			return commerce.Fail("payment_query_failed", "原付款页面查询未确认，请稍后重试", 502)
		}
		if j.ManualInvoiceID != "" {
			if err := guardManualInvoice(page, o, s, &j); err != nil {
				return err
			}
			payload, err := e.invoicePayload(ctx, tx, j.ManualInvoiceID, id)
			if err != nil {
				return err
			}
			checkout, err = invoiceLink(page, payload.URL, j.Session)
			return err
		}
		pi := obj(page["payment_intent"])
		method := str(pi["payment_method"])
		if method == "" {
			method = str(obj(pi["payment_method"])["id"])
		}
		pendingAction := str(pi["status"]) == "requires_action" && method == j.Method && str(obj(pi["next_action"])["type"]) != ""
		// A challenge can expire and clear its payment method. Keep the same unpaid
		// hosted session accessible after handoff; only the operator may retry there.
		manualRetry := manual && str(pi["status"]) == "requires_payment_method" && pi["next_action"] == nil && (method == "" || method == j.Method)
		if (!pendingAction && !manualRetry) || !regexp.MustCompile(`^pi_[A-Za-z0-9]+$`).MatchString(str(pi["id"])) || !truth(pi["livemode"]) || number(pi["amount"]) != number(s.ProductConfig["amount_minor"]) || str(pi["currency"]) != str(s.ProductConfig["currency"]) {
			return commerce.Fail("payment_action_changed", "原支付已不处于待验证状态，请刷新并核对原单", 409)
		}
		// Validate the original merchant, recipient, product and amount; the bound
		// pending intent was checked above rather than the pre-payment guard.
		checked := Object{}
		for k, v := range page {
			checked[k] = v
		}
		delete(checked, "payment_intent")
		if err = guardPage(checked, o, s, &j); err != nil {
			return commerce.Fail("payment_evidence_mismatch", "原账单商品、金额或接收账号核对不一致", 409)
		}
		checkout = str(page["url"])
		if !validCheckoutURL(checkout, j.Session) {
			checkout = str(page["stripe_hosted_url"])
		}
		if !validCheckoutURL(checkout, j.Session) {
			return commerce.Fail("checkout_link_unavailable", "没有可核实的原账单付款链接", 409)
		}
		if err = isolatePaymentTx(ctx, tx, id, j.CardID); err != nil {
			return err
		}
		failure := "payment_manual_action"
		if manualRetry {
			failure = "payment_manual_review"
		}
		_, err = tx.Exec(ctx, "UPDATE orders SET manual_payment_at=COALESCE(manual_payment_at,now()),failure_code=$2,updated_at=now(),next_at=now() WHERE id=$1", id, failure)
		if err != nil {
			return err
		}
		return store.Audit(ctx, tx, actor, "payment_handoff", id, "管理员重新拉起原账单手动支付；不占用付款卡、不预留卡片金额，只查询原支付结果，不自动重付")
	})
	return checkout, err
}
