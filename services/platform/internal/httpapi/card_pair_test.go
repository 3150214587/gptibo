package httpapi

import (
	"context"
	"encoding/json"
	"gptibo/platform/internal/commerce"
	"testing"
)

func TestPairRuleValidatesTwoDistinctCardsAndShowsMaskedCounters(t *testing.T) {
	s, h := fixture(t)
	ctx := context.Background()
	p, _ := json.Marshal(commerce.ProviderConfig{APIBase: "https://cards.example.invalid/openapi/v1", Environment: "sandbox", APIKey: "synthetic"})
	if _, err := s.Store.DB.Exec(ctx, "INSERT INTO settings(id,payload_cipher) VALUES('provider',$1)", s.Store.Vault.Seal("setting:provider", p)); err != nil {
		t.Fatal(err)
	}
	validations := 0
	s.ValidatePair = func(context.Context, commerce.CardRule) (map[int64]string, error) {
		validations++
		return map[int64]string{42: "0042", 43: "0043"}, nil
	}
	r := commerce.CardRule{Business: "x", CardProduct: "TEST-BIN", CardID: 42, BackupCardID: 43, AutoOpen: true, ReplaceOnDecline: true, MaxReplacements: 1, FundingMinor: 1000, MaxOrderMinor: 1000, DailyLimitMinor: 10000, Enabled: true}
	send := func() int {
		b, _ := json.Marshal(r)
		resp := call(h, "POST", "/api/admin/card-rules", "admin-session", string(b))
		if resp.Code >= 500 {
			t.Fatal(resp.Body.String())
		}
		return resp.Code
	}
	r.BackupCardID = 42
	if got := send(); got != 400 || validations != 0 {
		t.Fatal("same card accepted", got)
	}
	r.BackupCardID = 43
	if got := send(); got != 200 || validations != 1 {
		t.Fatal("valid pair rejected", got)
	}
	// Editing roles doesn't erase per-card history; swapping must be atomic.
	r.CardID = 43
	r.BackupCardID = 42
	if got := send(); got != 200 {
		t.Fatal("role swap failed", got)
	}
	got := call(h, "GET", "/api/admin/card-rules", "admin-session", "")
	if got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	var result struct {
		Data []struct {
			Pool []struct {
				CardID   int64  `json:"card_id"`
				LastFour string `json:"last_four"`
				Declines int    `json:"declines"`
			} `json:"pool"`
		}
	}
	if err := json.Unmarshal(got.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Data) != 1 || len(result.Data[0].Pool) != 2 || result.Data[0].Pool[0].CardID != 43 || result.Data[0].Pool[0].LastFour != "0043" {
		t.Fatal(got.Body.String())
	}
	if got := call(h, "GET", "/api/admin/card-rules", "one-session", ""); got.Code != 403 {
		t.Fatal("channel accessed pool")
	}
}
