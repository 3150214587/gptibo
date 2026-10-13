package integration_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/httpapi"
	"gptibo/storefront/internal/orders"
	"gptibo/storefront/internal/security"
	"gptibo/storefront/internal/store"
	"gptibo/storefront/internal/recharge"
)

type hierarchyFixture struct {
	owner                   *store.Store
	h                       http.Handler
	mail                    *fakeMail
	admin, parent, child    *http.Cookie
	parentID, childID, slug string
}

func hierarchySetup(t *testing.T, configure ...func(*httpapi.Options)) hierarchyFixture {
	t.Helper()
	owner, app, a, base, mail := identityFixture(t)
	admin := login(t, base, mail, "admin", "")
	opts := httpapi.Options{Origin: testOrigin, Auth: a, OrderTesting: true}
	for _, configure := range configure {
		configure(&opts)
	}
	h := httpapi.Router(app, "fixture", slog.New(slog.NewTextHandler(io.Discard, nil)), opts)
	object(t, request(h, "POST", "/api/admin/plans", map[string]any{"id": "plus", "productId": "chatgpt", "name": "Plus", "priceCents": 13000, "defaultCostCents": 10000, "enabled": true, "partnerEnabled": true, "note": "fixture"}, admin), 200)
	parentID := object(t, request(h, "POST", "/api/admin/partners", map[string]any{"email": "parent@example.test"}, admin), 200)["id"].(string)
	parent := login(t, h, mail, "partner", "parent@example.test")
	object(t, request(h, "POST", "/api/admin/partners/"+parentID+"/recruit-permission", map[string]any{"enabled": true, "version": 0}, admin), 200)
	child := object(t, request(h, "POST", "/api/partner/children", map[string]any{"email": "child@example.test"}, parent), 200)
	childID := child["id"].(string)
	object(t, request(h, "POST", "/api/partner/children/"+childID+"/prices/plus", map[string]any{"costCents": 11000, "enabled": true, "version": 0}, parent), 200)
	cc := login(t, h, mail, "partner", "child@example.test")
	return hierarchyFixture{owner, h, mail, admin, parent, cc, parentID, childID, child["slug"].(string)}
}
func (f hierarchyFixture) seed(t *testing.T, id, plan string, buyer *http.Cookie) {
	t.Helper()
	now := time.Now().UnixMilli()
	execSQL(t, f.owner, `INSERT INTO orders(id,owner_hash,idempotency_key,plan,plan_name,price_cents,cost_cents,partner_id,upstream_partner_id,upstream_cost_cents,customer_email,mode,payment_status,status,created_at,updated_at,last_event_id) VALUES($1,$2,$1,$3,'fixture',13000,11000,$4,$5,10000,'private-buyer@example.test','live','paid','success',$6,$6,'fixture')`, id, security.Hash(buyer.Value), plan, f.childID, f.parentID, now)
}
func (f hierarchyFixture) balance(t *testing.T, c *http.Cookie, available, debt float64) {
	t.Helper()
	st := object(t, request(f.h, "GET", "/api/partner/dashboard", nil, c), 200)["stats"].(map[string]any)
	if st["available"] != available || st["debt"] != debt {
		t.Fatalf("wrong balance: %+v", st)
	}
}
func (f hierarchyFixture) bind(t *testing.T) {
	t.Helper()
	for _, c := range []*http.Cookie{f.parent, f.child} {
		object(t, request(f.h, "POST", "/api/partner/payout-account", map[string]any{"phone": "13800138000", "name": "fixture", "version": 0}, c), 200)
	}
}
func (f hierarchyFixture) payout(t *testing.T, c *http.Cookie) string {
	t.Helper()
	in := map[string]any{"key": security.ID(), "accountVersion": 1}
	object(t, request(f.h, "POST", "/api/partner/payouts", in, c), 201)
	object(t, request(f.h, "POST", "/api/partner/payouts", in, c), 200)
	var id string
	if e := f.owner.DB.QueryRow(context.Background(), `SELECT id FROM partner_payouts WHERE request_key=$1`, in["key"]).Scan(&id); e != nil {
		t.Fatal(e)
	}
	return id
}
func (f hierarchyFixture) handle(t *testing.T, id, action string, version int) {
	t.Helper()
	object(t, request(f.h, "POST", "/api/admin/partner-payouts/"+id, map[string]any{"action": action, "version": version, "note": "fixture", "reference": "fixture-transfer"}, f.admin), 200)
}
func (f hierarchyFixture) refund(t *testing.T, id string, buyer *http.Cookie, amount, version int) string {
	t.Helper()
	key := security.ID()
	object(t, request(f.h, "POST", "/api/orders/"+id+"/refunds", map[string]any{"key": key, "reason": "fixture", "version": version}, buyer), 201)
	var rid string
	if e := f.owner.DB.QueryRow(context.Background(), `SELECT id FROM refund_cases WHERE request_key=$1`, key).Scan(&rid); e != nil {
		t.Fatal(e)
	}
	object(t, request(f.h, "POST", "/api/admin/refunds/"+rid, map[string]any{"action": "approve", "version": 0, "amountCents": amount, "note": "fixture"}, f.admin), 200)
	return rid
}
func (f hierarchyFixture) complete(t *testing.T, rid string) {
	t.Helper()
	in := map[string]any{"action": "complete", "version": 1, "note": "fixture", "reference": "fixture-refund"}
	object(t, request(f.h, "POST", "/api/admin/refunds/"+rid, in, f.admin), 200)
	object(t, request(f.h, "POST", "/api/admin/refunds/"+rid, in, f.admin), 409)
}

