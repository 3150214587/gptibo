-- Existing stores retain all products; visibility shares the product settings version.
ALTER TABLE partner_order_versions ADD COLUMN hidden_product_ids jsonb NOT NULL DEFAULT '[]'::jsonb
  CHECK (jsonb_typeof(hidden_product_ids) = 'array');
