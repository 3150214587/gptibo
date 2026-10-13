CREATE TABLE support_contacts (
  owner TEXT PRIMARY KEY,
  partner_id TEXT UNIQUE REFERENCES partners(id),
  wechat TEXT NOT NULL DEFAULT '' CHECK(length(wechat) <= 64),
  qq TEXT NOT NULL DEFAULT '' CHECK(length(qq) <= 20),
  email TEXT NOT NULL DEFAULT '' CHECK(length(email) <= 254),
  version INTEGER NOT NULL DEFAULT 0,
  change_id TEXT NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL DEFAULT 0,
  CHECK((owner = 'platform' AND partner_id IS NULL) OR (partner_id IS NOT NULL AND owner = partner_id))
);
INSERT INTO support_contacts(owner) VALUES ('platform');