func TestHierarchyPermissionsEmailIsolationAndAudit(t *testing.T) {
	f := hierarchySetup(t)
	h := f.h
	path := "/api/partner/children/" + f.childID
	object(t, request(h, "GET", path, nil, nil), 401)
	object(t, request(h, "POST", "/api/partner/children", map[string]any{"email": "third@example.test"}, f.child), 403)
	object(t, request(h, "POST", "/api/admin/partners/"+f.childID+"/recruit-permission", map[string]any{"enabled": true, "version": 0}, f.admin), 409)
	object(t, request(h, "POST", "/api/admin/partners/"+f.parentID+"/recruit-permission", map[string]any{"enabled": true, "version": 1}, f.parent), 401)
	otherID := object(t, request(h, "POST", "/api/admin/partners", map[string]any{"email": "other@example.test"}, f.admin), 200)["id"].(string)
	other := login(t, h, f.mail, "partner", "other@example.test")
	object(t, request(h, "POST", "/api/partner/children", map[string]any{"email": "unauthorized@example.test"}, other), 403)
	for _, route := range []string{path, path + "/orders", "/api/partner/children/" + otherID} {
		object(t, request(h, "GET", route, nil, other), 404)
	}
	object(t, request(h, "POST", path, map[string]any{"email": "hijack@example.test", "active": true, "version": 0}, other), 404)
	object(t, request(h, "POST", path, map[string]any{"email": "child-new@example.test", "active": true, "version": 0}, f.parent), 200)
	object(t, request(h, "GET", "/api/partner/dashboard", nil, f.child), 401)
	f.child = login(t, h, f.mail, "partner", "child-new@example.test")
	object(t, request(h, "POST", path, map[string]any{"email": "child@example.test", "active": true, "version": 0}, f.parent), 409)
	object(t, request(h, "POST", path, map[string]any{"email": "child-new@example.test", "active": true, "version": 1, "name": "unapproved-store-edit"}, f.parent), 400)
	object(t, request(h, "POST", path+"/prices/plus", map[string]any{"costCents": 9999, "enabled": true, "version": 1}, f.parent), 400)
	object(t, request(h, "POST", path+"/prices/plus", map[string]any{"costCents": 13001, "enabled": true, "version": 1}, f.parent), 400)
	object(t, request(h, "POST", "/api/partner/prices/plus", map[string]any{"priceCents": 10999, "version": 1}, f.child), 400)
	object(t, request(h, "POST", "/api/admin/partners/"+f.parentID+"/prices/plus", map[string]any{"costCents": 11001, "version": 0}, f.admin), 409)
	// Permission revocation only prevents new children; existing accounts remain manageable.
	object(t, request(h, "POST", "/api/admin/partners/"+f.parentID+"/recruit-permission", map[string]any{"enabled": false, "version": 1}, f.admin), 200)
	object(t, request(h, "POST", "/api/partner/children", map[string]any{"email": "closed@example.test"}, f.parent), 403)
	object(t, request(h, "GET", path, nil, f.parent), 200)
	object(t, request(h, "POST", path, map[string]any{"email": "child-new@example.test", "active": false, "version": 1}, f.parent), 200)
	object(t, request(h, "GET", "/api/partner/dashboard", nil, f.child), 401)
	object(t, request(h, "POST", path, map[string]any{"email": "child-new@example.test", "active": true, "version": 2}, f.parent), 200)
	// Failed audit rolls back mailbox changes and session version changes together.
	execSQL(t, f.owner, `CREATE FUNCTION fail_child_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='child-update' THEN RAISE EXCEPTION 'fixture'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_child_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION fail_child_audit()`)
	object(t, request(h, "POST", path, map[string]any{"email": "rollback@example.test", "active": true, "version": 3}, f.parent), 503)
	detail := object(t, request(h, "GET", path, nil, f.parent), 200)
	if detail["partner"].(map[string]any)["email"] != "child-new@example.test" {
		t.Fatal("audit failure changed email")
	}
}

