package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gptibo/platform/internal/commerce"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func pairFixture(t *testing.T) (*Engine, commerce.Order, commerce.Snapshot) {
	e, o, s := orderFixture(t)
	s.Business = "x"
	s.Rule.BackupCardID = 43
	s.Rule.ReplaceOnDecline = true
	s.Rule.UseProviderLimits = true
	s.Rule.MaxReplacements = 1
	s.Rule.RefillThresholdMinor = 700
	s.Rule.FundingMinor = 1000
	s.Rule.OpenAmountMinor = 2000
	s.Rule.MaxOpenFeeMinor = 50
	s.Rule.DailyLimitMinor = 100000
	s.Rule.Enabled = true
	s.Provider.WritesEnabled = true
	s.Provider.Billing.FirstName = "Test"
	s.Provider.Billing.LastName = "Owner"
	ctx := context.Background()
	b, _ := json.Marshal(s.Provider)
	_, err := e.Store.DB.Exec(ctx, "INSERT INTO settings(id,payload_cipher) VALUES('provider',$1)", e.Store.Vault.Seal("setting:provider", b))
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.Store.DB.Exec(ctx, `INSERT INTO card_rules(id,business,card_product,card_id,backup_card_id,auto_open,replace_on_decline,funding_minor,max_order_minor,daily_limit_minor,enabled)
  VALUES('rule','x','TEST-BIN',42,43,true,true,1000,1000,100000,true);
  INSERT INTO card_pool_slots(rule_id,environment,slot,card_product,card_id,last_four) VALUES('rule','sandbox',1,'TEST-BIN',42,'0042'),('rule','sandbox',2,'TEST-BIN',43,'0043')`)
	if err != nil {
		t.Fatal(err)
	}
	return e, o, s
}

func pairJob(id int64) Job {
	return Job{Stage: "declined", CardID: id, Method: "pm_old", DeclinedMethod: "pm_old", DeclinedIntent: "pi_original", DeclineCode: "insufficient_funds", PoolCards: []int64{42, 43}}
}

func TestPairFirstDeclineSwitchesWithoutRetiringOrOpening(t *testing.T) {
	e, o, s := pairFixture(t)
	ctx := context.Background()
	j := pairJob(42)
	result, err := e.replaceDeclinedCard(ctx, o, s, &j, func() error { return nil })
	if err != nil || result.Status != "running" || j.CardAttempt != 1 {
		t.Fatal(result, err)
	}
	if err = e.selectPairCard(ctx, o, s, &j, func() error { return nil }); err != nil || j.CardID != 43 {
		t.Fatal("did not use backup", err)
	}
	var retired int
	_ = e.Store.DB.QueryRow(ctx, "SELECT count(*) FROM card_retirements").Scan(&retired)
	if retired != 0 {
		t.Fatal("retired on first decline")
	}
	// An intervening success is not a reset; the next order prefers the primary.
	next := commerce.Order{ID: "next"}
	j2 := Job{}
	if err = e.selectPairCard(ctx, next, s, &j2, func() error { return nil }); err != nil || j2.CardID != 42 {
		t.Fatal("primary not retained", err)
	}
}

