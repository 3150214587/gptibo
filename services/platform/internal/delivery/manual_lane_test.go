package delivery

import (
	"context"
	"gptibo/platform/internal/commerce"
	"gptibo/platform/internal/providers"
	"gptibo/platform/internal/testutil"
	"testing"
	"time"
)

func TestSlowManualQueryDoesNotBlockAutomaticQueue(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	_, err := s.DB.Exec(ctx, `INSERT INTO users(id,email,name,role,status) VALUES('user','test@example.com','test','channel','active'); INSERT INTO wallets(user_id) VALUES('user')`)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"manual", "automatic"} {
		_, err = s.DB.Exec(ctx, `INSERT INTO orders(id,user_id,merchant_order_no,idempotency_key,request_hash,product_code,business,supply_minor,status,stage,config_cipher,job_cipher,manual_payment_at) VALUES($1,'user',$1,$1,'hash','x-3m','x',1700,'unknown','submitted',$2,$3,CASE WHEN $1='manual' THEN now() ELSE NULL END)`, id, s.Vault.Seal("order:"+id, []byte(`{"business":"x"}`)), s.Vault.Seal("job:"+id, []byte(`{"stage":"submitted"}`)))
		if err != nil {
			t.Fatal(err)
		}
	}
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	driver := providers.DriverFunc(func(ctx context.Context, o commerce.Order, _ commerce.Snapshot, _ commerce.Credential, _ *providers.Job, _ func() error) (providers.Result, error) {
		if o.ID == "manual" {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		return providers.Result{Status: "unknown", Stage: "submitted"}, nil
	})
	w := &Worker{Store: s, Engine: &providers.Engine{Store: s, Drivers: map[string]providers.Driver{"x": driver}}, Commerce: &commerce.Service{Store: s}}
	queryCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := w.orderLane(queryCtx, true); done <- err }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("manual query not claimed")
	}
	auto := make(chan bool, 1)
	go func() { worked, err := w.order(ctx); auto <- worked && err == nil }()
	select {
	case worked := <-auto:
		if !worked {
			t.Fatal("automatic lane did not proceed")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("manual query blocked automatic order")
	}
	if worked, err := w.orderLane(ctx, true); worked || err != nil {
		t.Fatal("manual lease duplicated", err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("manual worker did not stop")
	}
}
