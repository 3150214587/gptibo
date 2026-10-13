ALTER TABLE refund_cases ADD COLUMN lease_token text;
ALTER TABLE refund_cases ADD COLUMN lease_until bigint NOT NULL DEFAULT 0;
-- Stable idempotency receipts for administrative operations that touch many rows.
CREATE TABLE operation_receipts (
 id uuid PRIMARY KEY,
 actor text NOT NULL,
 action text NOT NULL,
 payload_hash text NOT NULL,
 result jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
