-- Anonymous storefront visitors, aggregated per local calendar day and shop.
-- The identifier is a SHA-256 digest of a random browser ID; no IP or user agent is retained.
CREATE TABLE visitor_days (
  day TEXT NOT NULL,
  shop_id TEXT NOT NULL,
  visitor_hash TEXT NOT NULL,
  page_views INTEGER NOT NULL DEFAULT 1,
  PRIMARY KEY (day, shop_id, visitor_hash)
);
CREATE INDEX visitor_days_shop_day ON visitor_days(shop_id, day);
CREATE INDEX visitor_days_day ON visitor_days(day);
