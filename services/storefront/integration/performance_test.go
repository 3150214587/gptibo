package integration_test

import (
	"context"
	"github.com/jackc/pgx/v5"
	"gptibo/storefront/internal/orders"
	"gptibo/storefront/internal/worker"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

type countingQuery struct {
	base  orders.Query
	calls int
}

func (q *countingQuery) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	q.calls++
	return q.base.Query(ctx, sql, args...)
}
func (q *countingQuery) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	q.calls++
	return q.base.QueryRow(ctx, sql, args...)
}

func TestOrderReadsBatchRelationsAndNeverWaitForSuppliers(t *testing.T) {
	owner, app := fixture(t, true)
	now := time.Now().UnixMilli()
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents) VALUES('duolingo-12m','duolingo','Duolingo',1000)`)
	execSQL(t, owner, `INSERT INTO orders(id,owner_hash,idempotency_key,plan,plan_name,price_cents,mode,payment_status,status,created_at,updated_at,last_event_id) SELECT 'PERF-'||n,'buyer','key-'||n,'duolingo-12m','Duolingo',1000,'live','paid','processing',$1,$1,'fixture' FROM generate_series(1,100) n`, now)
	service := orders.Service{Store: app, Sync: func(context.Context, string, bool) error { t.Error("read called supplier"); return nil }}
	ctx := context.Background()
	list, e := orders.List(ctx, app.DB, `SELECT to_jsonb(o) FROM orders o ORDER BY id`)
	if e != nil {
		t.Fatal(e)
	}
	for _, n := range []int{1, 20, 100} {
		q := &countingQuery{base: app.DB}
		start := time.Now()
		views, e := service.Views(ctx, q, list[:n], false)
		if e != nil || len(views) != n {
			t.Fatal(e)
		}
		if q.calls > 8 {
			t.Fatalf("N+1 queries: %d for %d orders", q.calls, n)
		}
		t.Logf("%d order views: %d queries, %s", n, q.calls, time.Since(start))
	}
	access := orders.Access{OwnerHash: "buyer"}
	start := time.Now()
	result, e := service.CustomerList(ctx, access)
	if e != nil {
		t.Fatal(e)
	}
	if len(result["orders"].([]map[string]any)) != 100 {
		t.Fatal("missing orders")
	}
	t.Logf("100-order customer list: %s", time.Since(start))
	if _, e = service.Detail(ctx, "PERF-1", &access); e != nil {
		t.Fatal(e)
	}
	other := orders.Access{OwnerHash: "another-buyer"}
	if _, e = service.Detail(ctx, "PERF-1", &other); e == nil {
		t.Fatal("order ownership bypass")
	}
}

func TestPaymentWakeIsCommittedAndSurvivesWorkerRestart(t *testing.T) {
	owner, app := fixture(t, true)
	ctx := context.Background()
	now := time.Now().UnixMilli()
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents) VALUES('plus','chatgpt','Plus',1000)`)
	execSQL(t, owner, `INSERT INTO orders(id,owner_hash,idempotency_key,plan,plan_name,price_cents,mode,payment_status,status,created_at,updated_at,last_event_id) VALUES('WAKE','buyer','key','plus','Plus',1000,'live','unpaid','pending',$1,$1,'fixture')`, now)
	var calls atomic.Int32
	var paid atomic.Bool
	startWorker := func() (context.CancelFunc, chan error) {
		run, stop := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() {
			done <- worker.Run(run, app, "wake-test", slog.New(slog.NewTextHandler(io.Discard, nil)), worker.Job{Name: "probe", Wake: true, Interval: time.Hour, Timeout: time.Second, Run: func(ctx context.Context) error {
				calls.Add(1)
				o, e := orders.Get(ctx, app.DB, "WAKE", nil, false)
				if e == nil && o.Payment == "paid" {
					paid.Store(true)
				}
				return e
			}})
		}()
		return stop, done
	}
	stop, done := startWorker()
	defer func() { stop(); <-done }()
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if calls.Load() < 2 {
		t.Fatal("listener did not initialize")
	}
	tx, e := app.DB.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	o, e := orders.Get(ctx, tx, "WAKE", nil, true)
	if e != nil {
		t.Fatal(e)
	}
	next := o
	next.Payment = "paid"
	next.Status = "session"
	if _, e = orders.Change(ctx, tx, o, next, "payment", "payment-confirmed", "fixture"); e != nil {
		t.Fatal(e)
	}
	time.Sleep(100 * time.Millisecond)
	if paid.Load() {
		t.Fatal("uncommitted payment woke fulfillment")
	}
	started := time.Now()
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	deadline = time.Now().Add(3 * time.Second)
	for !paid.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !paid.Load() {
		t.Fatal("committed payment did not wake job")
	}
	t.Logf("payment commit to background observation: %s", time.Since(started))
	stop()
	<-done
	paid.Store(false)
	stop, done = startWorker()
	deadline = time.Now().Add(3 * time.Second)
	for !paid.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !paid.Load() {
		t.Fatal("restart failed to discover durable paid order")
	}
}
