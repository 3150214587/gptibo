CREATE TABLE partner_branding (
 partner_id text PRIMARY KEY REFERENCES partners(id),
 name text NOT NULL DEFAULT '' CHECK(char_length(name)<=32),
 logo text NOT NULL DEFAULT '' CHECK(octet_length(logo)<=200),
 version bigint NOT NULL DEFAULT 0 CHECK(version>=0)
);