func TestHierarchyOrderSnapshotsVisibilityAndParentSuspension(t *testing.T) {
	f := hierarchySetup(t)
	buyer := &http.Cookie{Name: "__Host-sf_buyer", Value: security.Token()}
	in := map[string]any{"key": security.ID(), "plan": "plus", "email": "private-buyer@example.test", "partner": f.slug, "expectedPrice": 13000}
	order := object(t, request(f.h, "POST", "/api/orders", in, buyer), 201)["order"].(map[string]any)
	id := order["id"].(string)
	var upstream string
	var cost, base int64
	if e := f.owner.DB.QueryRow(context.Background(), `SELECT upstream_partner_id,cost_cents,upstream_cost_cents FROM orders WHERE id=$1`, id).Scan(&upstream, &cost, &base); e != nil || upstream != f.parentID || cost != 11000 || base != 10000 {
		t.Fatal("missing snapshot", e)
	}
	path := "/api/partner/children/" + f.childID
	object(t, request(f.h, "POST", path+"/prices/plus", map[string]any{"costCents": 12000, "enabled": false, "version": 1}, f.parent), 200)
	object(t, request(f.h, "POST", "/api/orders", in, buyer), 200)
	in["key"] = security.ID()
	object(t, request(f.h, "POST", "/api/orders", in, buyer), 409)
	if e := f.owner.DB.QueryRow(context.Background(), `SELECT cost_cents FROM orders WHERE id=$1`, id).Scan(&cost); e != nil || cost != 11000 {
		t.Fatal("repriced history", e)
	}
	list := request(f.h, "GET", path+"/orders", nil, f.parent)
	object(t, list, 200)
	for _, secret := range []string{"private-buyer@example.test", "customer_email", "owner_hash", "session", "recipient", "payoutAccount"} {
		if strings.Contains(list.Body.String(), secret) {
			t.Fatalf("leaked %s", secret)
		}
	}
	object(t, request(f.h, "GET", "/api/partner/orders/"+id, nil, f.parent), 404)
	childDashboard := request(f.h, "GET", "/api/partner/dashboard", nil, f.child)
	object(t, childDashboard, 200)
	if strings.Contains(childDashboard.Body.String(), "upstream_cost_cents") || strings.Contains(childDashboard.Body.String(), "minimum_cost_cents") {
		t.Fatal("upstream costs leaked")
	}
	object(t, request(f.h, "POST", path+"/prices/plus", map[string]any{"costCents": 12000, "enabled": true, "version": 2}, f.parent), 200)
	object(t, request(f.h, "POST", "/api/admin/partners/"+f.parentID, map[string]any{"name": "parent", "email": "parent@example.test", "active": false, "version": 1}, f.admin), 200)
	object(t, request(f.h, "POST", "/api/orders", in, buyer), 409)
	object(t, request(f.h, "GET", "/api/orders/"+id, nil, buyer), 200)
	object(t, request(f.h, "GET", "/api/partner/dashboard", nil, f.child), 200)
	object(t, request(f.h, "POST", "/api/admin/partners/"+f.parentID, map[string]any{"name": "parent", "email": "parent@example.test", "active": true, "version": 2}, f.admin), 200)
	object(t, request(f.h, "POST", "/api/orders", in, buyer), 201)
}

