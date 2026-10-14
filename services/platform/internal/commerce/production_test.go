package commerce

import (
	"errors"
	"testing"
)

func TestOrderReadinessCannotFreezeForDisabledSpecificRuleOrDeadWorker(t *testing.T) {
	for _, tc := range []struct{ name, sql, code string }{
		{"disabled-specific", "INSERT INTO card_rules(id,business,product_code,card_product,enabled,card_id,funding_minor,max_order_minor,daily_limit_minor) VALUES('specific','gpt','gpt-plus','OTHER',false,42,2000,1000,2000)", "card_rule_disabled"},
		{"worker-down", "DELETE FROM worker_heartbeats", "worker_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, ctx := setup(t)
			if _, err := svc.Store.DB.Exec(ctx, tc.sql); err != nil {
				t.Fatal(err)
			}
			_, _, err := svc.Create(ctx, "channel", "readiness-request", request("readiness"))
			var known *Error
			if !errors.As(err, &known) || known.Code != tc.code {
				t.Fatalf("unexpected readiness result: %v", err)
			}
			var available, frozen int64
			if err = svc.Store.DB.QueryRow(ctx, "SELECT available_minor,frozen_minor FROM wallets WHERE user_id='channel'").Scan(&available, &frozen); err != nil || available != 10000 || frozen != 0 {
				t.Fatal("unready order froze funds", err)
			}
		})
	}
}
func TestConfirmedPriceAndOriginalRecoveryAfterPause(t *testing.T) {
	svc, ctx := setup(t)
	r := request("confirmed")
	expected := int64(999)
	r.ExpectedSupplyMinor = &expected
	if _, _, err := svc.Create(ctx, "channel", "confirmation-request", r); err == nil {
		t.Fatal("changed price accepted")
	}
	expected = 1000
	original, _, err := svc.Create(ctx, "channel", "confirmation-request", r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Store.DB.Exec(ctx, "DELETE FROM worker_heartbeats; UPDATE products SET enabled=false; UPDATE card_rules SET enabled=false"); err != nil {
		t.Fatal(err)
	}
	recovered, created, err := svc.Create(ctx, "channel", "confirmation-request", r)
	if err != nil || created || recovered.ID != original.ID {
		t.Fatal("pause prevented recovery of original", err)
	}
}
func TestSecondLedgerSettlementIsRejectedEvenWithDifferentReference(t *testing.T) {
	svc, ctx := setup(t)
	o, _, err := svc.Create(ctx, "channel", "double-settlement", request("double"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Store.DB.Exec(ctx, "UPDATE orders SET lease_token='lease' WHERE id=$1", o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.Finish(ctx, o.ID, "lease", "failed", "preflight", "rejected", ""); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Store.DB.Exec(ctx, "INSERT INTO ledger(id,user_id,order_id,kind,available_delta,frozen_delta,reference,note,actor_id) VALUES('double','channel',$1,'release',1000,-1000,'different-reference','test','admin')", o.ID); err == nil {
		t.Fatal("second release bypassed unique order settlement")
	}
}
