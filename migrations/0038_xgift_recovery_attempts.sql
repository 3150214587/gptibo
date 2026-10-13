-- A recovery purchases a new supplier attempt; the failed supplier order stays immutable.
ALTER TABLE xgift_purchases ADD COLUMN supplier_merchant_order_no TEXT;
ALTER TABLE xgift_purchases ADD COLUMN recovery_attempt INTEGER NOT NULL DEFAULT 0;
CREATE TABLE xgift_recovery_attempts (
 id TEXT PRIMARY KEY,
 order_id TEXT NOT NULL REFERENCES orders(id),
 attempt INTEGER NOT NULL CHECK(attempt>0),
 merchant_order_no TEXT NOT NULL UNIQUE,
 previous_merchant_order_no TEXT NOT NULL,
 previous_supplier_order TEXT NOT NULL,
 previous_failure_code TEXT NOT NULL CHECK(previous_failure_code='card_wallet_insufficient'),
 expected_order_version INTEGER NOT NULL,
 actor TEXT NOT NULL,
 note TEXT NOT NULL CHECK(length(note) BETWEEN 1 AND 500),
 created_at INTEGER NOT NULL,
 UNIQUE(order_id,attempt)
);
CREATE TRIGGER xgift_recovery_guard BEFORE INSERT ON xgift_recovery_attempts
 WHEN NOT EXISTS (
  SELECT 1 FROM xgift_purchases x JOIN orders o ON o.id=x.order_id
  WHERE x.order_id=NEW.order_id AND x.supplier_backend='platform'
   AND x.state='failed' AND x.failure_code=NEW.previous_failure_code
   AND x.supplier_order=NEW.previous_supplier_order AND COALESCE(x.receipt,'')=''
   AND COALESCE(x.supplier_merchant_order_no,x.order_id)=NEW.previous_merchant_order_no
   AND x.recovery_attempt+1=NEW.attempt AND x.lease_until<=NEW.created_at
   AND NEW.merchant_order_no=x.order_id||':retry:'||NEW.attempt
   AND o.version=NEW.expected_order_version AND o.status='failed'
   AND o.mode='live' AND o.is_test=0 AND o.payment_status='paid'
   AND o.refund_hold=0 AND o.refunded_cents=0 AND o.redemption_blocked=0
 )
BEGIN SELECT RAISE(ABORT,'xgift_recovery_conflict'); END;
-- These changes must commit together even when initiated by an operational client.
CREATE TRIGGER xgift_recovery_apply AFTER INSERT ON xgift_recovery_attempts
BEGIN
 UPDATE xgift_purchases SET supplier_merchant_order_no=NEW.merchant_order_no,
  recovery_attempt=NEW.attempt,state='pending',supplier_order=NULL,receipt=NULL,
  failure_code=NULL,lease_token=NULL,lease_until=0,attempted_at=NULL,next_check=0,
  updated_at=NEW.created_at WHERE order_id=NEW.order_id;
 UPDATE orders SET status='processing',version=version+1,updated_at=NEW.created_at,
  last_event_id=NEW.id WHERE id=NEW.order_id;
 INSERT INTO order_events(id,order_id,actor,action,from_status,to_status,note,order_version,created_at)
  SELECT NEW.id,id,NEW.actor,'xgift-recovery','failed','processing',NEW.note,version,NEW.created_at
  FROM orders WHERE id=NEW.order_id;
 INSERT INTO admin_audit(id,action,target_id,note,created_at)
  VALUES(NEW.id,'xgift-recovery',NEW.order_id,NEW.actor||': '||NEW.note||'; previous='||NEW.previous_supplier_order||'; merchant='||NEW.merchant_order_no,NEW.created_at);
END;
CREATE TRIGGER xgift_recovery_no_update BEFORE UPDATE ON xgift_recovery_attempts
BEGIN SELECT RAISE(ABORT,'immutable_xgift_recovery'); END;
CREATE TRIGGER xgift_recovery_no_delete BEFORE DELETE ON xgift_recovery_attempts
BEGIN SELECT RAISE(ABORT,'immutable_xgift_recovery'); END;
