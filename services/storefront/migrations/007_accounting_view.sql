CREATE VIEW accounting_candidates AS
SELECT o.id order_id,o.plan,o.plan_name,'order'::text kind,o.price_cents sale_cents,
 CASE WHEN o.plan='gmail-account' THEN k.actual_cost_cents WHEN n.order_id IS NOT NULL THEN n.actual_cost_cents ELSE c.cost_cents END cost_cents,
 greatest(o.refunded_cents,COALESCE(t.refunded_cents,0)) refund_cents,o.created_at
FROM orders o LEFT JOIN recharge_orders t ON t.order_id=o.id LEFT JOIN accounting_costs c ON c.plan=o.plan LEFT JOIN account_supplier_orders k ON k.order_id=o.id LEFT JOIN delivery_supplier_orders n ON n.order_id=o.id
WHERE o.mode='live' AND o.is_test=0 AND o.payment_status='paid' AND o.status='success'
 AND o.refund_hold=0 AND COALESCE(t.financial_hold,0)=0
 AND NOT EXISTS(SELECT 1 FROM accounting_items i WHERE i.order_id=o.id AND i.kind='order')
UNION ALL
SELECT o.id,o.plan,o.plan_name,'refund'::text,0::bigint,0::bigint,
 (greatest(o.refunded_cents,COALESCE(t.refunded_cents,0))-(SELECT sum(i.refund_cents) FROM accounting_items i WHERE i.order_id=o.id))::bigint,o.created_at
FROM orders o LEFT JOIN recharge_orders t ON t.order_id=o.id
WHERE o.mode='live' AND o.is_test=0 AND o.payment_status='paid' AND o.refund_hold=0 AND COALESCE(t.financial_hold,0)=0
 AND EXISTS(SELECT 1 FROM accounting_items i WHERE i.order_id=o.id AND i.kind='order')
 AND greatest(o.refunded_cents,COALESCE(t.refunded_cents,0))>(SELECT sum(i.refund_cents) FROM accounting_items i WHERE i.order_id=o.id);
