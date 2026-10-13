CREATE TABLE recharge_reminders (
  order_id TEXT PRIMARY KEY REFERENCES orders(id),
  paid_at INTEGER NOT NULL,
  next_attempt_at INTEGER NOT NULL,
  first_attempt_at INTEGER,
  attempt_count INTEGER NOT NULL DEFAULT 0,
  delivered_at INTEGER,
  cancelled_at INTEGER,
  message_json TEXT,
  recipient TEXT,
  locked_until INTEGER NOT NULL DEFAULT 0,
  lock_token TEXT NOT NULL DEFAULT ''
);
CREATE INDEX recharge_reminders_due ON recharge_reminders(next_attempt_at)
  WHERE delivered_at IS NULL AND cancelled_at IS NULL;

-- Capture the first local confirmation atomically with payment, including late payments.
CREATE TRIGGER queue_recharge_reminder AFTER UPDATE OF payment_status ON orders
WHEN NEW.mode='live' AND NEW.is_test=0 AND NEW.payment_status='paid'
  AND OLD.payment_status<>'paid' AND NEW.status='session'
  AND NEW.plan<>'codex-sms-once' AND NEW.customer_email IS NOT NULL
BEGIN
  INSERT OR IGNORE INTO recharge_reminders(order_id,paid_at,next_attempt_at)
  VALUES(NEW.id,NEW.updated_at,NEW.updated_at+900000);
END;

-- Existing waiting orders use their payment event, never their last update time.
INSERT OR IGNORE INTO recharge_reminders(order_id,paid_at,next_attempt_at)
SELECT o.id,MIN(e.created_at),MIN(e.created_at)+900000
FROM orders o JOIN order_events e ON e.order_id=o.id AND e.action='payment-confirmed'
WHERE o.mode='live' AND o.is_test=0 AND o.payment_status='paid' AND o.status='session'
  AND o.plan<>'codex-sms-once' AND o.customer_email IS NOT NULL
GROUP BY o.id;
