-- Preserve existing registrations and cancellation tokens while extending the product constraint.
CREATE TABLE launch_interest_next (
  id TEXT PRIMARY KEY,
  product TEXT NOT NULL CHECK (product IN ('claude', 'grok', 'gmail')),
  email TEXT NOT NULL,
  token_hash TEXT NOT NULL UNIQUE,
  consent_version TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  UNIQUE (product, email)
);
INSERT INTO launch_interest_next SELECT id, product, email, token_hash, consent_version, created_at FROM launch_interest;
DROP TABLE launch_interest;
ALTER TABLE launch_interest_next RENAME TO launch_interest;
