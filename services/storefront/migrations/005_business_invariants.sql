-- Preserve the side effects and immutable supplier bindings from the D1 schema.
CREATE FUNCTION queue_recharge_reminder_fn() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.mode='live' AND NEW.is_test=0 AND NEW.payment_status='paid'
  AND OLD.payment_status<>'paid' AND NEW.status='session'
  AND NEW.plan<>'codex-sms-once' AND NEW.customer_email IS NOT NULL THEN
  INSERT INTO recharge_reminders(order_id,paid_at,next_attempt_at)
  VALUES(NEW.id,NEW.updated_at,NEW.updated_at+900000) ON CONFLICT DO NOTHING;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER queue_recharge_reminder AFTER UPDATE OF payment_status ON orders
 FOR EACH ROW EXECUTE FUNCTION queue_recharge_reminder_fn();

CREATE FUNCTION immutable_business_record() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'immutable_business_record' USING ERRCODE='23514'; END $$;
CREATE TRIGGER xgift_purchase_immutable BEFORE UPDATE OF username,recipient_id,product_code,points,credential_hash,supplier_backend ON xgift_purchases
 FOR EACH ROW EXECUTE FUNCTION immutable_business_record();
CREATE TRIGGER xgift_recovery_no_update BEFORE UPDATE OR DELETE ON xgift_recovery_attempts
 FOR EACH ROW EXECUTE FUNCTION immutable_business_record();

CREATE FUNCTION xgift_recovery_guard_fn() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 -- Lock the original order first, matching all application financial writers.
 PERFORM 1 FROM orders WHERE id=NEW.order_id FOR UPDATE;
 PERFORM 1 FROM xgift_purchases WHERE order_id=NEW.order_id FOR UPDATE;
 IF NOT EXISTS (
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
 ) THEN RAISE EXCEPTION 'xgift_recovery_conflict' USING ERRCODE='40001'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER xgift_recovery_guard BEFORE INSERT ON xgift_recovery_attempts
 FOR EACH ROW EXECUTE FUNCTION xgift_recovery_guard_fn();

CREATE FUNCTION xgift_recovery_apply_fn() RETURNS trigger LANGUAGE plpgsql AS $$
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
 INSERT INTO audit_events(id,actor_role,actor_id,action,target,detail,created_at)
 VALUES(NEW.id::uuid,'admin',NEW.actor,'xgift-recovery',NEW.order_id,
 jsonb_build_object('note',NEW.note,'previous',NEW.previous_supplier_order,'merchant',NEW.merchant_order_no),to_timestamp(NEW.created_at/1000.0));
 RETURN NEW;
END $$;
CREATE TRIGGER xgift_recovery_apply AFTER INSERT ON xgift_recovery_attempts
 FOR EACH ROW EXECUTE FUNCTION xgift_recovery_apply_fn();
