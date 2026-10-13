-- Pin every purchase to its original supplier across cutover and rollback.
ALTER TABLE xgift_purchases ADD COLUMN supplier_backend TEXT NOT NULL DEFAULT 'legacy' CHECK(supplier_backend IN ('legacy','platform'));
CREATE TABLE xgift_preflight_backends (
 token TEXT PRIMARY KEY REFERENCES xgift_preflights(token) ON DELETE CASCADE,
 supplier_backend TEXT NOT NULL CHECK(supplier_backend IN ('legacy','platform'))
);
CREATE TRIGGER xgift_backend_immutable BEFORE UPDATE OF supplier_backend ON xgift_purchases
BEGIN SELECT RAISE(ABORT,'immutable_xgift_backend'); END;
