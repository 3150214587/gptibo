package providers

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"gptibo/platform/internal/commerce"
	"gptibo/platform/internal/security"
	"gptibo/platform/internal/store"
)

type manualInvoicePayload struct {
	Job      Job    `json:"job"`
	URL      string `json:"url"`
	Response Object `json:"response"`
}

func inactiveCheckout(err error) bool {
	var upstream *UpstreamError
	return errors.As(err, &upstream) && upstream.Code == "checkout_inactive"
}

func manualInvoiceError() error {
	return commerce.Fail("invoice_creation_unconfirmed", "新账单创建结果未确认，已停止重复创建，请核对创建记录", 409)
}

// Validate the original recipient, merchant and amount, while allowing the human
// to choose a payment method on this explicitly manual checkout.
func guardManualInvoice(page Object, o commerce.Order, s commerce.Snapshot, j *Job) error {
	if j.ManualInvoiceID == "" {
		return commerce.Fail("invoice_mismatch", "人工账单记录不匹配", 409)
	}
	if page["payment_intent"] != nil {
		pi := obj(page["payment_intent"])
		state := str(pi["status"])
		if !regexp.MustCompile(`^pi_[A-Za-z0-9]+$`).MatchString(str(pi["id"])) || !truth(pi["livemode"]) || number(pi["amount"]) != number(s.ProductConfig["amount_minor"]) || str(pi["currency"]) != str(s.ProductConfig["currency"]) || (state != "requires_payment_method" && state != "requires_action" && state != "requires_confirmation") {
			return commerce.Fail("payment_action_changed", "账单已付款或状态变化，请刷新核对，不能再生成付款", 409)
		}
	}
	checked := Object{}
	for k, v := range page {
		checked[k] = v
	}
	delete(checked, "payment_intent")
	if err := guardPage(checked, o, s, j); err != nil {
		return commerce.Fail("payment_evidence_mismatch", "账单商品、金额或接收账号核对不一致", 409)
	}
	return nil
}

func invoiceLink(page Object, fallback, session string) (string, error) {
	for _, link := range []string{str(page["url"]), str(page["stripe_hosted_url"]), fallback} {
		if validCheckoutURL(link, session) {
			return link, nil
		}
	}
	return "", commerce.Fail("checkout_link_unavailable", "没有可核实的账单付款链接，请稍后重新打开", 409)
}

func (e *Engine) invoicePayload(ctx context.Context, tx pgx.Tx, id, order string) (manualInvoicePayload, error) {
	var payload manualInvoicePayload
	var cipher string
	if err := tx.QueryRow(ctx, "SELECT payload_cipher FROM manual_payment_invoices WHERE id=$1 AND order_id=$2 AND status IN ('created','ready')", id, order).Scan(&cipher); err != nil {
		return payload, err
	}
	b, err := e.Store.Vault.Open("manual-invoice:"+id, cipher)
	if err != nil {
		return payload, err
	}
	err = json.Unmarshal(b, &payload)
	return payload, err
}

