-- Confirmed public progress belongs to one attempt; old rows keep version zero.
ALTER TABLE recharge_redemptions ADD COLUMN progress_stage TEXT;
ALTER TABLE recharge_redemptions ADD COLUMN progress_version INTEGER NOT NULL DEFAULT 0;
ALTER TABLE recharge_redemptions ADD COLUMN progress_updated_at INTEGER;
