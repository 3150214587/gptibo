-- New subscriptions cost CNY 5. Keep existing CNY 20 receipts immutable:
-- providers must still reconcile those receipts against their recorded amounts.
ALTER TABLE domain_charges DROP CONSTRAINT domain_charges_amount_cents_check;
ALTER TABLE domain_charges ALTER COLUMN amount_cents SET DEFAULT 500;
ALTER TABLE domain_charges ADD CONSTRAINT domain_charges_amount_cents_check CHECK(amount_cents IN (500,2000));
