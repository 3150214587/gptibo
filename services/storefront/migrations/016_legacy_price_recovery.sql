-- Match the two audited recovery reasons already accepted by production D1.
ALTER TABLE xgift_recovery_attempts DROP CONSTRAINT xgift_recovery_attempts_previous_failure_code_check;
ALTER TABLE xgift_recovery_attempts ADD CONSTRAINT xgift_recovery_failure_check
 CHECK (previous_failure_code IN ('card_wallet_insufficient','x_price_changed'));
ALTER TABLE xgift_recovery_attempts ADD CONSTRAINT xgift_recovery_stage_check
 CHECK (previous_failure_code <> 'x_price_changed' OR (previous_stage IS NOT NULL AND previous_stage='preflight'));
