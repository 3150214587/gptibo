-- X was added without prices for existing partners. Use the same conservative
-- retail-price fallback as partner creation; administrators can set supply prices.
-- Preserve all explicit prices and the separate retail/partner sale switches.
UPDATE partner_default_price_settings
SET version=version+1,updated_at=CAST(strftime('%s','now') AS INTEGER)*1000
WHERE id=1 AND EXISTS(
  SELECT 1 FROM catalog_plans c LEFT JOIN partner_default_prices d ON d.plan=c.id
  WHERE c.product_id='x-premium' AND d.plan IS NULL
);

INSERT INTO partner_default_prices(plan,cost_cents)
SELECT id,price_cents FROM catalog_plans WHERE product_id='x-premium'
ON CONFLICT(plan) DO NOTHING;

INSERT INTO partner_prices(partner_id,plan,cost_cents,price_cents)
SELECT p.id,c.id,d.cost_cents,MAX(c.price_cents,d.cost_cents)
FROM partners p CROSS JOIN catalog_plans c JOIN partner_default_prices d ON d.plan=c.id
WHERE c.product_id='x-premium'
ON CONFLICT(partner_id,plan) DO NOTHING;
