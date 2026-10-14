CREATE TABLE manual_alerts (
 order_id text PRIMARY KEY REFERENCES orders(id),
 reason text NOT NULL,
 status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','sent','cancelled')),
 attempts integer NOT NULL DEFAULT 0,
 next_at timestamptz NOT NULL DEFAULT now(),
 lease_token text NOT NULL DEFAULT '',
 lease_until timestamptz,
 last_error text NOT NULL DEFAULT '',
 message_id text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(),
 sent_at timestamptz
);
CREATE INDEX manual_alerts_due ON manual_alerts(next_at) WHERE status='pending';
-- Recover existing live handoffs, without notifying about historical completed orders.
INSERT INTO manual_alerts(order_id,reason)
 SELECT id,'payment_action_required' FROM orders
 WHERE manual_payment_at IS NOT NULL AND status IN ('queued','running','unknown');
