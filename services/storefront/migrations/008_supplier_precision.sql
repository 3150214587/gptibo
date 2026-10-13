-- SQLite REAL stores IEEE-754 doubles; PostgreSQL REAL is only single precision.
ALTER TABLE delivery_supplier_orders ALTER COLUMN cny_per_usd TYPE double precision;
