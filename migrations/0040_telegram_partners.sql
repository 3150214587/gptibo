-- Private application data. Never joined into public storefront contacts.
CREATE TABLE telegram_partner_sessions (
  user_id INTEGER PRIMARY KEY,
  group_id INTEGER,
  step TEXT NOT NULL DEFAULT 'idle',
  wechat TEXT NOT NULL DEFAULT '',
  qq TEXT NOT NULL DEFAULT '',
  email TEXT NOT NULL DEFAULT '',
  expires_at INTEGER NOT NULL DEFAULT 0,
  last_update_id INTEGER NOT NULL DEFAULT -1,
  lock_token TEXT,
  lock_until INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE telegram_partner_updates (
  update_id INTEGER PRIMARY KEY,
  lock_token TEXT,
  lock_until INTEGER NOT NULL DEFAULT 0,
  reply TEXT,
  created_at INTEGER NOT NULL
);
CREATE INDEX telegram_partner_updates_created ON telegram_partner_updates(created_at);
CREATE TABLE telegram_partner_applications (
  id TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL UNIQUE,
  group_id INTEGER NOT NULL,
  partner_id TEXT NOT NULL UNIQUE REFERENCES partners(id),
  wechat TEXT NOT NULL,
  qq TEXT NOT NULL,
  email TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  mail_status TEXT NOT NULL DEFAULT 'pending' CHECK(mail_status IN ('pending','sent','failed')),
  mail_attempts INTEGER NOT NULL DEFAULT 0,
  next_attempt_at INTEGER NOT NULL,
  notice_sent INTEGER NOT NULL DEFAULT 0 CHECK(notice_sent IN (0,1)),
  lock_token TEXT,
  lock_until INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX telegram_partner_mail_due ON telegram_partner_applications(mail_status,next_attempt_at);
