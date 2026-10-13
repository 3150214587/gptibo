CREATE TABLE partner_default_price_settings (
  id INTEGER PRIMARY KEY CHECK(id = 1),
  version INTEGER NOT NULL DEFAULT 0,
  change_id TEXT,
  updated_at INTEGER NOT NULL DEFAULT 0
);
INSERT INTO partner_default_price_settings(id) VALUES (1);

CREATE TABLE partner_default_prices (
  plan TEXT PRIMARY KEY REFERENCES catalog_plans(id),
  cost_cents INTEGER NOT NULL CHECK(cost_cents > 0 AND cost_cents <= 10000000)
);
INSERT INTO partner_default_prices(plan,cost_cents)
SELECT id,price_cents FROM catalog_plans;
