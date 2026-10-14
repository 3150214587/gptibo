package providers

import (
	"context"
	"gptibo/platform/internal/commerce"
	"gptibo/platform/migrations"
	"net/http"
	"strings"
	"testing"
)

func TestCaptchaSuspendsOrderAndNextOrderReusesCard(t *testing.T) {
	e, o, s, j := handoffFixture(t)
	ctx := context.Background()
	s.Rule.Business = "x"
	s.Rule.BackupCardID = 43
	s.Rule.UseProviderLimits = true
	s.Rule.ReplaceOnDecline = true
	_, err := e.Store.DB.Exec(ctx, `UPDATE orders SET status='queued' WHERE id=$1`, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.Store.DB.Exec(ctx, `INSERT INTO ledger(id,user_id,order_id,kind,available_delta,frozen_delta,reference,note,actor_id) VALUES('reserve','user',$1,'reserve',-1700,1700,'reserve','test','test')`, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	account := e.Store.Vault.Seal("x-account:shared", []byte(`{"auth_token":"synthetic","ct0":"synthetic"}`))
	_, err = e.Store.DB.Exec(ctx, `INSERT INTO x_accounts(id,name,payload_cipher,daily_limit) VALUES('shared','test',$1,100)`, account)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.Store.DB.Exec(ctx, `INSERT INTO card_pool_slots(rule_id,environment,slot,card_product,card_id,last_four) VALUES('rule','production',1,'TEST-BIN',42,'0042'),('rule','production',2,'TEST-BIN',43,'0043');
 INSERT INTO resource_locks(resource,order_id) VALUES('x-account:shared','handoff-order'),('card-rule:rule','handoff-order'),('card:production:42','handoff-order'),('x-recipient:tester','handoff-order'),('x-recipient-name:tester','handoff-order')`)
	if err != nil {
		t.Fatal(err)
	}
	old := client
	defer func() { client = old }()
	paid := false
	client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" {
			t.Fatal("unexpected payment or recharge")
		}
		if strings.HasSuffix(r.URL.Path, "/poll") {
			if paid {
				return response(`{"session_id":"cs_live_test","livemode":true,"is_sandbox_merchant":false,"mode":"payment","success_url":"https://x.com/tester/gift-premium/success","state":"succeeded","payment_object_status":"succeeded"}`), nil
			}
			return response(`{"session_id":"cs_live_test","livemode":true,"is_sandbox_merchant":false,"mode":"payment","success_url":"https://x.com/tester/gift-premium/success","state":"active","payment_object_status":"requires_action"}`), nil
		}
		if r.URL.Path != "/openapi/v1/cards/42" {
			t.Fatal("released primary card not reused", r.URL.Path)
		}
		return response(`{"code":0,"data":{"id":42,"product_code":"TEST-BIN","status":"ACTIVE","available_amount":20}}`), nil
	})}
	result, err := e.x(ctx, o, s, &j, func() error { return nil })
	if err != nil || result.Failure != "payment_action_required" {
		t.Fatal(result, err)
	}
	var alerts int
	if err = e.Store.DB.QueryRow(ctx, "SELECT count(*) FROM manual_alerts WHERE order_id=$1 AND reason='payment_action_required'", o.ID).Scan(&alerts); err != nil || alerts != 1 {
		t.Fatal("captcha was isolated without its durable reminder", alerts, err)
	}
	var retained int
	var frozen int64
	if err = e.Store.DB.QueryRow(ctx, "SELECT count(*) FROM resource_locks WHERE order_id=$1", o.ID).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if retained != 2 {
		t.Fatal("must retain only recipient protection", retained)
	}
	_ = e.Store.DB.QueryRow(ctx, "SELECT frozen_minor FROM wallets WHERE user_id='user'").Scan(&frozen)
	if frozen != 1700 {
		t.Fatal("manual funds released", frozen)
	}
	next := commerce.Order{ID: "test-order", UserID: "user"}
	nextJob := Job{Stage: "card"}
	if err = e.assignAccount(ctx, next, &nextJob); err != nil || nextJob.AccountID != "shared" {
		t.Fatal("manual payment blocked account", err)
	}
	card, err := e.card(ctx, next, s, &nextJob, func() error { return nil })
	if err != nil || card != 42 || nextJob.CardAttempt != 0 {
		t.Fatal("released primary was not selected", card, err)
	}
	if err = e.resource(ctx, next.ID, "x-recipient:tester"); err == nil {
		t.Fatal("duplicate recipient payment allowed")
	}
	// Manual polling cannot release the new order's account/rule locks.
	if err = e.isolatePayment(ctx, o.ID, j.CardID); err != nil {
		t.Fatal(err)
	}
	var owner string
	_ = e.Store.DB.QueryRow(ctx, "SELECT order_id FROM resource_locks WHERE resource='x-account:shared'").Scan(&owner)
	if owner != next.ID {
		t.Fatal("other order lock removed")
	}
	_ = e.Store.DB.QueryRow(ctx, "SELECT order_id FROM resource_locks WHERE resource='card:production:42'").Scan(&owner)
	if owner != next.ID {
		t.Fatal("manual polling removed or reclaimed another order's card lock")
	}
	paid = true
	result, err = e.x(ctx, o, s, &j, func() error { return nil })
	if err != nil || result.Status != "succeeded" {
		t.Fatal(result, err)
	}
	svc := commerce.Service{Store: e.Store}
	for i := 0; i < 2; i++ {
		if err = svc.Finish(ctx, o.ID, "", result.Status, result.Stage, result.Failure, "test-receipt"); err != nil {
			t.Fatal(err)
		}
	}
	var settlements int
	_ = e.Store.DB.QueryRow(ctx, "SELECT count(*) FROM ledger WHERE order_id=$1 AND kind='consume'", o.ID).Scan(&settlements)
	if settlements != 1 {
		t.Fatal("manual settlement not once", settlements)
	}
}

