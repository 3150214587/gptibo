-- One immutable channel/account/amount snapshot per real order.
CREATE TABLE payments (
  order_id TEXT PRIMARY KEY REFERENCES orders(id),
  provider TEXT NOT NULL,
  account TEXT NOT NULL,
  amount_cents INTEGER NOT NULL CHECK(amount_cents > 0),
  state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','paid','closed')),
  trade_no TEXT,
  qr TEXT,
  expires_at INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  lease_token TEXT,
  lease_until INTEGER NOT NULL DEFAULT 0,
  last_event_id TEXT,
  UNIQUE(provider,account,trade_no)
);
CREATE INDEX payments_pending ON payments(state,updated_at);
