CREATE TABLE partners (
  id TEXT PRIMARY KEY,
  slug TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  email TEXT NOT NULL UNIQUE,
  active INTEGER NOT NULL DEFAULT 1 CHECK(active IN (0,1)),
  version INTEGER NOT NULL DEFAULT 0,
  auth_version INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE partner_prices (
  partner_id TEXT NOT NULL REFERENCES partners(id),
  plan TEXT NOT NULL REFERENCES catalog_plans(id),
  cost_cents INTEGER NOT NULL CHECK(cost_cents > 0 AND cost_cents <= 10000000),
  price_cents INTEGER NOT NULL CHECK(price_cents >= cost_cents AND price_cents <= 10000000),
  version INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(partner_id,plan)
);
CREATE TABLE partner_sessions (
  token_hash TEXT PRIMARY KEY,
  partner_id TEXT NOT NULL REFERENCES partners(id),
  auth_version INTEGER NOT NULL,
  credential_version TEXT NOT NULL,
  expires_at INTEGER NOT NULL
);
CREATE TABLE partner_login_codes (
  id TEXT PRIMARY KEY,
  partner_id TEXT NOT NULL REFERENCES partners(id),
  code_hash TEXT NOT NULL,
  auth_version INTEGER NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 0,
  expires_at INTEGER NOT NULL,
  used INTEGER NOT NULL DEFAULT 0 CHECK(used IN (0,1))
);
CREATE INDEX partner_codes_owner ON partner_login_codes(partner_id,expires_at);
CREATE TABLE partner_payouts (
  id TEXT PRIMARY KEY,
  partner_id TEXT NOT NULL REFERENCES partners(id),
  amount_cents INTEGER NOT NULL CHECK(amount_cents > 0),
  status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','paid','rejected')),
  recipient TEXT NOT NULL,
  note TEXT NOT NULL DEFAULT '',
  reference TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  handled_at INTEGER,
  version INTEGER NOT NULL DEFAULT 0,
  request_key TEXT NOT NULL,
  UNIQUE(partner_id,request_key)
);
CREATE UNIQUE INDEX partner_one_pending_payout ON partner_payouts(partner_id) WHERE status='pending';
ALTER TABLE orders ADD COLUMN partner_id TEXT REFERENCES partners(id);
ALTER TABLE orders ADD COLUMN partner_name TEXT;
ALTER TABLE orders ADD COLUMN cost_cents INTEGER CHECK(cost_cents > 0 AND cost_cents <= price_cents);
ALTER TABLE orders ADD COLUMN payout_id TEXT REFERENCES partner_payouts(id);
CREATE INDEX orders_partner_created ON orders(partner_id,created_at DESC);
CREATE INDEX orders_payout ON orders(payout_id);
