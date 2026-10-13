package catalog

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCompletedOrderCacheExpiryAndCopies(t *testing.T) {
	var seconds atomic.Int64
	calls := 0
	cache := &CompletedOrderCache{now: func() time.Time { return time.Unix(seconds.Load(), 0) }, load: func(context.Context) (map[string]int64, error) {
		calls++
		return map[string]int64{"chatgpt": int64(calls)}, nil
	}}
	read := func(want int64) map[string]int64 {
		t.Helper()
		got, err := cache.Read(context.Background())
		if err != nil || got["chatgpt"] != want {
			t.Fatalf("counts=%v err=%v, want %d", got, err, want)
		}
		return got
	}
	first := read(1)
	first["chatgpt"] = 999
	seconds.Store(59)
	read(1)
	if calls != 1 {
		t.Fatal("cached count was queried again")
	}
	seconds.Store(60)
	read(2)
	if calls != 2 {
		t.Fatal("expired count did not refresh")
	}
}

func TestCompletedOrderCacheSharesRefreshAfterVisitorCancels(t *testing.T) {
	var calls atomic.Int64
	started, release := make(chan struct{}), make(chan struct{})
	cache := &CompletedOrderCache{now: time.Now, load: func(ctx context.Context) (map[string]int64, error) {
		calls.Add(1)
		close(started)
		select {
		case <-release:
			return map[string]int64{"chatgpt": 12}, ctx.Err()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, err := cache.Read(ctx); first <- err }()
	<-started
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled visitor did not return: %v", err)
	}
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			got, err := cache.Read(context.Background())
			if err != nil || got["chatgpt"] != 12 {
				t.Errorf("shared refresh failed: %v %v", got, err)
				return
			}
			got["chatgpt"] = 999 // Each reader owns its copy, including shared misses.
		})
	}
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("concurrent visitors triggered %d queries", calls.Load())
	}
}

func TestCompletedOrderCacheFailureCooldownAndRecovery(t *testing.T) {
	var seconds atomic.Int64
	calls := 0
	unavailable := errors.New("statistics unavailable")
	cache := &CompletedOrderCache{now: func() time.Time { return time.Unix(seconds.Load(), 0) }, load: func(context.Context) (map[string]int64, error) {
		calls++
		if calls == 1 {
			return map[string]int64{"chatgpt": 1}, nil
		}
		if calls == 2 {
			return nil, unavailable
		}
		return map[string]int64{"chatgpt": 2}, nil
	}}
	if _, err := cache.Read(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, second := range []int64{60, 61, 64} {
		seconds.Store(second)
		got, err := cache.Read(context.Background())
		if got != nil || !errors.Is(err, unavailable) {
			t.Fatalf("failed refresh exposed stale/zero counts: %v %v", got, err)
		}
	}
	if calls != 2 {
		t.Fatal("failed refresh was retried during cooldown")
	}
	seconds.Store(65)
	got, err := cache.Read(context.Background())
	if err != nil || got["chatgpt"] != 2 || calls != 3 {
		t.Fatalf("cache did not recover: %v %v, queries=%d", got, err, calls)
	}
}
