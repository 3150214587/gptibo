ALTER TABLE partners ADD COLUMN parent_id text REFERENCES partners(id);
ALTER TABLE partners ADD COLUMN can_recruit boolean NOT NULL DEFAULT false;
ALTER TABLE partners ADD CONSTRAINT partner_two_levels CHECK(parent_id IS NULL OR (parent_id<>id AND NOT can_recruit));
CREATE INDEX partners_parent ON partners(parent_id) WHERE parent_id IS NOT NULL;
ALTER TABLE partner_prices ADD COLUMN enabled boolean NOT NULL DEFAULT true;
ALTER TABLE orders ADD COLUMN upstream_partner_id text REFERENCES partners(id);
ALTER TABLE orders ADD COLUMN upstream_cost_cents bigint;
ALTER TABLE orders ADD COLUMN upstream_payout_id text REFERENCES partner_payouts(id);
ALTER TABLE orders ADD CONSTRAINT order_upstream_snapshot CHECK(
 (upstream_partner_id IS NULL AND upstream_cost_cents IS NULL AND upstream_payout_id IS NULL) OR
 (upstream_partner_id IS NOT NULL AND partner_id IS NOT NULL AND upstream_partner_id<>partner_id
  AND upstream_cost_cents IS NOT NULL AND cost_cents IS NOT NULL AND upstream_cost_cents>=1 AND upstream_cost_cents<=cost_cents));
CREATE INDEX orders_upstream ON orders(upstream_partner_id,created_at DESC) WHERE upstream_partner_id IS NOT NULL;

ALTER TABLE partner_debits DROP CONSTRAINT partner_debits_refund_id_key;
ALTER TABLE partner_debits ADD CONSTRAINT partner_debits_beneficiary_refund UNIQUE(partner_id,refund_id);

-- One immutable price snapshot, two independently payable beneficiaries.
-- Invoker rights; no access beyond the runtime role's existing order privileges.
CREATE FUNCTION partner_order_profit(order_key text, beneficiary text) RETURNS bigint
LANGUAGE sql STABLE AS $$
 WITH shares AS (
  SELECT o.partner_id,o.upstream_partner_id,
   CASE WHEN o.cost_cents IS NULL THEN NULL WHEN o.plan='duolingo-12m' AND o.refunded_cents>0 THEN 0
    ELSE greatest(0,o.price_cents-o.cost_cents-(o.price_cents-o.cost_cents)*o.refunded_cents/o.price_cents) END own,
   CASE WHEN o.upstream_partner_id IS NULL OR (o.plan='duolingo-12m' AND o.refunded_cents>0) THEN 0
    ELSE greatest(0,o.cost_cents-o.upstream_cost_cents-(o.cost_cents-o.upstream_cost_cents)*o.refunded_cents/o.price_cents) END upstream,
   t.merchant_margin_cents cap
  FROM orders o LEFT JOIN recharge_orders t ON t.order_id=o.id WHERE o.id=order_key
 ), capped AS (
  SELECT *,greatest(0,least(own+upstream,COALESCE(cap,own+upstream))) total FROM shares
 ), allocated AS (
  SELECT *,CASE WHEN own+upstream>0 THEN total*own/(own+upstream) ELSE own END child_share FROM capped
 ) SELECT CASE WHEN beneficiary=partner_id THEN child_share
   WHEN beneficiary=upstream_partner_id THEN total-child_share ELSE NULL END FROM allocated
$$;

-- Narrow internal view for financial reads. Customer data is absent on upstream rows.
CREATE VIEW partner_earning_orders AS
 SELECT id,partner_id,payout_id,price_cents,cost_cents,plan,plan_name,mode,is_test,payment_status,status,
  refund_hold,refunded_cents,version,created_at,updated_at,customer_email,partner_id source_partner_id,false upstream
 FROM orders WHERE partner_id IS NOT NULL
 UNION ALL
 SELECT id,upstream_partner_id,upstream_payout_id,price_cents,price_cents-(cost_cents-upstream_cost_cents),plan,plan_name,mode,is_test,payment_status,status,
  refund_hold,refunded_cents,version,created_at,updated_at,NULL::text,partner_id,true
 FROM orders WHERE upstream_partner_id IS NOT NULL;
