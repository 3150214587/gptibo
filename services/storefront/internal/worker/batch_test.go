package worker

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestBatchSlowOrderDoesNotBlockOthersAndConcurrencyIsBounded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	blocked, release, fast := make(chan struct{}), make(chan struct{}), make(chan struct{}, 10)
	var running, maxRunning atomic.Int32
	done := make(chan error, 1)
	failure := errors.New("slow upstream")
	go func() {
		done <- Each(ctx, []string{"slow", "a", "b", "c", "d"}, func(ctx context.Context, id string) error {
			n := running.Add(1)
			defer running.Add(-1)
			for previous := maxRunning.Load(); n > previous; previous = maxRunning.Load() {
				if maxRunning.CompareAndSwap(previous, n) {
					break
				}
			}
			if id == "slow" {
				close(blocked)
				select {
				case <-release:
					return failure
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			fast <- struct{}{}
			return nil
		})
	}()
	select {
	case <-blocked:
	case <-ctx.Done():
		t.Fatal("slow job never started")
	}
	for range 4 {
		select {
		case <-fast:
		case <-ctx.Done():
			t.Fatal("slow order blocked independent work")
		}
	}
	close(release)
	if e := <-done; !errors.Is(e, failure) {
		t.Fatal("lost upstream error", e)
	}
	if maxRunning.Load() > 3 {
		t.Fatal("unbounded concurrency")
	}
}
