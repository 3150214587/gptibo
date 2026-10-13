-- Immutable tenant, product, collection mode and exact create request per local order.
CREATE TABLE recharge_orders (
  order_id TEXT PRIMARY KEY REFERENCES orders(id),
  account TEXT NOT NULL,
  product_code TEXT NOT NULL,
  collection_mode TEXT NOT NULL CHECK(collection_mode IN ('platform_collect','agent_collect')),
  create_body TEXT NOT NULL,
  remote_id TEXT UNIQUE,
  payment_status TEXT NOT NULL DEFAULT 'pending',
  payment_url TEXT,
  expires_at INTEGER,
  attempted_at INTEGER,
  updated_at INTEGER NOT NULL,
  lease_token TEXT,
  lease_until INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX recharge_orders_due ON recharge_orders(updated_at);
-- No raw Session/Token/password is persisted. An unknown write must reuse the same body digest/key.
CREATE TABLE recharge_redemptions (
  id TEXT PRIMARY KEY,
  order_id TEXT NOT NULL REFERENCES recharge_orders(order_id),
  attempt_no INTEGER NOT NULL,
  request_hash TEXT NOT NULL,
  credential_mode TEXT NOT NULL CHECK(credential_mode IN ('session','access_token')),
  remote_id TEXT UNIQUE,
  status TEXT NOT NULL DEFAULT 'unknown' CHECK(status IN ('unknown','queued','running','succeeded','failed','cancelled','rejected')),
  fallback INTEGER NOT NULL DEFAULT 0 CHECK(fallback IN (0,1)),
  failure_code TEXT,
  account_email_masked TEXT,
  finished_at INTEGER,
  created_at INTEGER NOT NULL,
  UNIQUE(order_id,attempt_no)
);
CREATE TABLE recharge_refunds (
  case_id TEXT PRIMARY KEY REFERENCES refund_cases(id),
  order_id TEXT NOT NULL REFERENCES recharge_orders(order_id),
  create_body TEXT NOT NULL,
  remote_id TEXT UNIQUE,
  status TEXT NOT NULL DEFAULT 'unknown'
);
-- Persist only delivery identifiers and a digest, never the vendor payload.
CREATE TABLE recharge_webhooks (
  account TEXT NOT NULL,
  event_id TEXT NOT NULL,
  event_type TEXT NOT NULL,
  body_hash TEXT NOT NULL,
  received_at INTEGER NOT NULL,
  PRIMARY KEY(account,event_id)
);
