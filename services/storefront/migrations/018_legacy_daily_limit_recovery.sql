-- Preserve authorized legacy recovery audits without replaying their triggers.
-- New recovery requests remain restricted by the application's upstream checks.
ALTER TABLE xgift_recovery_attempts DROP CONSTRAINT xgift_recovery_failure_check;
ALTER TABLE xgift_recovery_attempts ADD CONSTRAINT xgift_recovery_failure_check
 CHECK (previous_failure_code IN ('card_wallet_insufficient','x_price_changed','card_daily_limit'));
ALTER TABLE xgift_recovery_attempts ADD CONSTRAINT xgift_recovery_daily_limit_stage_check
 CHECK (previous_failure_code <> 'card_daily_limit' OR (previous_stage IS NOT NULL AND previous_stage='card'));
