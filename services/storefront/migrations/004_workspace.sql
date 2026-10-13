CREATE TABLE partner_order_versions (
 partner_id text PRIMARY KEY REFERENCES partners(id),
 version bigint NOT NULL DEFAULT 0 CHECK(version>=0)
);
CREATE TABLE default_price_settings (
 id integer PRIMARY KEY CHECK(id=1),
 version bigint NOT NULL DEFAULT 0 CHECK(version>=0)
);
INSERT INTO default_price_settings(id) VALUES(1);
