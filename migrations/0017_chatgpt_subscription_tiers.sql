-- The former 20x keeps its ID so supplier mappings and existing orders work.
-- orders.plan_name and price snapshots are deliberately left untouched.
UPDATE catalog_plans
SET name='Pro 10×', renewal_only=0, price_cents=140000,
    version=version+1, updated_at=unixepoch()*1000
WHERE id='pro-20x';

INSERT INTO catalog_plans(id,product_id,name,price_cents,renewal_only,updated_at)
VALUES ('pro-25x','chatgpt','Pro 25×',450000,0,unixepoch()*1000);

INSERT INTO partner_default_prices(plan,cost_cents) VALUES ('pro-25x',330000);
INSERT INTO partner_prices(partner_id,plan,cost_cents,price_cents)
SELECT id,'pro-25x',330000,450000 FROM partners;
UPDATE partner_default_price_settings
SET version=version+1, updated_at=unixepoch()*1000 WHERE id=1;
