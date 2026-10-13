-- Authoritative supplier receivables cap downstream commissions.
ALTER TABLE recharge_orders ADD COLUMN supply_cents INTEGER CHECK(supply_cents>=0);
ALTER TABLE recharge_orders ADD COLUMN merchant_margin_cents INTEGER CHECK(merchant_margin_cents>=0);
ALTER TABLE recharge_orders ADD COLUMN refunded_cents INTEGER NOT NULL DEFAULT 0 CHECK(refunded_cents>=0);
ALTER TABLE recharge_orders ADD COLUMN financial_hold INTEGER NOT NULL DEFAULT 1 CHECK(financial_hold IN (0,1));
-- A notification arriving during a query must remain queued until another query succeeds.
ALTER TABLE recharge_orders ADD COLUMN notify_version INTEGER NOT NULL DEFAULT 0;
ALTER TABLE recharge_orders ADD COLUMN checked_notify_version INTEGER NOT NULL DEFAULT 0;
ALTER TABLE recharge_refunds ADD COLUMN updated_at INTEGER NOT NULL DEFAULT 0;
