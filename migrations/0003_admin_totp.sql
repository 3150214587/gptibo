-- Store only the credential fingerprint and highest accepted step, never the shared secret.
CREATE TABLE admin_totp_usage (
  credential_version TEXT PRIMARY KEY,
  last_step INTEGER NOT NULL
);