func TestBothManualCardsReleaseAndMigrationPreservesActiveOrder(t *testing.T) {
	e, o, s, _ := handoffFixture(t)
	ctx := context.Background()
	s.Rule.BackupCardID = 43
	_, err := e.Store.DB.Exec(ctx, `INSERT INTO orders(id,user_id,merchant_order_no,idempotency_key,request_hash,product_code,business,supply_minor,status,stage,config_cipher)
 VALUES('second-manual','user','second-manual','second-manual','hash','x-3m','x',1700,'unknown','submitted','unused');
 INSERT INTO card_pool_slots(rule_id,environment,slot,card_product,card_id,last_four) VALUES('rule','production',1,'TEST-BIN',42,'0042'),('rule','production',2,'TEST-BIN',43,'0043');
 INSERT INTO resource_locks(resource,order_id) VALUES('card:production:42','handoff-order'),('card:production:43','second-manual'),('x-recipient:tester','handoff-order'),('x-recipient:second','second-manual'),('card:production:44','test-order');
 UPDATE orders SET manual_payment_at=now() WHERE id IN ('handoff-order','second-manual')`)
	if err != nil {
		t.Fatal(err)
	}
	// Existing suspended orders must also be unblocked on upgrade, including
	// expired sessions which no longer return a requires_action response.
	migration, err := migrations.Files.ReadFile("010_manual_payment_release_cards.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Store.DB.Exec(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{o.ID, "second-manual"} {
		if err = e.isolatePayment(ctx, id, 42); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err = e.Store.DB.QueryRow(ctx, "SELECT count(*) FROM resource_locks").Scan(&count); err != nil || count != 3 {
		t.Fatal("migration removed recipient or active payment protection", count, err)
	}
	j := Job{Stage: "card"}
	if err = e.selectPairCard(ctx, commerce.Order{ID: "test-order"}, s, &j, func() error { return nil }); err != nil || j.CardID != 42 {
		t.Fatal("both manual orders still blocked card selection", j.CardID, err)
	}
	order, err := commerce.ScanOrder(e.Store.DB.QueryRow(ctx, "SELECT "+commerce.OrderColumns+" FROM orders WHERE id=$1", o.ID))
	if err != nil || !order.ManualPayment {
		t.Fatal("order details lost manual flag", err)
	}
}

func TestActiveAutomaticOrdersStillOwnTheirCards(t *testing.T) {
	e, o, s := pairFixture(t)
	ctx := context.Background()
	_, err := e.Store.DB.Exec(ctx, `INSERT INTO resource_locks(resource,order_id) VALUES('card:sandbox:42',$1),('card:sandbox:43',$1)`, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	j := Job{Stage: "card"}
	err = e.selectPairCard(ctx, commerce.Order{ID: "another"}, s, &j, func() error { return nil })
	if err == nil || j.CardID != 0 || len(j.PoolCards) != 0 {
		t.Fatal("reserved cards reused", err, j.CardID)
	}
}