func TestPairDuplicateDeclineAndCumulativeSecondDecline(t *testing.T) {
	e, o, s := pairFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j := pairJob(42)
			_, err := e.replaceDeclinedCard(ctx, o, s, &j, func() error { return nil })
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var n int
	_ = e.Store.DB.QueryRow(ctx, "SELECT count(*) FROM card_declines").Scan(&n)
	if n != 1 {
		t.Fatal("duplicate counted", n)
	}
	_, err := e.Store.DB.Exec(ctx, `INSERT INTO orders(id,user_id,merchant_order_no,idempotency_key,request_hash,product_code,business,supply_minor,config_cipher)
 SELECT 'next',user_id,'next','next',request_hash,product_code,business,supply_minor,config_cipher FROM orders WHERE id=$1`, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	j := pairJob(42)
	j.DeclinedIntent = "pi_next"
	j.Method = "pm_next"
	j.DeclinedMethod = j.Method
	if _, err = e.replaceDeclinedCard(ctx, commerce.Order{ID: "next"}, s, &j, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	_ = e.Store.DB.QueryRow(ctx, "SELECT count(*) FROM card_declines WHERE card_id=42").Scan(&n)
	if n != 2 {
		t.Fatal(n)
	}
	_ = e.Store.DB.QueryRow(ctx, "SELECT count(*) FROM card_replacements WHERE old_card_id=42 AND slot=1").Scan(&n)
	if n != 1 {
		t.Fatal("replacement not queued")
	}
	if _, err = e.Store.DB.Exec(ctx, "DELETE FROM card_declines"); err == nil {
		t.Fatal("declines mutable")
	}
}

func TestPairBothDeclineStopsEvenIfNewCardWasAdded(t *testing.T) {
	e, o, s := pairFixture(t)
	ctx := context.Background()
	j := pairJob(42)
	if _, err := e.replaceDeclinedCard(ctx, o, s, &j, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	j.CardID = 43
	j.Method = "pm_backup"
	j.DeclinedMethod = j.Method
	j.DeclineCode = "generic_decline"
	j.Stage = "declined"
	_, err := e.Store.DB.Exec(ctx, "UPDATE card_pool_slots SET card_id=44 WHERE slot=1; INSERT INTO resource_locks(resource,order_id) VALUES('x-account:test','test-order'),('card-rule:rule','test-order'),('x-recipient:test','test-order')")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		result, err := e.replaceDeclinedCard(ctx, o, s, &j, func() error { t.Fatal("third card attempted"); return nil })
		if err != nil || result.Failure != "card_pair_declined" || result.Status != "unknown" {
			t.Fatal(result, err)
		}
	}
	var n int
	_ = e.Store.DB.QueryRow(ctx, "SELECT count(*) FROM card_declines").Scan(&n)
	if n != 2 {
		t.Fatal(n)
	}
	_ = e.Store.DB.QueryRow(ctx, "SELECT count(*) FROM resource_locks WHERE resource IN ('card-rule:rule','x-account:test')").Scan(&n)
	if n != 0 {
		t.Fatal("unrelated orders blocked")
	}
	_ = e.Store.DB.QueryRow(ctx, "SELECT count(*) FROM resource_locks WHERE resource='x-recipient:test'").Scan(&n)
	if n != 1 {
		t.Fatal("recipient protection lost")
	}
}

func TestPairBackupLowBalanceFundsExistingCardOnce(t *testing.T) {
	e, o, s := pairFixture(t)
	ctx := context.Background()
	j := Job{Stage: "card", CardAttempt: 1, CardID: 43, PoolCards: []int64{42, 43}}
	old := client
	defer func() { client = old }()
	balance := 5.3
	posts := 0
	client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/products"):
			return response(`{"code":0,"data":[{"product_code":"TEST-BIN","min_amount":20,"open_fee":0.5,"recharge_fee":0}]}`), nil
		case strings.HasSuffix(r.URL.Path, "/balance"):
			return response(`{"code":0,"data":{"currency":"USD","spendable_balance":50}}`), nil
		case r.Method == "POST":
			posts++
			var b Object
			_ = json.NewDecoder(r.Body).Decode(&b)
			if r.URL.Path != "/openapi/v1/cards/recharge" || number(b["amount"]) != 10 || !matchesID(b["card_id"], 43) {
				t.Fatal("wrong card or amount")
			}
			balance += 10
			return response(`{"code":0}`), nil
		default:
			return response(fmt.Sprintf(`{"code":0,"data":{"id":43,"product_code":"TEST-BIN","status":"ACTIVE","available_amount":%g}}`, balance)), nil
		}
	})}
	for i := 0; i < 2; i++ {
		id, err := e.card(ctx, o, s, &j, func() error { return nil })
		if err != nil || id != 43 {
			t.Fatal(id, err)
		}
	}
	if posts != 1 {
		t.Fatal("duplicate refill", posts)
	}
}

