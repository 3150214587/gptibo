package integration_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"testing"

	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/httpapi"
	"gptibo/storefront/internal/orders"
	"gptibo/storefront/internal/security"
)

func TestOrderProtectionConcurrentPendingAndReplay(t *testing.T) {
	for _, shared := range []string{"email", "owner"} {
		t.Run(shared, func(t *testing.T) {
			owner, app, a, _, _ := identityFixture(t)
			execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents,enabled) VALUES('plus','chatgpt','Plus',100,true)`)
			s := orders.Service{Store: app, Auth: a, Testing: true, ProtectCreation: true}
			type result struct {
				access orders.Access
				in     orders.CreateInput
				order  orders.Order
				err    error
			}
			results := make(chan result, 8)
			var wg sync.WaitGroup
			for i := range 8 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					access := orders.Access{OwnerHash: fmt.Sprint("owner-", i)}
					email := fmt.Sprintf("buyer%d@gmail.com", i)
					if shared == "email" {
						email = "same@gmail.com"
					} else {
						access.OwnerHash = "same-owner"
					}
					in := orders.CreateInput{Plan: "plus", Email: email, Key: security.ID(), ExpectedPrice: 100}
					o, _, err := s.Create(context.Background(), access, in)
					results <- result{access, in, o, err}
				}()
			}
			wg.Wait()
			close(results)
			created := 0
			var saved result
			for r := range results {
				if r.err == nil {
					created++
					saved = r
				} else if fault.Status(r.err) != 429 {
					t.Fatal(r.err)
				}
			}
			if created != 3 {
				t.Fatalf("created %d, want 3", created)
			}
			got, fresh, err := s.Create(context.Background(), saved.access, saved.in)
			if err != nil || fresh || got.ID != saved.order.ID {
				t.Fatal("replay blocked or duplicated", err)
			}
			execSQL(t, owner, `UPDATE orders SET mode='live',payment_status='paid',status='session' WHERE id=$1`, saved.order.ID)
			saved.in.Key = security.ID()
			if _, fresh, err = s.Create(context.Background(), saved.access, saved.in); err != nil || !fresh {
				t.Fatal("payment did not free pending slot", err)
			}
		})
	}
}

func TestOrderProtectionIPAndAliasBudgets(t *testing.T) {
	for _, kind := range []string{"ip-minute", "ip-hour", "email-alias"} {
		t.Run(kind, func(t *testing.T) {
			owner, app, a, _, _ := identityFixture(t)
			execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents,enabled) VALUES('plus','chatgpt','Plus',100,true)`)
			s := orders.Service{Store: app, Auth: a, Testing: true, ProtectCreation: true}
			limit := 6
			if kind == "ip-hour" {
				limit = 30
			}
			if kind == "email-alias" {
				limit = 10
			}
			for i := 0; i <= limit; i++ {
				access := orders.Access{OwnerHash: fmt.Sprint("owner-", i), ClientIP: "192.0.2.1"}
				email := fmt.Sprintf("buyer%d@qq.com", i)
				if kind == "email-alias" {
					access.ClientIP = fmt.Sprintf("192.0.2.%d", i+1)
					email = fmt.Sprintf("first.last+%d@gmail.com", i)
					if i%2 == 1 {
						email = fmt.Sprintf("firstlast+%d@googlemail.com", i)
					}
				}
				if kind == "ip-hour" && i%6 == 0 {
					// Let minute budgets expire while preserving the durable hour budget.
					execSQL(t, owner, `UPDATE auth_limits SET reset_at=now()-interval '1 second' WHERE reset_at<now()+interval '2 minutes'`)
				}
				_, _, err := s.Create(context.Background(), access, orders.CreateInput{Plan: "plus", Email: email, Key: security.ID(), ExpectedPrice: 100})
				if i < limit && err != nil {
					t.Fatalf("order %d: %v", i, err)
				}
				if i == limit && fault.Status(err) != 429 {
					t.Fatalf("budget bypass: %v", err)
				}
				// Recreate the service to verify limits survive process-local state loss.
				s = orders.Service{Store: app, Auth: a, Testing: true, ProtectCreation: true}
			}
		})
	}
}

func TestOrderProtectionHTTPAndLegacyOrders(t *testing.T) {
	owner, app, a, _, _ := identityFixture(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents,enabled) VALUES('plus','chatgpt','Plus',100,true)`)
	legacy := httpapi.Router(app, "fixture", log, httpapi.Options{Origin: testOrigin, Auth: a, OrderTesting: true})
	protected := httpapi.Router(app, "fixture", log, httpapi.Options{Origin: testOrigin, Auth: a, OrderTesting: true, OrderProtection: true})
	buyer := &http.Cookie{Name: "__Host-sf_buyer", Value: security.Token()}
	in := map[string]any{"plan": "plus", "email": "old@example.invalid", "key": security.ID(), "expectedPrice": 100}
	old := object(t, request(legacy, "POST", "/api/orders", in, buyer), 201)["order"].(map[string]any)
	object(t, request(protected, "POST", "/api/orders", in, buyer), 200)
	object(t, request(protected, "GET", "/api/orders/"+old["id"].(string), nil, buyer), 200)
	in["key"] = security.ID()
	object(t, request(protected, "POST", "/api/orders", in, buyer), 400)
	in["email"] = "buyer@gmail.com.attacker.test"
	object(t, request(protected, "POST", "/api/orders", in, buyer), 400)
	var count int
	if err := owner.DB.QueryRow(context.Background(), `SELECT count(*) FROM orders`).Scan(&count); err != nil || count != 1 {
		t.Fatal("rejected request created order", count, err)
	}
	// Invalid emails also consume a bounded request budget, even without new orders.
	var limited bool
	for range 13 {
		w := request(protected, "POST", "/api/orders", in, buyer)
		if w.Code == 429 {
			limited = true
			if w.Header().Get("Retry-After") == "" {
				t.Fatal("missing retry hint")
			}
			break
		}
		if w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if !limited {
		t.Fatal("attempt limit not enforced")
	}
}
