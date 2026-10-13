ALTER TABLE admin_totp_usage ADD COLUMN login_token TEXT;

CREATE TABLE admin_email_login_codes (
  id TEXT PRIMARY KEY,
  code_hash TEXT NOT NULL,
  credential_version TEXT NOT NULL,
  expires_at INTEGER NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 0,
  ready INTEGER NOT NULL DEFAULT 0 CHECK(ready IN (0,1)),
  consumed_by TEXT
);
CREATE INDEX admin_email_codes_expiry ON admin_email_login_codes(expires_at);