func TestCardMaintenanceWaitsForSettlementAndRefundThenReplacesOnce(t *testing.T) {
	e, o, s := pairFixture(t)
	ctx := context.Background()
	raw, _ := json.Marshal(s)
	_, err := e.Store.DB.Exec(ctx, "INSERT INTO card_retirements(environment,card_id,order_id,reason) VALUES('sandbox',42,$1,'two_declines');", o.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.Store.DB.Exec(ctx, "INSERT INTO card_replacements(environment,old_card_id,rule_id,slot,order_id,config_cipher) VALUES('sandbox',42,'rule',1,$1,$2)", o.ID, e.Store.Vault.Seal(replacementPurpose("sandbox", 42), raw))
	if err != nil {
		t.Fatal(err)
	}
	old := client
	defer func() { client = old }()
	settled, deleted, refundReady := false, false, false
	deletes, opens := 0, 0
	client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/transactions"):
			if !settled {
				return response(`{"code":0,"data":[{"status":"PENDING"}]}`), nil
			}
			return response(`{"code":0,"data":[{"status":"COMPLETE"}]}`), nil
		case strings.HasSuffix(r.URL.Path, "/fund-flows"):
			if refundReady {
				return response(`{"code":0,"data":[{"id":1,"type":"card_refund","direction":"out","amount":-99},{"id":2,"type":"card_refund","direction":"out","amount":-7.75}]}`), nil
			}
			return response(`{"code":0,"data":[{"id":1,"type":"card_refund","direction":"out","amount":-99}]}`), nil
		case r.Method == "DELETE":
			deletes++
			if r.Header.Get("Idempotency-Key") == "" {
				t.Fatal("missing delete idempotency")
			}
			reply := response(`{"code":0,"data":{"pending":true}}`)
			reply.StatusCode = 202
			return reply, nil
		case strings.HasSuffix(r.URL.Path, "/cards/42"):
			status := "ACTIVE"
			if deleted {
				status = "DELETED"
			}
			return response(fmt.Sprintf(`{"code":0,"data":{"id":42,"product_code":"TEST-BIN","status":"%s","available_amount":7.75}}`, status)), nil
		case strings.HasSuffix(r.URL.Path, "/products"):
			return response(`{"code":0,"data":[{"product_code":"TEST-BIN","min_amount":20,"open_fee":0.5,"recharge_fee":0}]}`), nil
		case strings.HasSuffix(r.URL.Path, "/balance"):
			return response(`{"code":0,"data":{"currency":"USD","spendable_balance":100}}`), nil
		case strings.HasSuffix(r.URL.Path, "/cards/open"):
			opens++
			return response(`{"code":0,"data":{"id":44}}`), nil
		case strings.HasSuffix(r.URL.Path, "/cards/44"):
			return response(`{"code":0,"data":{"id":44,"card_number":"4242424242420044","product_code":"TEST-BIN","status":"ACTIVE","available_amount":20}}`), nil
		default:
			t.Fatal("unexpected path", r.URL.Path)
			return nil, fmt.Errorf("unexpected")
		}
	})}
	run := func() {
		t.Helper()
		_, _ = e.Store.DB.Exec(ctx, "UPDATE card_replacements SET next_at=now()")
		worked, err := e.MaintainCards(ctx)
		if err != nil || !worked {
			t.Fatal(worked, err)
		}
	}
	run()
	if deletes != 0 || opens != 0 {
		t.Fatal("closed unsettled card")
	}
	settled = true
	run()
	run()
	if deletes != 1 || opens != 0 {
		t.Fatal("202 was repeated or treated as success", deletes, opens)
	}
	deleted = true
	run()
	if opens != 0 {
		t.Fatal("opened before refund proof")
	}
	refundReady = true
	run()
	if deletes != 1 || opens != 1 {
		t.Fatal(deletes, opens)
	}
	worked, err := e.MaintainCards(ctx)
	if err != nil || worked {
		t.Fatal("completed operation repeated", err)
	}
	var id, refund int64
	var status string
	_ = e.Store.DB.QueryRow(ctx, "SELECT card_id FROM card_pool_slots WHERE slot=1").Scan(&id)
	_ = e.Store.DB.QueryRow(ctx, "SELECT status,refund_minor FROM card_replacements").Scan(&status, &refund)
	if id != 44 || status != "completed" || refund != 775 {
		t.Fatal(id, status, refund)
	}
}

