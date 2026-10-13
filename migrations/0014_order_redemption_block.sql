ALTER TABLE orders ADD COLUMN redemption_blocked INTEGER NOT NULL DEFAULT 0 CHECK (redemption_blocked IN (0, 1));