// A durable creating record is committed before contacting X. Unknown results
// never dispatch again; a recorded session can only be re-read and attached.
func (e *Engine) RegenerateManualCheckout(ctx context.Context, actor, id string, confirmedUnpaid bool) (string, error) {
	if !confirmedUnpaid {
		return "", commerce.Fail("confirm_original_unpaid", "请先核对并确认旧账单未付款", 400)
	}
	var o commerce.Order
	var s commerce.Snapshot
	var original Job
	var payload manualInvoicePayload
	var invoiceID, originalCipher, reuseURL string
	dispatch := false
	err := e.Store.Tx(ctx, func(tx pgx.Tx) error {
		var config, stage string
		var lease *time.Time
		var manual, active bool
		err := tx.QueryRow(ctx, `SELECT o.id,o.user_id,o.business,o.recipient,o.status,o.stage,o.config_cipher,o.job_cipher,o.lease_until,o.manual_payment_at IS NOT NULL,u.status='active'
 FROM orders o JOIN users u ON u.id=o.user_id WHERE o.id=$1 FOR UPDATE OF o`, id).Scan(&o.ID, &o.UserID, &o.Business, &o.Recipient, &o.Status, &stage, &config, &originalCipher, &lease, &manual, &active)
		if errors.Is(err, pgx.ErrNoRows) {
			return commerce.Fail("order_missing", "订单不存在", 404)
		}
		if err != nil {
			return err
		}
		if !active || !manual || o.Business != "x" || o.Status != "unknown" && o.Status != "running" || stage != "submitted" {
			return commerce.Fail("invoice_unavailable", "仅未完成的挂起 X 订单可以重新生成账单", 409)
		}
		if lease != nil && lease.After(time.Now()) {
			return commerce.Fail("operation_busy", "订单正在处理，请稍后重试", 409)
		}
		b, err := e.Store.Vault.Open("order:"+id, config)
		if err != nil || json.Unmarshal(b, &s) != nil {
			return commerce.Fail("configuration_unavailable", "订单配置不可用", 409)
		}
		b, err = e.Store.Vault.Open("job:"+id, originalCipher)
		if err != nil || json.Unmarshal(b, &original) != nil {
			return commerce.Fail("checkpoint_unavailable", "原付款记录不可用", 409)
		}
		if original.Stage != "submitted" || s.Business != "x" || s.Provider.Environment != "production" || !regexp.MustCompile(`^cs_live_[A-Za-z0-9]+$`).MatchString(original.Session) || !regexp.MustCompile(`^\d{1,25}$`).MatchString(original.RecipientID) {
			return commerce.Fail("invoice_unavailable", "原订单接收账号或付款记录不完整", 409)
		}
		var providerCipher string
		if err = tx.QueryRow(ctx, "SELECT payload_cipher FROM settings WHERE id='provider'").Scan(&providerCipher); err != nil {
			return err
		}
		var current commerce.ProviderConfig
		b, err = e.Store.Vault.Open("setting:provider", providerCipher)
		if err != nil || json.Unmarshal(b, &current) != nil || !current.WritesEnabled || current.StripeKey != s.Provider.StripeKey {
			return commerce.Fail("execution_paused", "支付处理已暂停或连接配置变化", 409)
		}
		// Recover a captured creation response instead of creating another session.
		var state string
		err = tx.QueryRow(ctx, "SELECT id,status FROM manual_payment_invoices WHERE order_id=$1 AND previous_session_hash=$2", id, security.Hash(original.Session)).Scan(&invoiceID, &state)
		if err == nil {
			if state != "created" {
				return manualInvoiceError()
			}
			payload, err = e.invoicePayload(ctx, tx, invoiceID, id)
			if err != nil {
				return err
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		} else {
			page, readErr := stripe(ctx, s, "POST", "payment_pages/"+original.Session+"/init", url.Values{"browser_locale": {"en"}, "redirect_type": {"url"}}, "")
			if readErr == nil {
				if original.ManualInvoiceID == "" {
					return commerce.Fail("checkout_still_active", "原账单仍可用，请点击重新拉起支付，不需要生成另一张", 409)
				}
				if err := guardManualInvoice(page, o, s, &original); err != nil {
					return err
				}
				existing, err := e.invoicePayload(ctx, tx, original.ManualInvoiceID, id)
				if err != nil {
					return err
				}
				reuseURL, err = invoiceLink(page, existing.URL, original.Session)
				return err
			}
			if !inactiveCheckout(readErr) {
				return commerce.Fail("payment_query_failed", "原账单查询暂未确认，请稍后重试，未创建新账单", 502)
			}
			// Inactive alone is not proof of nonpayment. The administrator explicitly
			// confirms it above; a contradictory paid/active poll still blocks creation.
			_, readErr = stripe(ctx, s, "GET", "payment_pages/"+original.Session+"/poll", url.Values{}, "")
			if !inactiveCheckout(readErr) {
				return commerce.Fail("payment_action_changed", "原付款状态仍可查询或结果变化，请先核对原单", 409)
			}
			check, err := xEligibility(ctx, s, original.Account, o.Recipient)
			if err != nil {
				return err
			}
			if !truth(check["eligible"]) || str(check["recipient_id"]) != original.RecipientID || s.ConfirmedRecipientID != "" && s.ConfirmedRecipientID != original.RecipientID {
				return commerce.Fail("recipient_changed", "接收账号或赠送资格已变化，请核对是否已经到账", 409)
			}
			if err = xQuote(ctx, s, original.Account); err != nil {
				return err
			}
			invoiceID = security.ID("invoice")
			_, err = tx.Exec(ctx, "INSERT INTO manual_payment_invoices(id,order_id,previous_session_hash,previous_job_cipher,status,actor_id) VALUES($1,$2,$3,$4,'creating',$5)", invoiceID, id, security.Hash(original.Session), originalCipher, actor)
			if err != nil {
				return err
			}
			if err = store.Audit(ctx, tx, actor, "manual_invoice.requested", id, "管理员确认旧账单未付款；生成一张新账单供人工付款；保留旧付款记录"); err != nil {
				return err
			}
			dispatch = true
		}
		// Keep worker and concurrent administrator actions away from this order only.
		if _, err = tx.Exec(ctx, "INSERT INTO resource_locks(resource,order_id) VALUES($1,$2) ON CONFLICT DO NOTHING", "x-recipient:"+original.RecipientID, id); err != nil {
			return err
		}
		var recipientOwner string
		if err = tx.QueryRow(ctx, "SELECT order_id FROM resource_locks WHERE resource=$1", "x-recipient:"+original.RecipientID).Scan(&recipientOwner); err != nil {
			return err
		}
		if recipientOwner != id {
			return commerce.Fail("recipient_busy", "该接收账号另有订单正在处理，请先核对", 409)
		}
		_, err = tx.Exec(ctx, "UPDATE orders SET lease_token=$2,lease_until=now()+interval '5 minutes' WHERE id=$1", id, invoiceID)
		return err
	})
	if err != nil || reuseURL != "" {
		return reuseURL, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_, _ = e.Store.DB.Exec(cleanup, "UPDATE orders SET lease_token='',lease_until=NULL,next_at=now() WHERE id=$1 AND lease_token=$2", id, invoiceID)
	}()
	if dispatch {
		if err = e.dispatchAllowed(ctx, id, invoiceID); err != nil {
			return "", err
		}
		success := "https://x.com/" + o.Recipient + "/gift-premium/success"
		created, createErr := xQuery(ctx, s, original.Account, "useOneTimePurchaseGiftMutation", "GqTVJ4S1526tLkxj69xIZw", Object{"cancel_url": strings.TrimSuffix(success, "/success"), "success_url": success, "external_product_id": str(s.ProductConfig["stripe_product"]), "gift_recipient": original.RecipientID})
		row := obj(created["onetimepurchase_gift"])
		payload = manualInvoicePayload{Job: original, URL: str(row["session_url"]), Response: created}
		payload.Job.Session = str(row["session_id"])
		payload.Job.ManualInvoiceID = invoiceID
		payload.Job.Method, payload.Job.DeclinedIntent, payload.Job.DeclinedMethod = "", "", ""
		payload.Job.CardAttempt, payload.Job.Proof, payload.Job.Checksum = 0, nil, ""
		payload.Job.UpstreamRef = payload.Job.Session
		payload.Job.Stage = "submitted"
		state := "created"
		if createErr != nil || str(row["session_status"]) != "Unpaid" || !regexp.MustCompile(`^cs_live_[A-Za-z0-9]+$`).MatchString(payload.Job.Session) || payload.Job.Session == original.Session {
			state = "unknown"
		}
		b, _ := json.Marshal(payload)
		persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		_, saveErr := e.Store.DB.Exec(persist, "UPDATE manual_payment_invoices SET status=$2,payload_cipher=$3,updated_at=now() WHERE id=$1 AND status='creating'", invoiceID, state, e.Store.Vault.Seal("manual-invoice:"+invoiceID, b))
		cancel()
		if saveErr != nil {
			return "", saveErr
		}
		if state == "unknown" {
			return "", manualInvoiceError()
		}
	}
	page, err := stripe(ctx, s, "POST", "payment_pages/"+payload.Job.Session+"/init", url.Values{"browser_locale": {"en"}, "redirect_type": {"url"}}, "")
	if err != nil {
		return "", commerce.Fail("invoice_verification_pending", "新账单已创建，付款链接仍在核对；请稍后重试此按钮，不会重复创建", 502)
	}
	if err = guardManualInvoice(page, o, s, &payload.Job); err != nil {
		return "", err
	}
	link, err := invoiceLink(page, payload.URL, payload.Job.Session)
	if err != nil {
		return "", err
	}
	payload.URL, payload.Job.Proof, payload.Job.Checksum = link, page, str(page["init_checksum"])
	delete(payload.Job.Proof, "payment_intent")
	job, _ := json.Marshal(payload.Job)
	b, _ := json.Marshal(payload)
	err = e.Store.Tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE orders SET job_cipher=$3,upstream_ref=$4,failure_code='manual_invoice_ready',updated_at=now()
 WHERE id=$1 AND lease_token=$2 AND lease_until>now() AND job_cipher=$5 AND status IN ('unknown','running')`, id, invoiceID, e.Store.Vault.Seal("job:"+id, job), payload.Job.Session, originalCipher)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return commerce.Fail("operation_busy", "订单记录已变化，请核对新账单记录", 409)
		}
		if _, err = tx.Exec(ctx, "UPDATE manual_payment_invoices SET status='ready',payload_cipher=$2,updated_at=now() WHERE id=$1", invoiceID, e.Store.Vault.Seal("manual-invoice:"+invoiceID, b)); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "UPDATE payment_captcha_attempts SET status='failed',failure_code='captcha_manual_invoice_replaced',updated_at=now() WHERE order_id=$1 AND status='pending'", id); err != nil {
			return err
		}
		return store.Audit(ctx, tx, actor, "manual_invoice.ready", id, "新账单已绑定原订单，等待管理员人工付款；未调用付款确认或卡台接口")
	})
	return link, err
}