func TestHierarchyIndependentPayoutsAndProportionalRefunds(t *testing.T) {
	f := hierarchySetup(t)
	f.bind(t)
	buyer := &http.Cookie{Name: "__Host-sf_buyer", Value: security.Token()}
	f.seed(t, "HIERARCHY-PAID", "plus", buyer)
	f.balance(t, f.parent, 1000, 0)
	f.balance(t, f.child, 2000, 0)
	// Concurrent requests from both beneficiaries must reserve separate earnings.
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for _, cookie := range []*http.Cookie{f.parent, f.child} {
		wg.Go(func() {
			codes <- request(f.h, "POST", "/api/partner/payouts", map[string]any{"key": security.ID(), "accountVersion": 1}, cookie).Code
		})
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != 201 {
			t.Fatalf("concurrent payout: %d", code)
		}
	}
	var parentPayout, childPayout string
	if e := f.owner.DB.QueryRow(context.Background(), `SELECT upstream_payout_id,payout_id FROM orders WHERE id='HIERARCHY-PAID'`).Scan(&parentPayout, &childPayout); e != nil || parentPayout == childPayout {
		t.Fatal("payout ownership conflict", e)
	}
	for _, pid := range []string{parentPayout, childPayout} {
		f.handle(t, pid, "approve", 0)
		f.handle(t, pid, "paid", 1)
	}
	f.balance(t, f.parent, 0, 0)
	f.balance(t, f.child, 0, 0)
	for i, amount := range []int{3333, 3333, 6334} {
		rid := f.refund(t, "HIERARCHY-PAID", buyer, amount, i*2)
		f.complete(t, rid)
	}
	f.balance(t, f.parent, 0, 1000)
	f.balance(t, f.child, 0, 2000)
	var total int64
	if e := f.owner.DB.QueryRow(context.Background(), `SELECT sum(amount_cents)::bigint FROM partner_debits`).Scan(&total); e != nil || total != 3000 {
		t.Fatal("refund duplicated commission", e)
	}
	// Outstanding refunds are deducted from each beneficiary's own subsequent payout.
	for _, pid := range []string{f.parentID, f.childID} {
		object(t, request(f.h, "POST", "/api/admin/partners/"+pid+"/payout-cooldown/reset", map[string]any{"key": security.ID(), "note": "fixture"}, f.admin), 200)
	}
	f.seed(t, "NEXT-A", "plus", buyer)
	f.seed(t, "NEXT-B", "plus", buyer)
	f.balance(t, f.parent, 1000, 1000)
	f.balance(t, f.child, 2000, 2000)
	for _, c := range []*http.Cookie{f.parent, f.child} {
		pid := f.payout(t, c)
		f.handle(t, pid, "approve", 0)
		f.handle(t, pid, "paid", 1)
	}
	f.balance(t, f.parent, 0, 0)
	f.balance(t, f.child, 0, 0)
}