func TestMaintenanceUnknownOpeningNeverSpendsAgainAndCanReconcileOriginal(t *testing.T) {
	e, o, s := pairFixture(t)
	ctx := context.Background()
	raw, _ := json.Marshal(s)
	_, err := e.Store.DB.Exec(ctx, "INSERT INTO card_replacements(environment,old_card_id,rule_id,slot,order_id,config_cipher,status) VALUES('sandbox',42,'rule',1,$1,$2,'opening')", o.ID, e.Store.Vault.Seal(replacementPurpose("sandbox", 42), raw))
	if err != nil {
		t.Fatal(err)
	}
	old := client
	defer func() { client = old }()
	opens := 0
	client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/products"):
			return response(`{"code":0,"data":[{"product_code":"TEST-BIN","min_amount":20,"open_fee":0.5,"recharge_fee":0}]}`), nil
		case strings.HasSuffix(r.URL.Path, "/balance"):
			return response(`{"code":0,"data":{"currency":"USD","spendable_balance":100}}`), nil
		case strings.HasSuffix(r.URL.Path, "/cards/open"):
			opens++
			return nil, errors.New("accepted opening reply lost")
		case strings.HasSuffix(r.URL.Path, "/cards/44"):
			return response(`{"code":0,"data":{"id":44,"card_number":"4242424242420044","product_code":"TEST-BIN","status":"ACTIVE","available_amount":20}}`), nil
		default:
			t.Fatal(r.URL.Path)
			return nil, errors.New("unexpected")
		}
	})}
	for i := 0; i < 3; i++ {
		_, _ = e.Store.DB.Exec(ctx, "UPDATE card_replacements SET next_at=now()")
		if worked, err := e.MaintainCards(ctx); !worked || err != nil {
			t.Fatal(worked, err)
		}
	}
	if opens != 1 {
		t.Fatal("unknown opening resubmitted", opens)
	}
	// An operator records the original card after confirming it in the card platform.
	r := commerce.CardResolution{Reference: cardReference(o.ID, 101), Status: "succeeded", CardID: 44, Confirmation: "RESOLVE", Note: "Original provider operation confirmed with the returned card"}
	if err = e.ResolveCardOperation(ctx, "admin", r); err != nil {
		t.Fatal("original maintenance operation cannot recover", err)
	}
	if worked, err := e.MaintainCards(ctx); !worked || err != nil {
		t.Fatal(worked, err)
	}
	var id int64
	_ = e.Store.DB.QueryRow(ctx, "SELECT card_id FROM card_pool_slots WHERE slot=1").Scan(&id)
	if id != 44 || opens != 1 {
		t.Fatal("recovery did not adopt original card", id, opens)
	}
}

func TestCardSpecificRechargeMinimumStopsBeforeSpending(t *testing.T) {
	e, o, s := pairFixture(t)
	old := client
	defer func() { client = old }()
	client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || !strings.HasSuffix(r.URL.Path, "/cards/42") {
			t.Fatal("minimum violation sent to provider")
		}
		return response(`{"code":0,"data":{"id":42,"product_code":"TEST-BIN","status":"ACTIVE","available_amount":5.3,"min_recharge":20}}`), nil
	})}
	_, err := e.card(context.Background(), o, s, &Job{Stage: "card"}, func() error { return nil })
	var up *UpstreamError
	if !errors.As(err, &up) || up.Code != "card_funding_minimum" || !up.Definitive {
		t.Fatal(err)
	}
}

func TestConfirmedFailedFundingOnlyResumesAfterSeparateVerifiedFunding(t *testing.T) {
	e, o, s := pairFixture(t)
	ctx := context.Background()
	_, err := e.Store.DB.Exec(ctx, "INSERT INTO card_operations(reference,order_id,rule_id,operation,amount_minor,card_id,status,resolution_note) VALUES($1,$2,'rule','fund',1000,42,'failed','Minimum recharge rejected; provider confirms no charge')", cardReference(o.ID, 0), o.ID)
	if err != nil {
		t.Fatal(err)
	}
	old := client
	defer func() { client = old }()
	balance := 5.3
	client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || !strings.HasSuffix(r.URL.Path, "/cards/42") {
			t.Fatal("repeated failed funding")
		}
		return response(fmt.Sprintf(`{"code":0,"data":{"id":42,"product_code":"TEST-BIN","status":"ACTIVE","available_amount":%g,"min_recharge":20}}`, balance)), nil
	})}
	_, err = e.card(ctx, o, s, &Job{Stage: "funding", CardID: 42}, func() error { return nil })
	var up *UpstreamError
	if !errors.As(err, &up) || up.Code != "card_operation_not_charged" {
		t.Fatal(err)
	}
	balance = 25.3
	id, err := e.card(ctx, o, s, &Job{Stage: "funding", CardID: 42}, func() error { return nil })
	if err != nil || id != 42 {
		t.Fatal(id, err)
	}
	var state string
	_ = e.Store.DB.QueryRow(ctx, "SELECT status FROM card_operations").Scan(&state)
	if state != "failed" {
		t.Fatal("failed history rewritten")
	}
}
