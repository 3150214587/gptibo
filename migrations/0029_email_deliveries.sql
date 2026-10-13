CREATE TABLE email_deliveries (
  order_id TEXT PRIMARY KEY REFERENCES orders(id),
  kind TEXT NOT NULL CHECK(kind IN ('link','cdk')),
  value TEXT NOT NULL,
  instructions TEXT NOT NULL DEFAULT '',
  mail_body TEXT NOT NULL,
  request_key TEXT NOT NULL UNIQUE,
  status TEXT NOT NULL DEFAULT 'draft' CHECK(status IN ('draft','sending','unknown','sent')),
  version INTEGER NOT NULL DEFAULT 0,
  first_attempt_at INTEGER,
  sent_at INTEGER,
  provider_id TEXT,
  lease_token TEXT,
  lease_until INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
