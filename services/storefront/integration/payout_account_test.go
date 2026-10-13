package integration_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"gptibo/storefront/internal/security"
)

func TestPayoutAccountValidationIsolationAndConcurrentSave(t *testing.T) {
	owner, _, _, h, mail := identityFixture(t)
	admin := login(t, h, mail, "admin", "")
	first := object(t, request(h, "POST", "/api/admin/partners", map[string]any{"email": "payee-a@example.test"}, admin), 200)["id"].(string)
	object(t, request(h, "POST", "/api/admin/partners", map[string]any{"email": "payee-b@example.test"}, admin), 200)
	a := login(t, h, mail, "partner", "payee-a@example.test")
	b := login(t, h, mail, "partner", "payee-b@example.test")
	path := "/api/partner/payout-account"
	valid := map[string]any{"phone": "13800138000", "name": "测试收款人", "version": 0}
	object(t, request(h, "POST", path, valid, nil), 401)
	object(t, request(h, "POST", path, valid, admin), 401)
	for _, in := range []map[string]any{
		{"name": "测试", "version": 0}, {"phone": "13800138000", "version": 0},
		{"phone": "13800138000", "name": "  ", "version": 0},
		{"phone": "12345678901", "name": "测试", "version": 0},
		{"phone": "13800138000", "name": "测试\n姓名", "version": 0},
		{"phone": "13800138000", "name": "测试"},
		{"phone": "13800138000", "name": "测试", "version": -1},
	} {
		object(t, request(h, "POST", path, in, a), 400)
	}
	object(t, request(h, "POST", path, map[string]any{"phone": " 13800138000 ", "name": " 测试收款人 ", "version": 0}, a), 200)
	account := object(t, request(h, "GET", "/api/partner/dashboard", nil, a), 200)["payoutAccount"].(map[string]any)
	if account["phone"] != valid["phone"] || account["name"] != valid["name"] || account["version"] != float64(1) {
		t.Fatal("binding was not persisted or normalized")
	}
	other := object(t, request(h, "GET", "/api/partner/dashboard", nil, b), 200)["payoutAccount"].(map[string]any)
	if other["phone"] != "" || other["name"] != "" || other["version"] != float64(0) {
		t.Fatal("partner account leaked across sessions")
	}
	preview := object(t, request(h, "GET", "/api/admin/partners/"+first+"/preview", nil, admin), 200)
	if preview["payoutAccount"].(map[string]any)["phone"] != valid["phone"] {
		t.Fatal("read-only preview omitted binding")
	}
	object(t, request(h, "POST", path, valid, a), 409)
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for i := range 2 {
		wg.Go(func() {
			codes <- request(h, "POST", path, map[string]any{"phone": "13800138000", "name": fmt.Sprintf("测试%d", i), "version": 1}, a).Code
		})
	}
	wg.Wait()
	close(codes)
	seen := map[int]int{}
	for code := range codes {
		seen[code]++
	}
	if seen[200] != 1 || seen[409] != 1 {
		t.Fatalf("concurrent saves did not reserve one version: %v", seen)
	}
	// A failed audit must roll back the account update as well.
	execSQL(t, owner, `CREATE FUNCTION fail_payout_account_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='partner-payout-account' THEN RAISE EXCEPTION 'fixture audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_payout_account_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION fail_payout_account_audit()`)
	object(t, request(h, "POST", path, map[string]any{"phone": "13900139000", "name": "回滚测试", "version": 2}, a), 503)
	account = object(t, request(h, "GET", "/api/partner/dashboard", nil, a), 200)["payoutAccount"].(map[string]any)
	if account["version"] != float64(2) || account["phone"] != "13800138000" {
		t.Fatal("failed save changed binding")
	}
}

func TestPayoutUsesBoundAccountSnapshotAndIdempotentRetry(t *testing.T) {
	owner, _, _, h, mail := identityFixture(t)
	admin := login(t, h, mail, "admin", "")
	id := object(t, request(h, "POST", "/api/admin/partners", map[string]any{"email": "payee@example.test"}, admin), 200)["id"].(string)
	p := login(t, h, mail, "partner", "payee@example.test")
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents) VALUES('plus','chatgpt','Plus',10000)`)
	now := time.Now().UnixMilli()
	execSQL(t, owner, `INSERT INTO orders(id,owner_hash,idempotency_key,plan,plan_name,price_cents,cost_cents,partner_id,mode,payment_status,status,created_at,updated_at,last_event_id) VALUES('BOUND-PAYOUT','fixture','fixture','plus','Plus',10000,6000,$1,'live','paid','success',$2,$2,'fixture')`, id, now)
	key := security.ID()
	in := map[string]any{"key": key, "accountVersion": 1}
	object(t, request(h, "POST", "/api/partner/payouts", in, p), 409)
	object(t, request(h, "POST", "/api/partner/payout-account", map[string]any{"phone": "13800138000", "name": "原收款人", "version": 0}, p), 200)
	for _, version := range []any{nil, 0, 2} {
		object(t, request(h, "POST", "/api/partner/payouts", map[string]any{"key": key, "accountVersion": version}, p), 409)
	}
	// User-supplied recipient text cannot bypass the saved binding.
	object(t, request(h, "POST", "/api/partner/payouts", map[string]any{"key": key, "accountVersion": 1, "recipient": "other account"}, p), 400)
	object(t, request(h, "POST", "/api/partner/payouts", in, p), 201)
	object(t, request(h, "POST", "/api/partner/payout-account", map[string]any{"phone": "13900139000", "name": "新收款人", "version": 1}, p), 200)
	object(t, request(h, "POST", "/api/partner/payouts", in, p), 200)
	var recipient string
	var count int
	if e := owner.DB.QueryRow(context.Background(), `SELECT recipient FROM partner_payouts WHERE partner_id=$1 AND request_key=$2`, id, key).Scan(&recipient); e != nil || recipient != "13800138000 / 原收款人" {
		t.Fatal("later binding changed the original payout recipient", e)
	}
	if e := owner.DB.QueryRow(context.Background(), `SELECT count(*) FROM partner_payouts WHERE partner_id=$1`, id).Scan(&count); e != nil || count != 1 {
		t.Fatal("retry created a duplicate payout", e)
	}
	preview := object(t, request(h, "GET", "/api/admin/partners/"+id+"/preview", nil, admin), 200)
	if preview["payouts"].([]any)[0].(map[string]any)["recipient"] != recipient || preview["payoutAccount"].(map[string]any)["phone"] != "13900139000" {
		t.Fatal("preview mixed current binding and historical payout")
	}
}
