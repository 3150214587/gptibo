package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"gptibo/platform/internal/commerce"
	"gptibo/platform/internal/security"
	"time"
)

type cardReplacement struct {
	Environment string
	Old         int64
	Rule        string
	Slot        int
	Order       string
	Config      string
	Status      string
	Balance     *int64
	Prior       []byte
	Job         string
	Token       string
}

// Maintenance is independent of order settlement: a successful backup payment
// must not lose a pending card closure, and a closure never changes customer money.
func (e *Engine) MaintainCards(ctx context.Context) (bool, error) {
	t := cardReplacement{Token: security.Token()}
	err := e.Store.DB.QueryRow(ctx, `WITH candidate AS (
  SELECT environment,old_card_id FROM card_replacements r WHERE status<>'completed' AND next_at<=now()
   AND (lease_until IS NULL OR lease_until<now())
   AND NOT EXISTS(SELECT 1 FROM orders o WHERE o.id=r.order_id AND o.lease_until>now())
   AND NOT EXISTS(SELECT 1 FROM resource_locks l WHERE l.resource='card:'||r.environment||':'||r.old_card_id)
  ORDER BY next_at FOR UPDATE SKIP LOCKED LIMIT 1)
 UPDATE card_replacements r SET lease_token=$1,lease_until=now()+interval '3 minutes'
 FROM candidate c WHERE r.environment=c.environment AND r.old_card_id=c.old_card_id
 RETURNING r.environment,r.old_card_id,r.rule_id,r.slot,r.order_id,r.config_cipher,r.status,r.close_balance_minor,r.prior_flow_ids,r.job_cipher`, t.Token).
		Scan(&t.Environment, &t.Old, &t.Rule, &t.Slot, &t.Order, &t.Config, &t.Status, &t.Balance, &t.Prior, &t.Job)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	taskCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	err = e.maintainCard(taskCtx, &t)
	code := ""
	if err != nil {
		code = "card_replacement_unconfirmed"
		var up *UpstreamError
		if errors.As(err, &up) {
			code = up.Code
		}
	}
	_, finishErr := e.Store.DB.Exec(ctx, "UPDATE card_replacements SET last_error=$4,next_at=now()+interval '60 seconds',lease_until=NULL,updated_at=now() WHERE environment=$1 AND old_card_id=$2 AND lease_token=$3", t.Environment, t.Old, t.Token, code)
	return true, finishErr
}

