CREATE TABLE partner_payout_accounts (
    partner_id text PRIMARY KEY REFERENCES partners(id),
    phone text NOT NULL DEFAULT '',
    name text NOT NULL DEFAULT '',
    version bigint NOT NULL DEFAULT 0 CHECK (version >= 0),
    CHECK ((phone = '' AND name = '') OR
        (phone ~ '^1[3-9][0-9]{9}$' AND char_length(name) BETWEEN 1 AND 50 AND name = btrim(name)))
);
