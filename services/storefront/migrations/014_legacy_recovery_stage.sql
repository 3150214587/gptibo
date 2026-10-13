-- Some production D1 installations added this audit field after migration 0038.
-- Keep its original value; it must never replay a historical recovery.
ALTER TABLE xgift_recovery_attempts ADD COLUMN previous_stage text;
