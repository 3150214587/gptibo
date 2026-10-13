package catalog

import (
	"context"
	"maps"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
	"gptibo/storefront/internal/store"
)

// One cache per API router/database, shared by the platform and every shop.
// Never cache prices, availability or shop settings with these public totals.
type CompletedOrderCache struct {
	mu         sync.Mutex
	group      singleflight.Group
	counts     map[string]int64
	err        error
	validUntil time.Time
	now        func() time.Time
	load       func(context.Context) (map[string]int64, error)
}

func NewCompletedOrderCache(s *store.Store) *CompletedOrderCache {
	return &CompletedOrderCache{now: time.Now, load: func(ctx context.Context) (map[string]int64, error) {
		return readCompletedOrders(ctx, s)
	}}
}

func (c *CompletedOrderCache) cached() (map[string]int64, error, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return maps.Clone(c.counts), c.err, c.now().Before(c.validUntil)
}

func (c *CompletedOrderCache) Read(ctx context.Context) (map[string]int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if counts, err, ok := c.cached(); ok {
		return counts, err
	}
	result := c.group.DoChan("totals", func() (any, error) {
		if counts, err, ok := c.cached(); ok {
			return counts, err
		}
		// A disconnected visitor must not cancel the shared refresh for others.
		refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		counts, err := c.load(refreshCtx)
		ttl := time.Minute
		if err != nil {
			counts = nil
			ttl = 5 * time.Second // Bound retries while the statistics query is failing.
		}
		c.mu.Lock()
		c.counts, c.err, c.validUntil = maps.Clone(counts), err, c.now().Add(ttl)
		c.mu.Unlock()
		return counts, err
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-result:
		if r.Err != nil {
			return nil, r.Err
		}
		return maps.Clone(r.Val.(map[string]int64)), nil
	}
}

func readCompletedOrders(ctx context.Context, s *store.Store) (map[string]int64, error) {
	// Read orders, not earning allocations: reseller shares are not extra sales.
	rows, err := s.DB.Query(ctx, `SELECT c.product_id,count(*) FROM orders o JOIN catalog_plans c ON c.id=o.plan
 WHERE o.mode='live' AND o.is_test=0 AND o.payment_status='paid' AND o.status='success'
 AND o.refunded_cents=0 AND o.refund_hold=0 GROUP BY c.product_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int64{}
	for rows.Next() {
		var productID string
		var count int64
		if err := rows.Scan(&productID, &count); err != nil {
			return nil, err
		}
		counts[productID] = count
	}
	return counts, rows.Err()
}
