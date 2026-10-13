package recharge

import (
	"context"
	"gptibo/storefront/internal/orders"
	"gptibo/storefront/internal/worker"
	"time"
)

// Fast lane for pending payments, running redemptions and verified webhook hints.
// Completed orders and financial reconciliation retain the slower full scan.
func (s *Service) ReconcileActive(ctx context.Context) error {
	now := time.Now().UnixMilli()
	list, e := orders.Rows(ctx, s.Store.DB, `SELECT t.order_id FROM recharge_orders t JOIN orders o ON o.id=t.order_id WHERE t.lease_until<=$1 AND t.updated_at<=$2 AND (t.notify_version>t.checked_notify_version OR o.status IN ('pending','processing') OR (o.status='session' AND EXISTS(SELECT 1 FROM recharge_redemptions a WHERE a.order_id=o.id AND a.status='unknown'))) ORDER BY CASE WHEN o.status='processing' THEN 0 WHEN o.status='session' THEN 1 WHEN o.status='pending' THEN 2 ELSE 3 END,t.updated_at LIMIT 10`, now, now-10000)
	if e != nil {
		return e
	}
	ids := make([]string, 0, len(list))
	for _, r := range list {
		ids = append(ids, r["order_id"].(string))
	}
	return worker.Each(ctx, ids, func(ctx context.Context, id string) error {
		e := s.Sync(ctx, id, true)
		if e != nil {
			_, _ = s.Store.DB.Exec(ctx, `UPDATE recharge_orders SET updated_at=$1 WHERE order_id=$2 AND lease_until<=$1 AND updated_at<=$3`, time.Now().UnixMilli(), id, now-10000)
		}
		return e
	})
}
