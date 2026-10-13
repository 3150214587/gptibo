ALTER TABLE orders ADD COLUMN customer_email TEXT;
ALTER TABLE orders ADD COLUMN refunded_cents INTEGER NOT NULL DEFAULT 0 CHECK(refunded_cents>=0 AND refunded_cents<=price_cents);
ALTER TABLE orders ADD COLUMN refund_hold INTEGER NOT NULL DEFAULT 0 CHECK(refund_hold IN (0,1));
CREATE INDEX orders_customer_email ON orders(customer_email,created_at DESC);
ALTER TABLE partner_payouts ADD COLUMN reviewed_at INTEGER;
ALTER TABLE partner_payouts ADD COLUMN review_token TEXT;
ALTER TABLE partner_payouts ADD COLUMN gross_cents INTEGER NOT NULL DEFAULT 0;
ALTER TABLE partner_payouts ADD COLUMN deduction_cents INTEGER NOT NULL DEFAULT 0;
UPDATE partner_payouts SET gross_cents=amount_cents;

CREATE TABLE refund_cases (
  id TEXT PRIMARY KEY,
  order_id TEXT NOT NULL REFERENCES orders(id),
  request_key TEXT NOT NULL,
  reason TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','approved','refunded','rejected','cancelled')),
  requested_cents INTEGER NOT NULL CHECK(requested_cents>0),
  approved_cents INTEGER CHECK(approved_cents>0),
  note TEXT NOT NULL DEFAULT '',
  reference TEXT NOT NULL DEFAULT '',
  version INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  last_event_id TEXT NOT NULL,
  UNIQUE(order_id,request_key)
);
CREATE UNIQUE INDEX refund_one_open ON refund_cases(order_id) WHERE status IN ('pending','approved');
CREATE INDEX refund_status_created ON refund_cases(status,created_at DESC);
CREATE TABLE refund_events (
  id TEXT PRIMARY KEY,
  case_id TEXT NOT NULL REFERENCES refund_cases(id),
  status TEXT NOT NULL,
  note TEXT NOT NULL,
  actor TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX refund_events_case ON refund_events(case_id,created_at);
CREATE TABLE partner_debits (
  id TEXT PRIMARY KEY,
  partner_id TEXT NOT NULL REFERENCES partners(id),
  refund_id TEXT NOT NULL UNIQUE REFERENCES refund_cases(id),
  amount_cents INTEGER NOT NULL CHECK(amount_cents>0),
  payout_id TEXT REFERENCES partner_payouts(id),
  created_at INTEGER NOT NULL
);
CREATE INDEX partner_debits_available ON partner_debits(partner_id,payout_id);

CREATE TABLE customer_login_codes (
  id TEXT PRIMARY KEY,
  email TEXT NOT NULL,
  code_hash TEXT NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 0,
  used INTEGER NOT NULL DEFAULT 0,
  expires_at INTEGER NOT NULL
);
CREATE INDEX customer_codes_email ON customer_login_codes(email);
CREATE TABLE customer_email_sessions (
  token_hash TEXT PRIMARY KEY,
  email TEXT NOT NULL,
  credential_version TEXT NOT NULL,
  expires_at INTEGER NOT NULL
);

CREATE TABLE partner_payout_debit_items (payout_id TEXT NOT NULL REFERENCES partner_payouts(id), debit_id TEXT NOT NULL REFERENCES partner_debits(id), amount_cents INTEGER NOT NULL, PRIMARY KEY(payout_id,debit_id));
