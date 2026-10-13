package recharge

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gptibo/storefront/internal/security"
)

func (s *Service) Webhook(ctx context.Context, r *http.Request) error {
	invalid := errors.New("notification verification failed")
	if s.Client == nil || s.WebhookSecret == "" || r.Method != "POST" || !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		return invalid
	}
	for _, h := range []string{"X-Quefa-Timestamp", "X-Quefa-Signature", "X-Quefa-Event-Id", "X-Quefa-Event", "X-Quefa-Delivery"} {
		if len(r.Header.Values(h)) != 1 {
			return invalid
		}
	}
	stamp, sig, event, kind, delivery := r.Header.Get("X-Quefa-Timestamp"), r.Header.Get("X-Quefa-Signature"), r.Header.Get("X-Quefa-Event-Id"), r.Header.Get("X-Quefa-Event"), r.Header.Get("X-Quefa-Delivery")
	n, e := strconv.ParseInt(stamp, 10, 64)
	delta := time.Now().Unix() - n
	if e != nil || len(stamp) != 10 || delta > 300 || delta < -300 || !remoteID.MatchString(event) || !remoteID.MatchString(delivery) || !regexp.MustCompile(`^[a-z.]{1,64}$`).MatchString(kind) {
		return invalid
	}
	raw, e := bounded(r.Body, 64000)
	if e != nil {
		return invalid
	}
	want := "t=" + stamp + ",v1=" + HMAC(s.WebhookSecret, stamp+"."+string(raw))
	if !hmac.Equal([]byte(want), []byte(sig)) {
		return invalid
	}
	var payload map[string]any
	if json.Unmarshal(raw, &payload) != nil || payload == nil {
		return invalid
	}
	for k, v := range map[string]string{"event_id": event, "type": kind, "event": kind} {
		if got, ok := payload[k]; ok && got != v {
			return invalid
		}
	}
	// A notification belongs to one order. Waking every order in the account
	// repeatedly resets the entire queue and starves current recharge tasks.
	var identity struct {
		Order    string `json:"order_id"`
		Merchant string `json:"merchant_order_no"`
		Data     struct {
			Order    string `json:"order_id"`
			Merchant string `json:"merchant_order_no"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &identity) != nil {
		return invalid
	}
	order, merchant := identity.Data.Order, identity.Data.Merchant
	if order == "" {
		order = identity.Order
	} else if identity.Order != "" && identity.Order != order {
		return invalid
	}
	if merchant == "" {
		merchant = identity.Merchant
	} else if identity.Merchant != "" && identity.Merchant != merchant {
		return invalid
	}
	if order != "" && !remoteID.MatchString(order) {
		return invalid
	}
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	account, digest := s.Client.Account(), security.Hash(string(raw))
	tag, e := tx.Exec(ctx, `INSERT INTO recharge_webhooks(account,event_id,event_type,body_hash,received_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT(account,event_id) DO NOTHING`, account, event, kind, digest, time.Now().UnixMilli())
	if e != nil {
		return e
	}
	var previous string
	if e = tx.QueryRow(ctx, `SELECT body_hash FROM recharge_webhooks WHERE account=$1 AND event_id=$2`, account, event).Scan(&previous); e != nil {
		return e
	}
	if previous != digest {
		return invalid
	}
	if tag.RowsAffected() == 1 && (order != "" || merchant != "") {
		if _, e = tx.Exec(ctx, `UPDATE recharge_orders SET updated_at=0,notify_version=notify_version+1 WHERE account=$1 AND (remote_id=$2 OR (remote_id IS NULL AND order_id=$3))`, account, order, merchant); e != nil {
			return e
		}
	}
	if _, e = tx.Exec(ctx, `SELECT pg_notify('storefront_work',current_schema())`); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
