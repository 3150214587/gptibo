-- Preserve paid receipt snapshots while new domains temporarily cost zero.
ALTER TABLE domain_charges DROP CONSTRAINT domain_charges_amount_cents_check;
ALTER TABLE domain_charges ALTER COLUMN amount_cents SET DEFAULT 0;
ALTER TABLE domain_charges ADD CONSTRAINT domain_charges_amount_cents_check CHECK(amount_cents BETWEEN 0 AND 10000000);
ALTER TABLE domain_charges DROP CONSTRAINT domain_charges_source_check;
ALTER TABLE domain_charges ADD CONSTRAINT domain_charges_source_check CHECK(source IN ('balance','gatewayb','admin','promotion'));
ALTER TABLE domain_charges ADD CONSTRAINT domain_charge_promotion_amount CHECK((source<>'promotion' OR (amount_cents=0 AND state='paid')) AND (source NOT IN ('balance','gatewayb') OR amount_cents>0));
