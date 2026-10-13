CREATE TABLE partner_product_order (
  partner_id TEXT PRIMARY KEY REFERENCES partners(id),
  product_ids TEXT NOT NULL,
  version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
  change_id TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);
