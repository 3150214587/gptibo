CREATE TABLE partner_announcements (
  partner_id TEXT PRIMARY KEY REFERENCES partners(id),
  enabled INTEGER NOT NULL DEFAULT 0 CHECK(enabled IN (0,1)),
  placement TEXT NOT NULL DEFAULT 'home' CHECK(placement IN ('home','product','both')),
  title TEXT NOT NULL DEFAULT '站点公告' CHECK(length(title) <= 80),
  content TEXT NOT NULL DEFAULT '' CHECK(length(content) <= 5000),
  version INTEGER NOT NULL DEFAULT 1,
  change_id TEXT NOT NULL,
  updated_at INTEGER NOT NULL,
  CHECK(enabled=0 OR (length(trim(title))>0 AND length(trim(content))>0))
);
