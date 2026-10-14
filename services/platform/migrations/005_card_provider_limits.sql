-- Existing rules and order snapshots continue enforcing their configured cap.
ALTER TABLE card_rules ADD COLUMN use_provider_limits boolean NOT NULL DEFAULT false;
