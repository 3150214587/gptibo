CREATE TABLE partner_domains (
 partner_id text PRIMARY KEY REFERENCES partners(id),
 hostname text NOT NULL UNIQUE CHECK(hostname=lower(hostname)),
 verification text NOT NULL,
 provider_id text NOT NULL DEFAULT '',
 state text NOT NULL DEFAULT 'pending_dns' CHECK(state IN ('pending_dns','pending_certificate','active','error','deleting')),
 message text NOT NULL DEFAULT '',
 records jsonb NOT NULL DEFAULT '[]',
 checked_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE auth_challenges ADD COLUMN domain_scope text NOT NULL DEFAULT '';
ALTER TABLE auth_sessions ADD COLUMN domain_scope text NOT NULL DEFAULT '';
