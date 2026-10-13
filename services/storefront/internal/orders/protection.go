package orders

import (
	"context"
	_ "embed"
	"encoding/json"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"gptibo/storefront/internal/fault"
)

// The purchase form imports this same list. Match complete domains, never suffixes.
//
//go:embed email-domains.json
var emailDomainsJSON []byte

var allowedEmailDomains = func() map[string]bool {
	var domains []string
	if err := json.Unmarshal(emailDomainsJSON, &domains); err != nil {
		panic(err)
	}
	allowed := make(map[string]bool, len(domains))
	for _, domain := range domains {
		allowed[domain] = true
	}
	return allowed
}()

func AllowedOrderEmail(email string) bool {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(email)), "@")
	return len(parts) == 2 && parts[0] != "" && allowedEmailDomains[parts[1]]
}

// Canonicalization affects abuse budgets only; delivery retains the supplied address.
func orderEmailBudget(email string) string {
	local, domain, _ := strings.Cut(email, "@")
	local, _, _ = strings.Cut(local, "+")
	if domain == "gmail.com" || domain == "googlemail.com" {
		domain = "gmail.com"
		local = strings.ReplaceAll(local, ".", "")
	}
	return local + "@" + domain
}

// Run only for a new order, inside its transaction. Locks and durable counters
// prevent parallel requests, instance restarts and idempotency-key rotation bypasses.
func (s Service) protectCreation(ctx context.Context, tx pgx.Tx, access Access, email string) error {
	if !AllowedOrderEmail(email) {
		return fault.New(400, "下单仅支持常用邮箱，请使用 QQ、Gmail、网易、Outlook 或 iCloud 等邮箱。")
	}
	for _, key := range []string{"order-owner:" + access.OwnerHash, "order-email:" + orderEmailBudget(email)} {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,10))`, key); err != nil {
			return err
		}
	}
	var ownerPending, emailPending int
	err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE owner_hash=$1),count(*) FILTER (WHERE customer_email=$2)
 FROM orders WHERE (owner_hash=$1 OR customer_email=$2) AND is_test=0
 AND status='pending' AND payment_status='unpaid' AND created_at>$3`, access.OwnerHash, email, time.Now().Add(-24*time.Hour).UnixMilli()).Scan(&ownerPending, &emailPending)
	if err != nil {
		return err
	}
	if ownerPending >= 3 || emailPending >= 3 {
		return fault.New(429, "待付款订单较多，请先完成已有订单，或稍后再试。")
	}
	type creationBudget struct {
		key          string
		max, seconds int
	}
	budgets := []creationBudget{
		{"order-new-owner:" + access.OwnerHash, 20, 3600},
		{"order-new-email:" + orderEmailBudget(email), 10, 3600},
	}
	if access.ClientIP != "" {
		budgets = append(budgets,
			creationBudget{"order-new-ip-minute:" + access.ClientIP, 6, 60},
			creationBudget{"order-new-ip-hour:" + access.ClientIP, 30, 3600})
	}
	for _, budget := range budgets {
		if err := s.Auth.LimitIn(ctx, tx, budget.key, budget.max, budget.seconds); err != nil {
			return err
		}
	}
	return nil
}
