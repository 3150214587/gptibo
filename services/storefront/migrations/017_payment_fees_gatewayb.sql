ALTER TABLE payment_settings DROP CONSTRAINT payment_settings_provider_check;
ALTER TABLE payment_settings ADD CONSTRAINT payment_settings_provider_check CHECK(provider IN ('','alipay','gatewaya-alipay','gatewaya-wxpay','gatewayb-alipay','gatewayb-wxpay'));
ALTER TABLE payment_settings DROP CONSTRAINT enabled_payment_providers;
ALTER TABLE payment_settings ADD CONSTRAINT enabled_payment_providers CHECK (
 enabled_providers IS NULL OR (
 cardinality(enabled_providers) BETWEEN 1 AND 2
 AND enabled_providers <@ ARRAY['alipay','gatewaya-alipay','gatewaya-wxpay','gatewayb-alipay','gatewayb-wxpay']::text[]
 AND array_position(enabled_providers,NULL) IS NULL
 AND (cardinality(enabled_providers)=1 OR (
   (enabled_providers[1] LIKE '%wxpay') <> (enabled_providers[2] LIKE '%wxpay')
 ))
 ));
ALTER TABLE payment_settings ADD COLUMN fee_rates_bps jsonb NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(fee_rates_bps)='object');
ALTER TABLE payments ADD COLUMN fee_bps bigint NOT NULL DEFAULT 0 CHECK(fee_bps BETWEEN 0 AND 10000);
ALTER TABLE payments ADD COLUMN fee_cents bigint NOT NULL DEFAULT 0 CHECK(fee_cents>=0 AND fee_cents<amount_cents);
ALTER TABLE payments ADD CONSTRAINT payment_fee_calculation CHECK(fee_cents=((amount_cents-fee_cents)*fee_bps+5000)/10000);
CREATE TABLE payment_refund_attempts (
 id text PRIMARY KEY,
 order_id text NOT NULL REFERENCES payments(order_id),
 product_cents bigint NOT NULL CHECK(product_cents>0),
 fee_cents bigint NOT NULL CHECK(fee_cents>=0),
 state text NOT NULL DEFAULT 'prepared' CHECK(state IN ('prepared','submitted','confirmed')),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX payment_refund_attempts_order ON payment_refund_attempts(order_id);

-- Merchandise/partner commissions remain separate; platform bookkeeping uses gross receipts.
CREATE OR REPLACE VIEW accounting_candidates AS
SELECT o.id order_id,o.plan,o.plan_name,'order'::text kind,o.price_cents+COALESCE(p.fee_cents,0) sale_cents,
 CASE WHEN o.plan='gmail-account' THEN k.actual_cost_cents WHEN n.order_id IS NOT NULL THEN n.actual_cost_cents ELSE c.cost_cents END cost_cents,
 (greatest(o.refunded_cents,COALESCE(t.refunded_cents,0))+COALESCE(r.fee,0)) refund_cents,o.created_at
FROM orders o LEFT JOIN recharge_orders t ON t.order_id=o.id LEFT JOIN payments p ON p.order_id=o.id LEFT JOIN (SELECT order_id,sum(fee_cents)::bigint fee FROM payment_refund_attempts WHERE state='confirmed' GROUP BY order_id) r ON r.order_id=o.id LEFT JOIN accounting_costs c ON c.plan=o.plan LEFT JOIN account_supplier_orders k ON k.order_id=o.id LEFT JOIN delivery_supplier_orders n ON n.order_id=o.id
WHERE o.mode='live' AND o.is_test=0 AND o.payment_status='paid' AND o.status='success'
 AND o.refund_hold=0 AND COALESCE(t.financial_hold,0)=0
 AND NOT EXISTS(SELECT 1 FROM accounting_items i WHERE i.order_id=o.id AND i.kind='order')
UNION ALL
SELECT o.id,o.plan,o.plan_name,'refund'::text,0::bigint,0::bigint,
 ((greatest(o.refunded_cents,COALESCE(t.refunded_cents,0))+COALESCE(r.fee,0))-(SELECT sum(i.refund_cents) FROM accounting_items i WHERE i.order_id=o.id))::bigint,o.created_at
FROM orders o LEFT JOIN recharge_orders t ON t.order_id=o.id LEFT JOIN payments p ON p.order_id=o.id LEFT JOIN (SELECT order_id,sum(fee_cents)::bigint fee FROM payment_refund_attempts WHERE state='confirmed' GROUP BY order_id) r ON r.order_id=o.id
WHERE o.mode='live' AND o.is_test=0 AND o.payment_status='paid' AND o.refund_hold=0 AND COALESCE(t.financial_hold,0)=0
 AND EXISTS(SELECT 1 FROM accounting_items i WHERE i.order_id=o.id AND i.kind='order')
 AND (greatest(o.refunded_cents,COALESCE(t.refunded_cents,0))+COALESCE(r.fee,0))>(SELECT sum(i.refund_cents) FROM accounting_items i WHERE i.order_id=o.id);