func (e *Engine) replacementAllowed(ctx context.Context, t *cardReplacement, s commerce.Snapshot) error {
	p, err := (&commerce.Service{Store: e.Store}).Provider(ctx)
	if err != nil {
		return err
	}
	if !p.WritesEnabled || p.Environment != s.Provider.Environment || p.APIKey != s.Provider.APIKey {
		return &UpstreamError{"execution_paused", false}
	}
	var allowed bool
	err = e.Store.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM card_replacements r JOIN card_rules cr ON cr.id=r.rule_id
  JOIN card_pool_slots p ON p.rule_id=r.rule_id AND p.environment=r.environment AND p.slot=r.slot
  WHERE r.environment=$1 AND r.old_card_id=$2 AND r.lease_token=$3 AND r.lease_until>now()
  AND cr.enabled AND cr.auto_open AND cr.replace_on_decline AND cr.backup_card_id>0
  AND cr.card_product=$4 AND p.card_id=r.old_card_id)`, t.Environment, t.Old, t.Token, s.Rule.CardProduct).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return &UpstreamError{"card_replacement_paused", false}
	}
	return nil
}

func (e *Engine) replacementUpdate(ctx context.Context, t *cardReplacement, clause string, args ...any) error {
	params := []any{t.Environment, t.Old, t.Token}
	params = append(params, args...)
	tag, err := e.Store.DB.Exec(ctx, "UPDATE card_replacements SET "+clause+",updated_at=now() WHERE environment=$1 AND old_card_id=$2 AND lease_token=$3 AND lease_until>now()", params...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("maintenance_lease_lost")
	}
	return nil
}

func cardFlowIDs(data any) ([]string, error) {
	rows, ok := data.([]any)
	if !ok {
		return nil, &UpstreamError{"card_refund_unconfirmed", false}
	}
	ids := []string{}
	for _, v := range rows {
		r := obj(v)
		id := str(r["id"])
		if id == "" {
			return nil, &UpstreamError{"card_refund_unconfirmed", false}
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (e *Engine) maintainCard(ctx context.Context, t *cardReplacement) error {
	raw, err := e.Store.Vault.Open(replacementPurpose(t.Environment, t.Old), t.Config)
	if err != nil {
		return err
	}
	var s commerce.Snapshot
	if err = json.Unmarshal(raw, &s); err != nil {
		return err
	}
	if err = e.replacementAllowed(ctx, t, s); err != nil {
		return err
	}
	path := fmt.Sprintf("/cards/%d", t.Old)
	if t.Status == "pending" {
		card, err := cardAPI(ctx, s.Provider, "GET", path+"?sync=1", nil, "")
		if err != nil {
			return err
		}
		c := obj(card["data"])
		balance, valid := usdBalanceMinor(c["available_amount"])
		if !matchesID(c["id"], t.Old) || str(c["product_code"]) != s.Rule.CardProduct || !valid || c["balance_stale"] == true || c["status_change_pending"] == true || str(c["status"]) != "ACTIVE" && str(c["status"]) != "FROZEN" {
			return &UpstreamError{"card_close_precheck_unconfirmed", false}
		}
		// Full bounded pagination: any unsettled authorization postpones closure.
		complete := false
		for page := 1; page <= 20; page++ {
			txs, err := cardAPI(ctx, s.Provider, "GET", fmt.Sprintf("%s/transactions?page=%d&page_size=100&sync=1", path, page), nil, "")
			if err != nil {
				return err
			}
			rows, ok := txs["data"].([]any)
			if !ok {
				return &UpstreamError{"card_transactions_unconfirmed", false}
			}
			for _, v := range rows {
				r := obj(v)
				status := str(r["status"])
				if status != "COMPLETE" && status != "DECLINED" {
					return &UpstreamError{"card_settlement_pending", false}
				}
			}
			if len(rows) < 100 {
				complete = true
				break
			}
		}
		if !complete {
			return &UpstreamError{"card_transactions_unconfirmed", false}
		}
		flows, err := cardAPI(ctx, s.Provider, "GET", path+"/fund-flows", nil, "")
		if err != nil {
			return err
		}
		ids, err := cardFlowIDs(flows["data"])
		if err != nil {
			return err
		}
		encoded, _ := json.Marshal(ids)
		if err = e.replacementAllowed(ctx, t, s); err != nil {
			return err
		}
		if err = e.replacementUpdate(ctx, t, "status='closing',close_balance_minor=$4,prior_flow_ids=$5", balance, encoded); err != nil {
			return err
		}
		t.Balance = &balance
		t.Prior = encoded
		t.Status = "closing"
		// The intent is durable before dispatch. A timeout/202 is queried, never resent.
		reply, err := cardAPI(ctx, s.Provider, "DELETE", path, nil, fmt.Sprintf("pair-close:%s:%d", t.Environment, t.Old))
		if err != nil {
			return err
		}
		if reply["_http_status"] == 200 && obj(reply["data"])["pending"] != true {
			if err = e.replacementUpdate(ctx, t, "status='closed'"); err != nil {
				return err
			}
			t.Status = "closed"
		}
	}
	if t.Status == "closing" {
		card, err := cardAPI(ctx, s.Provider, "GET", path+"?sync=1", nil, "")
		if err != nil {
			return err
		}
		c := obj(card["data"])
		if !matchesID(c["id"], t.Old) || str(c["status"]) != "DELETED" && str(c["status"]) != "CANCELLED" {
			return &UpstreamError{"card_close_pending", false}
		}
		if err = e.replacementUpdate(ctx, t, "status='closed'"); err != nil {
			return err
		}
		t.Status = "closed"
	}
	if t.Status == "closed" {
		if t.Balance == nil {
			return &UpstreamError{"card_refund_unconfirmed", false}
		}
		flows, err := cardAPI(ctx, s.Provider, "GET", path+"/fund-flows", nil, "")
		if err != nil {
			return err
		}
		if _, err = cardFlowIDs(flows["data"]); err != nil {
			return err
		}
		var prior []string
		if err = json.Unmarshal(t.Prior, &prior); err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, id := range prior {
			seen[id] = true
		}
		refund := int64(0)
		for _, v := range flows["data"].([]any) {
			r := obj(v)
			id := str(r["id"])
			amount, ok := exactNumber(r["amount"])
			if !seen[id] && str(r["type"]) == "card_refund" && str(r["direction"]) == "out" && ok && amount < 0 {
				minor, valid := usdBalanceMinor(-amount)
				if !valid {
					return &UpstreamError{"card_refund_unconfirmed", false}
				}
				refund += minor
				seen[id] = true
			}
		}
		if refund < *t.Balance {
			return &UpstreamError{"card_refund_pending", false}
		}
		if err = e.replacementUpdate(ctx, t, "status='opening',refund_minor=$4", refund); err != nil {
			return err
		}
		t.Status = "opening"
	}
	if t.Status == "opening" {
		j := Job{Stage: "card", CardAttempt: 100 + t.Slot}
		if t.Job != "" {
			raw, err := e.Store.Vault.Open(replacementPurpose(t.Environment, t.Old)+":job", t.Job)
			if err != nil {
				return err
			}
			if err = json.Unmarshal(raw, &j); err != nil {
				return err
			}
		}
		funding := s
		funding.Rule.CardID = 0
		funding.Rule.BackupCardID = 0
		funding.Rule.ReplaceOnDecline = false
		save := func() error {
			if err := e.replacementAllowed(ctx, t, s); err != nil {
				return err
			}
			b, _ := json.Marshal(j)
			return e.replacementUpdate(ctx, t, "job_cipher=$4", e.Store.Vault.Seal(replacementPurpose(t.Environment, t.Old)+":job", b))
		}
		id, err := e.card(ctx, commerce.Order{ID: t.Order}, funding, &j, save)
		if err != nil {
			return err
		}
		data, err := cardAPI(ctx, s.Provider, "GET", fmt.Sprintf("/cards/%d", id), nil, "")
		if err != nil {
			return err
		}
		card := obj(data["data"])
		pan := str(card["card_number"])
		if id == t.Old || !matchesID(card["id"], id) || len(pan) < 4 {
			return &UpstreamError{"card_evidence_mismatch", false}
		}
		var unused bool
		if err = e.Store.DB.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM card_pool_slots WHERE environment=$1 AND card_id=$2)
 AND NOT EXISTS(SELECT 1 FROM card_retirements WHERE environment=$1 AND card_id=$2)
 AND NOT EXISTS(SELECT 1 FROM card_declines WHERE environment=$1 AND card_id=$2)`, t.Environment, id).Scan(&unused); err != nil {
			return err
		}
		if !unused {
			return &UpstreamError{"card_evidence_mismatch", false}
		}
		return e.Store.Tx(ctx, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `UPDATE card_pool_slots SET card_id=$4,last_four=$5,updated_at=now() WHERE rule_id=$1 AND environment=$2 AND slot=$3 AND card_id=$6
    AND EXISTS(SELECT 1 FROM card_replacements WHERE environment=$2 AND old_card_id=$6 AND lease_token=$7 AND lease_until>now())`, t.Rule, t.Environment, t.Slot, id, pan[len(pan)-4:], t.Old, t.Token)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return errors.New("pool_slot_changed")
			}
			if _, err = tx.Exec(ctx, "UPDATE card_replacements SET status='completed',new_card_id=$4,updated_at=now() WHERE environment=$1 AND old_card_id=$2 AND lease_token=$3", t.Environment, t.Old, t.Token, id); err != nil {
				return err
			}
			_, err = tx.Exec(ctx, "DELETE FROM resource_locks WHERE order_id=$1 AND resource=$2", t.Order, fmt.Sprintf("card:%s:%d", t.Environment, id))
			return err
		})
	}
	return nil
}
