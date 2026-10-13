-- Keep the signed platform margin separately from the nonnegative commission cap.
ALTER TABLE recharge_orders ADD COLUMN reported_margin_cents INTEGER;
ALTER TABLE recharge_orders ADD COLUMN verified_at INTEGER;
