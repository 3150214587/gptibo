CREATE TABLE crm_customers (
  id TEXT PRIMARY KEY,
  partner_id TEXT REFERENCES partners(id),
  name TEXT NOT NULL,
  wechat TEXT NOT NULL DEFAULT '',
  qq TEXT NOT NULL DEFAULT '',
  email TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL DEFAULT '',
  note TEXT NOT NULL DEFAULT '',
  version INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  CHECK(wechat<>'' OR qq<>'' OR email<>'')
);
CREATE INDEX crm_customers_owner ON crm_customers(partner_id,created_at DESC);
CREATE TABLE crm_accounts (
  id TEXT PRIMARY KEY,
  customer_id TEXT NOT NULL REFERENCES crm_customers(id),
  account TEXT NOT NULL,
  note TEXT NOT NULL DEFAULT '',
  current_subscription_id TEXT REFERENCES crm_subscriptions(id),
  version INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  UNIQUE(customer_id,account)
);
CREATE INDEX crm_accounts_customer ON crm_accounts(customer_id);
CREATE TABLE crm_subscriptions (
  id TEXT PRIMARY KEY,
  account_id TEXT NOT NULL REFERENCES crm_accounts(id),
  previous_id TEXT REFERENCES crm_subscriptions(id),
  plan TEXT NOT NULL REFERENCES catalog_plans(id),
  plan_name TEXT NOT NULL,
  kind TEXT NOT NULL CHECK(kind IN ('open','renew','upgrade')),
  paid_cents INTEGER NOT NULL CHECK(paid_cents>=0 AND paid_cents<=10000000),
  opened_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL CHECK(expires_at>opened_at),
  note TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE INDEX crm_subscriptions_account ON crm_subscriptions(account_id,created_at DESC);
