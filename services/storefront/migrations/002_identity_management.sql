ALTER TABLE partners ADD COLUMN email text;
ALTER TABLE partners ADD COLUMN version bigint NOT NULL DEFAULT 0 CHECK (version>=0);
ALTER TABLE partners ADD COLUMN auth_version bigint NOT NULL DEFAULT 0 CHECK (auth_version>=0);
ALTER TABLE partners ADD COLUMN created_at timestamptz NOT NULL DEFAULT now();
CREATE UNIQUE INDEX partners_email_unique ON partners(email) WHERE email IS NOT NULL;
ALTER TABLE support_contacts ADD COLUMN version bigint NOT NULL DEFAULT 0 CHECK (version>=0);
ALTER TABLE catalog_plans ADD COLUMN default_cost_cents bigint CHECK (default_cost_cents BETWEEN 1 AND 10000000);
ALTER TABLE catalog_plans ADD CONSTRAINT catalog_default_cost_bound CHECK(default_cost_cents<=price_cents);

CREATE TABLE auth_challenges (
    id uuid PRIMARY KEY,
    role text NOT NULL CHECK (role IN ('admin','partner','customer')),
    subject text NOT NULL,
    credential_version text NOT NULL,
    code_hash text NOT NULL,
    attempts integer NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 5),
    ready boolean NOT NULL DEFAULT false,
    used boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL DEFAULT now()+interval '10 minutes'
);
CREATE INDEX auth_challenges_subject ON auth_challenges(role,subject,created_at DESC);
CREATE INDEX auth_challenges_expiry ON auth_challenges(expires_at);
CREATE TABLE auth_sessions (
    token_hash text PRIMARY KEY,
    role text NOT NULL CHECK (role IN ('admin','partner','customer')),
    subject text NOT NULL,
    credential_version text NOT NULL,
    expires_at timestamptz NOT NULL DEFAULT now()+interval '3 days'
);
CREATE INDEX auth_sessions_expiry ON auth_sessions(expires_at);
CREATE TABLE auth_limits (
    key text PRIMARY KEY,
    attempts integer NOT NULL,
    reset_at timestamptz NOT NULL
);
CREATE TABLE admin_totp_usage (
    credential_version text PRIMARY KEY,
    last_step bigint NOT NULL
);
CREATE INDEX auth_limits_expiry ON auth_limits(reset_at);
CREATE TABLE audit_events (
    id uuid PRIMARY KEY,
    actor_role text NOT NULL,
    actor_id text NOT NULL,
    action text NOT NULL,
    target text NOT NULL,
    detail jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_events_time ON audit_events(created_at DESC,id);
