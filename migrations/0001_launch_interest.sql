CREATE TABLE launch_interest (
  id TEXT PRIMARY KEY,
  product TEXT NOT NULL CHECK (product IN ('claude', 'grok')),
  email TEXT NOT NULL,
  token_hash TEXT NOT NULL UNIQUE,
  consent_version TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  UNIQUE (product, email)
);
