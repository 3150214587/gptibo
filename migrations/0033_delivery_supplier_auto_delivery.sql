CREATE TABLE delivery_supplier_orders (
  order_id TEXT PRIMARY KEY REFERENCES orders(id),
  service_id TEXT NOT NULL,
  credential_hash TEXT NOT NULL,
  request_key TEXT NOT NULL UNIQUE,
  request_body TEXT NOT NULL,
  max_usd_cents INTEGER NOT NULL CHECK(max_usd_cents>0),
  cny_per_usd REAL NOT NULL CHECK(cny_per_usd>0),
  instructions TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT 'prepared' CHECK(state IN ('prepared','creating','unknown','pending','purchased','rejected')),
  supplier_order TEXT UNIQUE,
  actual_usd_cents INTEGER,
  actual_cost_cents INTEGER,
  value TEXT,
  attempted_at INTEGER,
  lease_token TEXT,
  lease_until INTEGER NOT NULL DEFAULT 0,
  next_check_at INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL
);
CREATE INDEX delivery_supplier_reconcile ON delivery_supplier_orders(state,next_check_at,updated_at);
UPDATE catalog_plans SET purchase_note=REPLACE(purchase_note,'邮件交付领取链接，付款后由工作人员处理。','付款确认后自动发货，领取链接在订单页展示，并发送至下单邮箱。'),version=version+1 WHERE id='gemini-pro-18m';
UPDATE catalog_plans SET purchase_note=REPLACE(purchase_note,'邮件交付兑换 CDK，付款后由工作人员处理。','付款确认后自动发货，兑换 CDK 在订单页展示，并发送至下单邮箱。'),version=version+1 WHERE id='duolingo-12m';

DROP VIEW accounting_candidates;
CREATE VIEW accounting_candidates AS
SELECT o.id order_id,o.plan,o.plan_name,'order' kind,o.price_cents sale_cents,
  CASE WHEN o.plan='gmail-account' THEN k.actual_cost_cents WHEN n.order_id IS NOT NULL THEN n.actual_cost_cents ELSE c.cost_cents END cost_cents,
  MAX(o.refunded_cents,COALESCE(t.refunded_cents,0)) refund_cents,o.created_at
FROM orders o LEFT JOIN recharge_orders t ON t.order_id=o.id LEFT JOIN accounting_costs c ON c.plan=o.plan LEFT JOIN account_supplier_orders k ON k.order_id=o.id LEFT JOIN delivery_supplier_orders n ON n.order_id=o.id
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
