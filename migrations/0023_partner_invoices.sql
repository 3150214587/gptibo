CREATE TABLE partner_invoices (
  id TEXT PRIMARY KEY,
  partner_id TEXT NOT NULL REFERENCES partners(id),
  title TEXT NOT NULL, tax_number TEXT NOT NULL DEFAULT '',
  invoice_type TEXT NOT NULL CHECK(invoice_type IN ('ordinary','special')),
  email TEXT NOT NULL, address TEXT NOT NULL DEFAULT '', phone TEXT NOT NULL DEFAULT '',
  bank_name TEXT NOT NULL DEFAULT '', bank_account TEXT NOT NULL DEFAULT '', note TEXT NOT NULL DEFAULT '',
  base_cents INTEGER NOT NULL CHECK(base_cents BETWEEN 10 AND 10000000),
  fee_cents INTEGER NOT NULL CHECK(fee_cents = CAST((base_cents * 5 + 50) / 100 AS INTEGER)),
  include_fee INTEGER NOT NULL CHECK(include_fee IN (0,1)),
  invoice_cents INTEGER NOT NULL CHECK(invoice_cents = base_cents + include_fee * fee_cents),
  status TEXT NOT NULL DEFAULT 'awaiting_payment' CHECK(status IN ('awaiting_payment','awaiting_submission','submitted','completed')),
  paid_at INTEGER, submitted_at INTEGER, completed_at INTEGER,
  platform_mode TEXT NOT NULL DEFAULT 'manual', platform_reference TEXT NOT NULL DEFAULT '', admin_note TEXT NOT NULL DEFAULT '',
  version INTEGER NOT NULL DEFAULT 0,
  lease_token TEXT, lease_until INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE INDEX partner_invoices_owner ON partner_invoices(partner_id,created_at DESC);
CREATE INDEX partner_invoices_status ON partner_invoices(status,created_at DESC);

CREATE TABLE invoice_payments (
  id TEXT PRIMARY KEY, invoice_id TEXT NOT NULL REFERENCES partner_invoices(id),
  provider TEXT NOT NULL, account TEXT NOT NULL,
  amount_cents INTEGER NOT NULL CHECK(amount_cents > 0),
  state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','paid','closed')),
  qr TEXT, trade_no TEXT, expires_at INTEGER NOT NULL, notified_at INTEGER,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX invoice_payment_pending ON invoice_payments(invoice_id) WHERE state='pending';
CREATE UNIQUE INDEX invoice_payment_trade ON invoice_payments(provider,account,trade_no) WHERE trade_no IS NOT NULL;
CREATE INDEX invoice_payment_reconcile ON invoice_payments(state,updated_at);

CREATE TABLE invoice_events (
  id TEXT PRIMARY KEY, invoice_id TEXT NOT NULL REFERENCES partner_invoices(id),
  actor TEXT NOT NULL, action TEXT NOT NULL, note TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL
);
CREATE INDEX invoice_event_history ON invoice_events(invoice_id,created_at);
