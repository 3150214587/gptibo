-- Preserve existing promises; only newly created orders snapshot changed policy.
ALTER TABLE catalog_plans ADD COLUMN supports_warranty boolean NOT NULL DEFAULT true;
ALTER TABLE orders ADD COLUMN supports_warranty boolean NOT NULL DEFAULT true;
