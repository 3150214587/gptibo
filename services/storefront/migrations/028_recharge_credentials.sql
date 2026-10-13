-- Credentials are encrypted with an order/attempt-bound AEAD key. Never returned
-- by ordinary order views or retained in logs, events or request metadata.
CREATE TABLE recharge_credentials (
  attempt_id text PRIMARY KEY REFERENCES recharge_redemptions(id),
  order_id text NOT NULL REFERENCES orders(id),
  ciphertext bytea NOT NULL,
  created_at bigint NOT NULL
);
CREATE INDEX recharge_credentials_order ON recharge_credentials(order_id);
