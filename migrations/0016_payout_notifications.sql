CREATE TABLE payout_notifications (
  payout_id TEXT PRIMARY KEY REFERENCES partner_payouts(id),
  created_at INTEGER NOT NULL,
  delivered_at INTEGER,
  next_attempt_at INTEGER NOT NULL DEFAULT 0,
  locked_until INTEGER NOT NULL DEFAULT 0,
  lock_token TEXT NOT NULL DEFAULT '',
  attempt_count INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX payout_notifications_due ON payout_notifications(delivered_at,next_attempt_at,locked_until);
