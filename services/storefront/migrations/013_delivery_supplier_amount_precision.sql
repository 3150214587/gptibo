ALTER TABLE delivery_supplier_orders ADD COLUMN actual_usd_micros bigint
  CHECK (actual_usd_micros IS NULL OR (actual_usd_micros > 0 AND actual_usd_micros <= max_usd_cents * 10000));
UPDATE delivery_supplier_orders SET actual_usd_micros = actual_usd_cents * 10000
  WHERE actual_usd_cents IS NOT NULL;
