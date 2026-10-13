-- An explicit operational reset skips a historical request when calculating
-- cooldown, while retaining its original timestamps and financial records.
ALTER TABLE partner_payouts ADD COLUMN cooldown_exempt INTEGER NOT NULL DEFAULT 0 CHECK(cooldown_exempt IN (0,1));