func TestHierarchyRefundRejectsBothPendingPayoutsAndWaitsForReviewed(t *testing.T) {
	f := hierarchySetup(t)
	f.bind(t)
	buyer := &http.Cookie{Name: "__Host-sf_buyer", Value: security.Token()}
	f.seed(t, "HIERARCHY-PENDING", "plus", buyer)
	parentPayout := f.payout(t, f.parent)
	childPayout := f.payout(t, f.child)
	f.handle(t, parentPayout, "approve", 0)
	rid := f.refund(t, "HIERARCHY-PENDING", buyer, 6500, 0)
	var status string
	if e := f.owner.DB.QueryRow(context.Background(), `SELECT status FROM partner_payouts WHERE id=$1`, childPayout).Scan(&status); e != nil || status != "rejected" {
		t.Fatal("child reservation not rejected", e)
	}
	object(t, request(f.h, "POST", "/api/admin/refunds/"+rid, map[string]any{"action": "complete", "version": 1, "note": "fixture", "reference": "fixture"}, f.admin), 409)
	f.handle(t, parentPayout, "paid", 1)
	f.complete(t, rid)
	f.balance(t, f.parent, 0, 500)
	f.balance(t, f.child, 1000, 0)
}

func TestHierarchySpecialRefundAndSupplierCap(t *testing.T) {
	f := hierarchySetup(t)
	f.bind(t)
	execSQL(t, f.owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents) VALUES('duolingo-12m','duolingo','Duolingo',13000)`)
	buyer := &http.Cookie{Name: "__Host-sf_buyer", Value: security.Token()}
	f.seed(t, "HIERARCHY-WARRANTY", "duolingo-12m", buyer)
	for _, c := range []*http.Cookie{f.parent, f.child} {
		pid := f.payout(t, c)
		f.handle(t, pid, "approve", 0)
		f.handle(t, pid, "paid", 1)
	}
	rid := f.refund(t, "HIERARCHY-WARRANTY", buyer, 1, 0)
	f.complete(t, rid)
	f.balance(t, f.parent, 0, 1000)
	f.balance(t, f.child, 0, 2000)
	detail := object(t, request(f.h, "GET", "/api/admin/refunds/"+rid, nil, f.admin), 200)
	if len(detail["partnerDebits"].([]any)) != 2 {
		t.Fatal("missing one beneficiary in admin refund")
	}
	f.seed(t, "HIERARCHY-CAP", "plus", buyer)
	execSQL(t, f.owner, `INSERT INTO recharge_orders(order_id,account,product_code,collection_mode,create_body,updated_at,merchant_margin_cents,financial_hold) VALUES('HIERARCHY-CAP','fixture','fixture','platform_collect','{}',1,1001,0)`)
	var own, up int64
	if e := f.owner.DB.QueryRow(context.Background(), `SELECT partner_order_profit(id,partner_id),partner_order_profit(id,upstream_partner_id) FROM orders WHERE id='HIERARCHY-CAP'`).Scan(&own, &up); e != nil || own+up != 1001 || own != 667 || up != 334 {
		t.Fatal(fmt.Sprintf("cap allocated %d/%d: %v", own, up, e))
	}
}

func TestHierarchyRelationshipChangesPreserveHistoricalBeneficiaries(t *testing.T) {
	f := hierarchySetup(t)
	buyer := &http.Cookie{Name: "__Host-sf_buyer", Value: security.Token()}
	f.seed(t, "OLD-RELATION", "plus", buyer)
	otherID := object(t, request(f.h, "POST", "/api/admin/partners", map[string]any{"email": "newparent@example.test"}, f.admin), 200)["id"].(string)
	object(t, request(f.h, "POST", "/api/admin/partners/"+otherID+"/recruit-permission", map[string]any{"enabled": true, "version": 0}, f.admin), 200)
	other := login(t, f.h, f.mail, "partner", "newparent@example.test")
	path := "/api/admin/partners/" + f.childID + "/parent"
	object(t, request(f.h, "POST", path, map[string]any{"parentId": otherID, "version": 0}, f.parent), 401)
	object(t, request(f.h, "POST", path, map[string]any{"parentId": f.childID, "version": 0}, f.admin), 400)
	object(t, request(f.h, "POST", "/api/admin/partners/"+f.parentID+"/parent", map[string]any{"parentId": otherID, "version": 1}, f.admin), 409)
	object(t, request(f.h, "POST", path, map[string]any{"parentId": otherID, "version": 0}, f.admin), 200)
	object(t, request(f.h, "GET", "/api/partner/dashboard", nil, f.child), 401)
	object(t, request(f.h, "GET", "/api/partner/children/"+f.childID, nil, f.parent), 404)
	object(t, request(f.h, "GET", "/api/partner/children/"+f.childID, nil, other), 200)
	f.balance(t, f.parent, 1000, 0)
	f.balance(t, other, 0, 0)
	historical := object(t, request(f.h, "GET", "/api/partner/upstream-orders", nil, f.parent), 200)
	if historical["total"] != float64(1) {
		t.Fatal("lost historical upstream earnings")
	}
	var original string
	if e := f.owner.DB.QueryRow(context.Background(), `SELECT upstream_partner_id FROM orders WHERE id='OLD-RELATION'`).Scan(&original); e != nil || original != f.parentID {
		t.Fatal("changed order attribution", e)
	}
	execSQL(t, f.owner, `UPDATE partner_prices SET enabled=false WHERE partner_id=$1`, f.childID)
	object(t, request(f.h, "POST", path, map[string]any{"parentId": "", "version": 1}, f.admin), 200)
	data := object(t, request(f.h, "GET", "/api/admin/partners/"+f.childID, nil, f.admin), 200)
	p := data["partner"].(map[string]any)
	var available bool
	if e := f.owner.DB.QueryRow(context.Background(), `SELECT enabled FROM partner_prices WHERE partner_id=$1 AND plan='plus'`, f.childID).Scan(&available); e != nil || !available {
		t.Fatal("promotion retained old parent restrictions", e)
	}
	if p["parent_id"] != nil || p["can_recruit"] != false {
		t.Fatal("promotion silently granted recruiting")
	}
}

func TestHierarchySupplierQuoteUsesPlatformWholesalePrice(t *testing.T) {
	f := hierarchySetup(t)
	client, _ := newRechargeFake(t) // Supplier requires 60 yuan; child wholesale remains 110.
	supplier := &recharge.Service{CredentialSecret: strings.Repeat("ab", 32), Store: f.owner, Client: client, Checkout: true, Products: map[string]string{"plus": "plus"}}
	service := orders.Service{Store: f.owner, LivePlan: func(string) bool { return true }, LiveSnapshot: supplier.Snapshot}
	execSQL(t, f.owner, `UPDATE partner_prices SET cost_cents=5000 WHERE partner_id=$1`, f.parentID)
	in := orders.CreateInput{Key: security.ID(), Plan: "plus", Email: "buyer@example.test", Partner: f.slug, ExpectedPrice: 13000}
	_, _, e := service.Create(context.Background(), orders.Access{OwnerHash: "fixture-hierarchy"}, in)
	if fault.Status(e) != 409 {
		t.Fatal("child markup hid a loss-making platform quote", e)
	}
	var count int64
	if e = f.owner.DB.QueryRow(context.Background(), `SELECT count(*) FROM orders WHERE idempotency_key=$1`, in.Key).Scan(&count); e != nil || count != 0 {
		t.Fatal("rejected quote left an order", e)
	}
	execSQL(t, f.owner, `UPDATE partner_prices SET cost_cents=7000 WHERE partner_id=$1`, f.parentID)
	o, created, e := service.Create(context.Background(), orders.Access{OwnerHash: "fixture-hierarchy"}, in)
	if e != nil || !created {
		t.Fatal("valid wholesale quote refused", e)
	}
	if o.Cost == nil || *o.Cost != 11000 {
		t.Fatal("supplier validation overwrote child wholesale snapshot")
	}
}
