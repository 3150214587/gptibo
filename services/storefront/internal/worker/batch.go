package worker

import (
	"context"
	"sync"
)

// Each bounds upstream concurrency so one slow order does not stall the batch.
// Individual services still enforce database leases and provider idempotency.
func Each(ctx context.Context, ids []string, run func(context.Context, string) error) error {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var first error
	queue := make(chan string, len(ids))
	for _, id := range ids {
		queue <- id
	}
	close(queue)
	for range min(3, len(ids)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range queue {
				if ctx.Err() != nil {
					mu.Lock()
					if first == nil {
						first = ctx.Err()
					}
					mu.Unlock()
					return
				}
				if e := run(ctx, id); e != nil {
					mu.Lock()
					if first == nil {
						first = e
					}
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	return first
}
