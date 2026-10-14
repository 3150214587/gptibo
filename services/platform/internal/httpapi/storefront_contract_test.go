package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"gptibo/platform/internal/commerce"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestActualStorefrontClientAgainstSupplyAPI(t *testing.T) {
	s, _ := fixture(t)
	ctx := context.Background()
	provider := commerce.ProviderConfig{APIBase: "https://cards.example.invalid/openapi/v1", Environment: "production", APIKey: "sk_synthetic", WritesEnabled: true, StripeKey: "pk_live_synthetic", XBearer: "synthetic", Billing: commerce.Billing{FirstName: "Test", LastName: "User", Email: "test@example.invalid", Country: "US", Line1: "Test", City: "Test", Postal: "00000"}}
	b, _ := json.Marshal(provider)
	for _, query := range []string{
		`UPDATE wallets SET available_minor=10000 WHERE user_id='one'`,
		`UPDATE products SET supply_minor=137,enabled=true WHERE code='x-3m'`,
		`INSERT INTO worker_heartbeats(id) VALUES('contract-worker')`,
		`INSERT INTO card_rules(id,business,card_product,card_id,funding_minor,max_order_minor,daily_limit_minor) VALUES('fixture-rule','x','DEMO-CARD',42,1000,500,1000)`,
		`INSERT INTO x_accounts(id,name,payload_cipher,daily_limit) VALUES('fixture-account','Test','unused',1)`,
	} {
		if _, err := s.Store.DB.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Store.DB.Exec(ctx, `INSERT INTO settings(id,payload_cipher) VALUES('provider',$1)`, s.Store.Vault.Seal("setting:provider", b)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.DB.Exec(ctx, `INSERT INTO api_keys(id,user_id,label,secret_cipher) VALUES('contract-key','one','Test',$1)`, s.Store.Vault.Seal("api-key:contract-key", []byte("synthetic-contract-secret"))); err != nil {
		t.Fatal(err)
	}
	// Only the external eligibility service is simulated; both HTTP API and client
	// are the actual applications, using TLS, HMAC, nonce storage and real SQL.
	s.Preflight = func(context.Context, string, string, commerce.Credential) (any, error) {
		return map[string]any{"recipient": "receiver", "recipient_id": "12345", "eligible": true}, nil
	}
	ts := httptest.NewTLSServer(s.Router())
	defer ts.Close()
	dir, err := filepath.Abs("../../../storefront")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "giftcontract")
	buildCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	build := exec.CommandContext(buildCtx, "go", "build", "-o", binary, "./internal/testutil/giftcontract")
	build.Dir = dir
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("client build: %v %s", err, output)
	}
	invoke := func(phase string) []byte {
		t.Helper()
		input, _ := json.Marshal(map[string]string{"URL": ts.URL, "Certificate": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw})), "Key": "contract-key", "Secret": "synthetic-contract-secret", "Phase": phase})
		cmd := exec.CommandContext(buildCtx, binary)
		cmd.Stdin = bytes.NewReader(input)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v %s", phase, err, out)
		}
		return out
	}
	var result struct{ ID string }
	if err := json.Unmarshal(invoke("create"), &result); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.DB.Exec(ctx, `UPDATE orders SET lease_token='contract',lease_until=now()+interval '1 minute' WHERE id=$1`, result.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Commerce.Finish(ctx, result.ID, "contract", "succeeded", "done", "", "rcpt_"+strings.Repeat("a", 24)); err != nil {
		t.Fatal(err)
	}
	invoke("verify")
	var settles, freezes int
	if err := s.Store.DB.QueryRow(ctx, `SELECT count(*) FILTER(WHERE kind='consume'),count(*) FILTER(WHERE kind='reserve') FROM ledger WHERE order_id=$1`, result.ID).Scan(&settles, &freezes); err != nil || settles != 1 || freezes != 1 {
		t.Fatal("duplicate ledger entries", settles, freezes, err)
	}
}
