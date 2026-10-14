CREATE TABLE manual_payment_invoices (
 id text PRIMARY KEY,
 order_id text NOT NULL REFERENCES orders(id),
 previous_session_hash text NOT NULL,
 previous_job_cipher text NOT NULL,
 status text NOT NULL CHECK(status IN ('creating','created','ready','unknown')),
 payload_cipher text NOT NULL DEFAULT '',
 actor_id text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(order_id,previous_session_hash)
);
CREATE INDEX manual_payment_invoices_order_created ON manual_payment_invoices(order_id,created_at DESC);
