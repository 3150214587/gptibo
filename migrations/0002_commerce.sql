CREATE TABLE catalog_plans (
  id TEXT PRIMARY KEY,
  product_id TEXT NOT NULL,
  name TEXT NOT NULL,
  price_cents INTEGER NOT NULL CHECK(price_cents > 0 AND price_cents <= 10000000),
  enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)),
  renewal_only INTEGER NOT NULL DEFAULT 0 CHECK(renewal_only IN (0,1)),
  version INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL
);
INSERT INTO catalog_plans(id, product_id, name, price_cents, renewal_only, updated_at) VALUES
  ('plus','chatgpt','Plus',13900,0,0),
  ('pro-5x','chatgpt','Pro 5×',69900,0,0),
  ('pro-20x','chatgpt','Pro 20×',140000,1,0),
  ('codex-sms-once','codex-sms','单次接码',1000,0,0);

CREATE TABLE orders (
  id TEXT PRIMARY KEY,
  owner_hash TEXT NOT NULL,
  idempotency_key TEXT NOT NULL,
  plan TEXT NOT NULL REFERENCES catalog_plans(id),
  plan_name TEXT NOT NULL,
  price_cents INTEGER NOT NULL CHECK(price_cents > 0),
  mode TEXT NOT NULL DEFAULT 'demo' CHECK(mode IN ('demo','live')),
  payment_status TEXT NOT NULL DEFAULT 'unpaid' CHECK(payment_status IN ('unpaid','simulated','paid')),
  status TEXT NOT NULL CHECK(status IN ('pending','session','processing','success','failed','cancelled')),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  started_at INTEGER,
  outcome TEXT CHECK(outcome IN ('success','failed')),
  sms_phone TEXT,
  version INTEGER NOT NULL DEFAULT 0,
  last_event_id TEXT NOT NULL,
  UNIQUE(owner_hash, idempotency_key),
  CHECK(mode != 'demo' OR payment_status != 'paid'),
  CHECK(mode != 'live' OR payment_status != 'simulated')
);
CREATE INDEX orders_owner_created ON orders(owner_hash,created_at DESC);
CREATE INDEX orders_status_created ON orders(status,created_at DESC);
CREATE INDEX orders_created ON orders(created_at DESC);

CREATE TABLE order_events (
  id TEXT PRIMARY KEY,
  order_id TEXT NOT NULL REFERENCES orders(id),
  actor TEXT NOT NULL,
  action TEXT NOT NULL,
  from_status TEXT,
  to_status TEXT NOT NULL,
  note TEXT NOT NULL DEFAULT '',
  order_version INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  UNIQUE(order_id, order_version)
);
CREATE INDEX order_events_order ON order_events(order_id,created_at);

CREATE TABLE admin_sessions (
  token_hash TEXT PRIMARY KEY,
  credential_version TEXT NOT NULL,
  expires_at INTEGER NOT NULL
);
CREATE TABLE admin_login_limits (
  key TEXT PRIMARY KEY,
  attempts INTEGER NOT NULL DEFAULT 0,
  reset_at INTEGER NOT NULL
);
CREATE TABLE admin_audit (
  id TEXT PRIMARY KEY,
  action TEXT NOT NULL,
  target_id TEXT NOT NULL,
  note TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
