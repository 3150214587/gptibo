ALTER TABLE orders ADD COLUMN is_test INTEGER NOT NULL DEFAULT 0 CHECK(is_test IN (0,1));
-- Only one unfinished live test at a time. A refunded/closed test allows a new one.
CREATE UNIQUE INDEX one_open_payment_test ON orders(is_test)
WHERE is_test=1 AND status IN ('pending','session') AND refunded_cents=0;
ALTER TABLE payments ADD COLUMN notified_at INTEGER;
