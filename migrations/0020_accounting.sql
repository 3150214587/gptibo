CREATE TABLE accounting_state (
  id INTEGER PRIMARY KEY CHECK(id=1),
  version INTEGER NOT NULL DEFAULT 0,
  last_settlement_id TEXT REFERENCES accounting_settlements(id)
);
CREATE TABLE accounting_costs (
  plan TEXT PRIMARY KEY REFERENCES catalog_plans(id),
  cost_cents INTEGER CHECK(cost_cents IS NULL OR (cost_cents >= 0 AND cost_cents <= 10000000))
);
INSERT INTO accounting_costs(plan,cost_cents) SELECT id, CASE id WHEN 'plus' THEN 11300 WHEN 'pro-5x' THEN 63800 WHEN 'pro-20x' THEN 100000 ELSE NULL END FROM catalog_plans;
CREATE TABLE accounting_settlements (
  id TEXT PRIMARY KEY,
  base_version INTEGER NOT NULL UNIQUE,
  previous_id TEXT REFERENCES accounting_settlements(id),
  from_at INTEGER NOT NULL,
  closed_at INTEGER NOT NULL,
  order_count INTEGER NOT NULL DEFAULT 0,
  adjustment_count INTEGER NOT NULL DEFAULT 0,
  sale_cents INTEGER NOT NULL DEFAULT 0,
  cost_cents INTEGER NOT NULL DEFAULT 0,
  refund_cents INTEGER NOT NULL DEFAULT 0,
  profit_cents INTEGER NOT NULL DEFAULT 0
);
INSERT INTO accounting_state(id) VALUES(1);
CREATE TABLE accounting_items (
  settlement_id TEXT NOT NULL REFERENCES accounting_settlements(id),
  order_id TEXT NOT NULL REFERENCES orders(id),
  plan TEXT NOT NULL,
  plan_name TEXT NOT NULL,
  kind TEXT NOT NULL CHECK(kind IN ('order','refund')),
  sale_cents INTEGER NOT NULL,
  cost_cents INTEGER NOT NULL,
  refund_cents INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY(settlement_id,order_id)
);
CREATE UNIQUE INDEX accounting_order_once ON accounting_items(order_id) WHERE kind='order';
CREATE INDEX accounting_items_order ON accounting_items(order_id);
CREATE INDEX accounting_settlements_time ON accounting_settlements(closed_at DESC);
-- Eligibility is based on unrecorded completed orders, so late completion and
-- a released financial hold remain eligible in a later accounting period.
CREATE VIEW accounting_candidates AS
SELECT o.id order_id,o.plan,o.plan_name,'order' kind,o.price_cents sale_cents,c.cost_cents,
  MAX(o.refunded_cents,COALESCE(t.refunded_cents,0)) refund_cents,o.created_at
FROM orders o LEFT JOIN recharge_orders t ON t.order_id=o.id LEFT JOIN accounting_costs c ON c.plan=o.plan
WHERE o.mode='live' AND o.is_test=0 AND o.payment_status='paid' AND o.status='success'
  AND o.refund_hold=0 AND COALESCE(t.financial_hold,0)=0
  AND NOT EXISTS(SELECT 1 FROM accounting_items i WHERE i.order_id=o.id AND i.kind='order')
UNION ALL
SELECT o.id,o.plan,o.plan_name,'refund',0,0,
  MAX(o.refunded_cents,COALESCE(t.refunded_cents,0))-(SELECT SUM(i.refund_cents) FROM accounting_items i WHERE i.order_id=o.id),o.created_at
FROM orders o LEFT JOIN recharge_orders t ON t.order_id=o.id
WHERE o.mode='live' AND o.is_test=0 AND o.payment_status='paid' AND o.refund_hold=0 AND COALESCE(t.financial_hold,0)=0
  AND EXISTS(SELECT 1 FROM accounting_items i WHERE i.order_id=o.id AND i.kind='order')
  AND MAX(o.refunded_cents,COALESCE(t.refunded_cents,0))>(SELECT SUM(i.refund_cents) FROM accounting_items i WHERE i.order_id=o.id);
