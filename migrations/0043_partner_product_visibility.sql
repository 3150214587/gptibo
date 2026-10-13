ALTER TABLE partner_product_order ADD COLUMN hidden_product_ids TEXT NOT NULL DEFAULT '[]'
  CHECK (json_valid(hidden_product_ids) AND json_type(hidden_product_ids) = 'array');
