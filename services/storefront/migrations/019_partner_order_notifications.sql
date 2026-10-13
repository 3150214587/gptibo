CREATE TABLE partner_notification_settings (
 partner_id text PRIMARY KEY REFERENCES partners(id),
 order_completed_email boolean NOT NULL DEFAULT false,
 version bigint NOT NULL DEFAULT 0 CHECK(version>=0),
 updated_at bigint NOT NULL DEFAULT 0
);

CREATE TABLE partner_order_notifications (
 order_id text PRIMARY KEY REFERENCES orders(id),
 partner_id text NOT NULL REFERENCES partners(id),
 preference_version bigint NOT NULL,
 recipient text NOT NULL,
 partner_name text NOT NULL,
 product_id text NOT NULL,
 plan_name text NOT NULL,
 price_cents bigint NOT NULL,
 customer_email text NOT NULL,
 completed_at bigint NOT NULL,
 message_json text,
 first_attempt_at bigint,
 delivered_at bigint,
 cancelled_at bigint,
 attempt_count bigint NOT NULL DEFAULT 0,
 next_attempt_at bigint NOT NULL DEFAULT 0,
 lock_token text,
 locked_until bigint NOT NULL DEFAULT 0
);
CREATE INDEX partner_order_notifications_due ON partner_order_notifications(next_attempt_at)
 WHERE delivered_at IS NULL AND cancelled_at IS NULL;

-- Enabling notifications never backfills historical orders. The outbox and
-- completion commit together, including completions from every supplier.
CREATE FUNCTION queue_partner_order_notification_fn() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.status='success' AND OLD.status<>'success' AND NEW.mode='live'
  AND NEW.payment_status='paid' AND NEW.is_test=0
  AND NEW.refund_hold=0 AND NEW.refunded_cents=0 THEN
  INSERT INTO partner_order_notifications(order_id,partner_id,preference_version,recipient,partner_name,product_id,plan_name,price_cents,customer_email,completed_at)
  SELECT NEW.id,p.id,n.version,p.email,p.name,COALESCE(c.product_id,''),NEW.plan_name,NEW.price_cents,COALESCE(NEW.customer_email,''),(extract(epoch FROM clock_timestamp())*1000)::bigint
  FROM partners p JOIN partner_notification_settings n ON n.partner_id=p.id
  LEFT JOIN catalog_plans c ON c.id=NEW.plan
  WHERE p.id=NEW.partner_id AND p.active AND p.email<>'' AND n.order_completed_email
  ON CONFLICT(order_id) DO NOTHING;
  IF FOUND THEN PERFORM pg_notify('storefront_work',current_schema()); END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER queue_partner_order_notification AFTER UPDATE OF status ON orders
 FOR EACH ROW EXECUTE FUNCTION queue_partner_order_notification_fn();
