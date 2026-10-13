-- Business schema translated from the existing D1 migrations. No production data or product seeds.

CREATE TABLE accounting_costs (
  plan TEXT PRIMARY KEY,
  cost_cents bigint CHECK(cost_cents IS NULL OR (cost_cents >= 0 AND cost_cents <= 10000000))
);

CREATE TABLE accounting_items (
  settlement_id TEXT NOT NULL,
  order_id TEXT NOT NULL,
  plan TEXT NOT NULL,
  plan_name TEXT NOT NULL,
  kind TEXT NOT NULL CHECK(kind IN ('order','refund')),
  sale_cents bigint NOT NULL,
  cost_cents bigint NOT NULL,
  refund_cents bigint NOT NULL,
  created_at bigint NOT NULL,
  PRIMARY KEY(settlement_id,order_id)
);

CREATE TABLE accounting_settlements (
  id TEXT PRIMARY KEY,
  base_version bigint NOT NULL UNIQUE,
  previous_id TEXT,
  from_at bigint NOT NULL,
  closed_at bigint NOT NULL,
  order_count bigint NOT NULL DEFAULT 0,
  adjustment_count bigint NOT NULL DEFAULT 0,
  sale_cents bigint NOT NULL DEFAULT 0,
  cost_cents bigint NOT NULL DEFAULT 0,
  refund_cents bigint NOT NULL DEFAULT 0,
  profit_cents bigint NOT NULL DEFAULT 0
);

CREATE TABLE accounting_state (
  id bigint PRIMARY KEY CHECK(id=1),
  version bigint NOT NULL DEFAULT 0,
  last_settlement_id TEXT
);

CREATE TABLE crm_accounts (
  id TEXT PRIMARY KEY,
  customer_id TEXT NOT NULL,
  account TEXT NOT NULL,
  note TEXT NOT NULL DEFAULT '',
  current_subscription_id TEXT,
  version bigint NOT NULL DEFAULT 0,
  created_at bigint NOT NULL,
  updated_at bigint NOT NULL,
  UNIQUE(customer_id,account)
);

CREATE TABLE crm_customers (
  id TEXT PRIMARY KEY,
  partner_id TEXT,
  name TEXT NOT NULL,
  wechat TEXT NOT NULL DEFAULT '',
  qq TEXT NOT NULL DEFAULT '',
  email TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL DEFAULT '',
  note TEXT NOT NULL DEFAULT '',
  version bigint NOT NULL DEFAULT 0,
  created_at bigint NOT NULL,
  updated_at bigint NOT NULL,
  CHECK(wechat<>'' OR qq<>'' OR email<>'')
);

CREATE TABLE crm_subscriptions (
  id TEXT PRIMARY KEY,
  account_id TEXT NOT NULL,
  previous_id TEXT,
  plan TEXT NOT NULL,
  plan_name TEXT NOT NULL,
  kind TEXT NOT NULL CHECK(kind IN ('open','renew','upgrade')),
  paid_cents bigint NOT NULL CHECK(paid_cents>=0 AND paid_cents<=10000000),
  opened_at bigint NOT NULL,
  expires_at bigint NOT NULL CHECK(expires_at>opened_at),
  note TEXT NOT NULL DEFAULT '',
  created_at bigint NOT NULL
);

CREATE TABLE email_deliveries (
  order_id TEXT PRIMARY KEY,
  kind TEXT NOT NULL CHECK(kind IN ('link','cdk')),
  value TEXT NOT NULL,
  instructions TEXT NOT NULL DEFAULT '',
  mail_body TEXT NOT NULL,
  request_key TEXT NOT NULL UNIQUE,
  status TEXT NOT NULL DEFAULT 'draft' CHECK(status IN ('draft','sending','unknown','sent')),
  version bigint NOT NULL DEFAULT 0,
  first_attempt_at bigint,
  sent_at bigint,
  provider_id TEXT,
  lease_token TEXT,
  lease_until bigint NOT NULL DEFAULT 0,
  created_at bigint NOT NULL,
  updated_at bigint NOT NULL
);

CREATE TABLE invoice_events (
  id TEXT PRIMARY KEY, invoice_id TEXT NOT NULL,
  actor TEXT NOT NULL, action TEXT NOT NULL, note TEXT NOT NULL DEFAULT '', created_at bigint NOT NULL
);

CREATE TABLE invoice_payments (
  id TEXT PRIMARY KEY, invoice_id TEXT NOT NULL,
  provider TEXT NOT NULL, account TEXT NOT NULL,
  amount_cents bigint NOT NULL CHECK(amount_cents > 0),
  state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','paid','closed')),
  qr TEXT, trade_no TEXT, expires_at bigint NOT NULL, notified_at bigint,
  created_at bigint NOT NULL, updated_at bigint NOT NULL
);

CREATE TABLE account_supplier_orders (
  order_id TEXT PRIMARY KEY,
  goods_id bigint NOT NULL,
  goods_name TEXT NOT NULL,
  api_base TEXT NOT NULL,
  app_id TEXT NOT NULL,
  max_cost_cents bigint NOT NULL CHECK(max_cost_cents>=0),
  instructions TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT 'prepared' CHECK(state IN ('prepared','creating','unknown','purchased','rejected')),
  supplier_order TEXT UNIQUE,
  actual_cost_cents bigint,
  cards TEXT,
  attempted_at bigint,
  lease_token TEXT,
  lease_until bigint NOT NULL DEFAULT 0,
  updated_at bigint NOT NULL
);

CREATE TABLE "launch_interest" (
  id TEXT PRIMARY KEY,
  product TEXT NOT NULL CHECK (product IN ('claude', 'grok', 'gmail')),
  email TEXT NOT NULL,
  token_hash TEXT NOT NULL UNIQUE,
  consent_version TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (to_char(clock_timestamp() at time zone 'UTC','YYYY-MM-DD"T"HH24:MI:SS.MS"Z"')),
  UNIQUE (product, email)
);

CREATE TABLE delivery_supplier_orders (
  order_id TEXT PRIMARY KEY,
  service_id TEXT NOT NULL,
  credential_hash TEXT NOT NULL,
  request_key TEXT NOT NULL UNIQUE,
  request_body TEXT NOT NULL,
  max_usd_cents bigint NOT NULL CHECK(max_usd_cents>0),
  cny_per_usd REAL NOT NULL CHECK(cny_per_usd>0),
  instructions TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT 'prepared' CHECK(state IN ('prepared','creating','unknown','pending','purchased','rejected')),
  supplier_order TEXT UNIQUE,
  actual_usd_cents bigint,
  actual_cost_cents bigint,
  value TEXT,
  attempted_at bigint,
  lease_token TEXT,
  lease_until bigint NOT NULL DEFAULT 0,
  next_check_at bigint NOT NULL DEFAULT 0,
  updated_at bigint NOT NULL
);

CREATE TABLE order_events (
  id TEXT PRIMARY KEY,
  order_id TEXT NOT NULL,
  actor TEXT NOT NULL,
  action TEXT NOT NULL,
  from_status TEXT,
  to_status TEXT NOT NULL,
  note TEXT NOT NULL DEFAULT '',
  order_version bigint NOT NULL,
  created_at bigint NOT NULL,
  UNIQUE(order_id, order_version)
);

CREATE TABLE orders (
  id TEXT PRIMARY KEY,
  owner_hash TEXT NOT NULL,
  idempotency_key TEXT NOT NULL,
  plan TEXT NOT NULL,
  plan_name TEXT NOT NULL,
  price_cents bigint NOT NULL CHECK(price_cents > 0),
  mode TEXT NOT NULL DEFAULT 'demo' CHECK(mode IN ('demo','live')),
  payment_status TEXT NOT NULL DEFAULT 'unpaid' CHECK(payment_status IN ('unpaid','simulated','paid')),
  status TEXT NOT NULL CHECK(status IN ('pending','session','processing','success','failed','cancelled')),
  created_at bigint NOT NULL,
  updated_at bigint NOT NULL,
  started_at bigint,
  outcome TEXT CHECK(outcome IN ('success','failed')),
  sms_phone TEXT,
  version bigint NOT NULL DEFAULT 0,
  last_event_id TEXT NOT NULL, partner_id TEXT, partner_name TEXT, cost_cents bigint CHECK(cost_cents > 0 AND cost_cents <= price_cents), payout_id TEXT, customer_email TEXT, refunded_cents bigint NOT NULL DEFAULT 0 CHECK(refunded_cents>=0 AND refunded_cents<=price_cents), refund_hold bigint NOT NULL DEFAULT 0 CHECK(refund_hold IN (0,1)), is_test bigint NOT NULL DEFAULT 0 CHECK(is_test IN (0,1)), redemption_blocked bigint NOT NULL DEFAULT 0 CHECK (redemption_blocked IN (0, 1)), purchase_note TEXT NOT NULL DEFAULT '',
  UNIQUE(owner_hash, idempotency_key),
  CHECK(mode != 'demo' OR payment_status != 'paid'),
  CHECK(mode != 'live' OR payment_status != 'simulated')
);

CREATE TABLE partner_debits (
  id TEXT PRIMARY KEY,
  partner_id TEXT NOT NULL,
  refund_id TEXT NOT NULL UNIQUE,
  amount_cents bigint NOT NULL CHECK(amount_cents>0),
  payout_id TEXT,
  created_at bigint NOT NULL
);

CREATE TABLE partner_invoices (
  id TEXT PRIMARY KEY,
  partner_id TEXT NOT NULL,
  title TEXT NOT NULL, tax_number TEXT NOT NULL DEFAULT '',
  invoice_type TEXT NOT NULL CHECK(invoice_type IN ('ordinary','special')),
  email TEXT NOT NULL, address TEXT NOT NULL DEFAULT '', phone TEXT NOT NULL DEFAULT '',
  bank_name TEXT NOT NULL DEFAULT '', bank_account TEXT NOT NULL DEFAULT '', note TEXT NOT NULL DEFAULT '',
  base_cents bigint NOT NULL CHECK(base_cents BETWEEN 10 AND 10000000),
  fee_cents bigint NOT NULL CHECK(fee_cents = CAST((base_cents * 5 + 50) / 100 AS bigint)),
  include_fee bigint NOT NULL CHECK(include_fee IN (0,1)),
  invoice_cents bigint NOT NULL CHECK(invoice_cents = base_cents + include_fee * fee_cents),
  status TEXT NOT NULL DEFAULT 'awaiting_payment' CHECK(status IN ('awaiting_payment','awaiting_submission','submitted','completed')),
  paid_at bigint, submitted_at bigint, completed_at bigint,
  platform_mode TEXT NOT NULL DEFAULT 'manual', platform_reference TEXT NOT NULL DEFAULT '', admin_note TEXT NOT NULL DEFAULT '',
  version bigint NOT NULL DEFAULT 0,
  lease_token TEXT, lease_until bigint NOT NULL DEFAULT 0,
  created_at bigint NOT NULL, updated_at bigint NOT NULL
);

CREATE TABLE partner_payout_debit_items (payout_id TEXT NOT NULL, debit_id TEXT NOT NULL, amount_cents bigint NOT NULL, PRIMARY KEY(payout_id,debit_id));

CREATE TABLE partner_payout_items (
  payout_id TEXT NOT NULL,
  order_id TEXT NOT NULL,
  profit_cents bigint NOT NULL CHECK(profit_cents > 0),
  PRIMARY KEY(payout_id,order_id)
);

CREATE TABLE partner_payouts (
  id TEXT PRIMARY KEY,
  partner_id TEXT NOT NULL,
  amount_cents bigint NOT NULL CHECK(amount_cents > 0),
  status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','paid','rejected')),
  recipient TEXT NOT NULL,
  note TEXT NOT NULL DEFAULT '',
  reference TEXT NOT NULL DEFAULT '',
  created_at bigint NOT NULL,
  handled_at bigint,
  version bigint NOT NULL DEFAULT 0,
  request_key TEXT NOT NULL, reviewed_at bigint, review_token TEXT, gross_cents bigint NOT NULL DEFAULT 0, deduction_cents bigint NOT NULL DEFAULT 0, cooldown_exempt bigint NOT NULL DEFAULT 0 CHECK(cooldown_exempt IN (0,1)),
  UNIQUE(partner_id,request_key)
);

CREATE TABLE payments (
  order_id TEXT PRIMARY KEY,
  provider TEXT NOT NULL,
  account TEXT NOT NULL,
  amount_cents bigint NOT NULL CHECK(amount_cents > 0),
  state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','paid','closed')),
  trade_no TEXT,
  qr TEXT,
  expires_at bigint NOT NULL,
  created_at bigint NOT NULL,
  updated_at bigint NOT NULL,
  lease_token TEXT,
  lease_until bigint NOT NULL DEFAULT 0,
  last_event_id TEXT, notified_at bigint,
  UNIQUE(provider,account,trade_no)
);

CREATE TABLE payout_notifications (
  payout_id TEXT PRIMARY KEY,
  created_at bigint NOT NULL,
  delivered_at bigint,
  next_attempt_at bigint NOT NULL DEFAULT 0,
  locked_until bigint NOT NULL DEFAULT 0,
  lock_token TEXT NOT NULL DEFAULT '',
  attempt_count bigint NOT NULL DEFAULT 0
);

CREATE TABLE recharge_reminders (
  order_id TEXT PRIMARY KEY,
  paid_at bigint NOT NULL,
  next_attempt_at bigint NOT NULL,
  first_attempt_at bigint,
  attempt_count bigint NOT NULL DEFAULT 0,
  delivered_at bigint,
  cancelled_at bigint,
  message_json TEXT,
  recipient TEXT,
  locked_until bigint NOT NULL DEFAULT 0,
  lock_token TEXT NOT NULL DEFAULT ''
);

CREATE TABLE refund_cases (
  id TEXT PRIMARY KEY,
  order_id TEXT NOT NULL,
  request_key TEXT NOT NULL,
  reason TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','approved','refunded','rejected','cancelled')),
  requested_cents bigint NOT NULL CHECK(requested_cents>0),
  approved_cents bigint CHECK(approved_cents>0),
  note TEXT NOT NULL DEFAULT '',
  reference TEXT NOT NULL DEFAULT '',
  version bigint NOT NULL DEFAULT 0,
  created_at bigint NOT NULL,
  updated_at bigint NOT NULL,
  last_event_id TEXT NOT NULL,
  UNIQUE(order_id,request_key)
);

CREATE TABLE refund_events (
  id TEXT PRIMARY KEY,
  case_id TEXT NOT NULL,
  status TEXT NOT NULL,
  note TEXT NOT NULL,
  actor TEXT NOT NULL,
  created_at bigint NOT NULL
);

CREATE TABLE telegram_partner_applications (
  id TEXT PRIMARY KEY,
  user_id bigint NOT NULL UNIQUE,
  group_id bigint NOT NULL,
  partner_id TEXT NOT NULL UNIQUE,
  wechat TEXT NOT NULL,
  qq TEXT NOT NULL,
  email TEXT NOT NULL,
  created_at bigint NOT NULL,
  mail_status TEXT NOT NULL DEFAULT 'pending' CHECK(mail_status IN ('pending','sent','failed')),
  mail_attempts bigint NOT NULL DEFAULT 0,
  next_attempt_at bigint NOT NULL,
  notice_sent bigint NOT NULL DEFAULT 0 CHECK(notice_sent IN (0,1)),
  lock_token TEXT,
  lock_until bigint NOT NULL DEFAULT 0
);

CREATE TABLE telegram_partner_sessions (
  user_id bigint PRIMARY KEY,
  group_id bigint,
  step TEXT NOT NULL DEFAULT 'idle',
  wechat TEXT NOT NULL DEFAULT '',
  qq TEXT NOT NULL DEFAULT '',
  email TEXT NOT NULL DEFAULT '',
  expires_at bigint NOT NULL DEFAULT 0,
  last_update_id bigint NOT NULL DEFAULT -1,
  lock_token TEXT,
  lock_until bigint NOT NULL DEFAULT 0
);

CREATE TABLE telegram_partner_updates (
  update_id bigint PRIMARY KEY,
  lock_token TEXT,
  lock_until bigint NOT NULL DEFAULT 0,
  reply TEXT,
  created_at bigint NOT NULL
);

CREATE TABLE recharge_orders (
  order_id TEXT PRIMARY KEY,
  account TEXT NOT NULL,
  product_code TEXT NOT NULL,
  collection_mode TEXT NOT NULL CHECK(collection_mode IN ('platform_collect','agent_collect')),
  create_body TEXT NOT NULL,
  remote_id TEXT UNIQUE,
  payment_status TEXT NOT NULL DEFAULT 'pending',
  payment_url TEXT,
  expires_at bigint,
  attempted_at bigint,
  updated_at bigint NOT NULL,
  lease_token TEXT,
  lease_until bigint NOT NULL DEFAULT 0
, supply_cents bigint CHECK(supply_cents>=0), merchant_margin_cents bigint CHECK(merchant_margin_cents>=0), refunded_cents bigint NOT NULL DEFAULT 0 CHECK(refunded_cents>=0), financial_hold bigint NOT NULL DEFAULT 1 CHECK(financial_hold IN (0,1)), notify_version bigint NOT NULL DEFAULT 0, checked_notify_version bigint NOT NULL DEFAULT 0, payment_qr TEXT, reported_margin_cents bigint, verified_at bigint);

CREATE TABLE recharge_redemptions (
  id TEXT PRIMARY KEY,
  order_id TEXT NOT NULL,
  attempt_no bigint NOT NULL,
  request_hash TEXT NOT NULL,
  credential_mode TEXT NOT NULL CHECK(credential_mode IN ('session','access_token')),
  remote_id TEXT UNIQUE,
  status TEXT NOT NULL DEFAULT 'unknown' CHECK(status IN ('unknown','queued','running','succeeded','failed','cancelled','rejected')),
  fallback bigint NOT NULL DEFAULT 0 CHECK(fallback IN (0,1)),
  failure_code TEXT,
  account_email_masked TEXT,
  finished_at bigint,
  created_at bigint NOT NULL, progress_stage TEXT, progress_version bigint NOT NULL DEFAULT 0, progress_updated_at bigint, error_category TEXT, result_code TEXT,
  UNIQUE(order_id,attempt_no)
);

CREATE TABLE recharge_refunds (
  case_id TEXT PRIMARY KEY,
  order_id TEXT NOT NULL,
  create_body TEXT NOT NULL,
  remote_id TEXT UNIQUE,
  status TEXT NOT NULL DEFAULT 'unknown'
, updated_at bigint NOT NULL DEFAULT 0);

CREATE TABLE recharge_webhooks (
  account TEXT NOT NULL,
  event_id TEXT NOT NULL,
  event_type TEXT NOT NULL,
  body_hash TEXT NOT NULL,
  received_at bigint NOT NULL,
  PRIMARY KEY(account,event_id)
);

CREATE TABLE visitor_days (
  day TEXT NOT NULL,
  shop_id TEXT NOT NULL,
  visitor_hash TEXT NOT NULL,
  page_views bigint NOT NULL DEFAULT 1,
  PRIMARY KEY (day, shop_id, visitor_hash)
);

CREATE TABLE xgift_preflight_backends (
 token TEXT PRIMARY KEY,
 supplier_backend TEXT NOT NULL CHECK(supplier_backend IN ('legacy','platform'))
);

CREATE TABLE xgift_preflights (
  token TEXT PRIMARY KEY, owner_hash TEXT NOT NULL, username TEXT NOT NULL, recipient_id TEXT NOT NULL,
  product_code TEXT NOT NULL, points bigint NOT NULL, expires_at bigint NOT NULL
);

CREATE TABLE xgift_purchases (
  order_id TEXT PRIMARY KEY,
  username TEXT NOT NULL, recipient_id TEXT NOT NULL, product_code TEXT NOT NULL,
  points bigint NOT NULL CHECK(points>0), credential_hash TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT 'prepared' CHECK(state IN ('prepared','creating','unknown','pending','succeeded','failed')),
  supplier_order TEXT UNIQUE, receipt TEXT, failure_code TEXT,
  lease_token TEXT, lease_until bigint NOT NULL DEFAULT 0, next_check bigint NOT NULL DEFAULT 0,
  attempted_at bigint, updated_at bigint NOT NULL
, supplier_backend TEXT NOT NULL DEFAULT 'legacy' CHECK(supplier_backend IN ('legacy','platform')), supplier_merchant_order_no TEXT, recovery_attempt bigint NOT NULL DEFAULT 0);

CREATE TABLE xgift_recovery_attempts (
 id TEXT PRIMARY KEY,
 order_id TEXT NOT NULL,
 attempt bigint NOT NULL CHECK(attempt>0),
 merchant_order_no TEXT NOT NULL UNIQUE,
 previous_merchant_order_no TEXT NOT NULL,
 previous_supplier_order TEXT NOT NULL,
 previous_failure_code TEXT NOT NULL CHECK(previous_failure_code='card_wallet_insufficient'),
 expected_order_version bigint NOT NULL,
 actor TEXT NOT NULL,
 note TEXT NOT NULL CHECK(length(note) BETWEEN 1 AND 500),
 created_at bigint NOT NULL,
 UNIQUE(order_id,attempt)
);

ALTER TABLE accounting_costs ADD FOREIGN KEY (plan) REFERENCES catalog_plans(id) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE accounting_items ADD FOREIGN KEY (order_id) REFERENCES orders(id) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE accounting_items ADD FOREIGN KEY (settlement_id) REFERENCES accounting_settlements(id) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE accounting_settlements ADD FOREIGN KEY (previous_id) REFERENCES accounting_settlements(id) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE accounting_state ADD FOREIGN KEY (last_settlement_id) REFERENCES accounting_settlements(id) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE crm_accounts ADD FOREIGN KEY (current_subscription_id) REFERENCES crm_subscriptions(id) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE crm_accounts ADD FOREIGN KEY (customer_id) REFERENCES crm_customers(id) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE crm_customers ADD FOREIGN KEY (partner_id) REFERENCES partners(id) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE crm_subscriptions ADD FOREIGN KEY (plan) REFERENCES catalog_plans(id) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE crm_subscriptions ADD FOREIGN KEY (previous_id) REFERENCES crm_subscriptions(id) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE crm_subscriptions ADD FOREIGN KEY (account_id) REFERENCES crm_accounts(id) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE email_deliveries ADD FOREIGN KEY (order_id) REFERENCES orders(id) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE invoice_events ADD FOREIGN KEY (invoice_id) REFERENCES partner_invoices(id) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE invoice_payments ADD FOREIGN KEY (invoice_id) REFERENCES partner_invoices(id) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE account_supplier_orders ADD FOREIGN KEY (order_id) REFERENCES orders(id) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE delivery_supplier_orders ADD FOREIGN KEY (order_id) REFERENCES orders(id) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE order_events ADD FOREIGN KEY (order_id) REFERENCES orders(id) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE orders ADD FOREIGN KEY (payout_id) REFERENCES partner_payouts(id) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE orders ADD FOREIGN KEY (partner_id) REFERENCES partners(id) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE orders ADD FOREIGN KEY (plan) REFERENCES catalog_plans(id) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE partner_debits ADD FOREIGN KEY (payout_id) REFERENCES partner_payouts(id) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE partner_debits ADD FOREIGN KEY (refund_id) REFERENCES refund_cases(id) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE partner_debits ADD FOREIGN KEY (partner_id) REFERENCES partners(id) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE partner_invoices ADD FOREIGN KEY (partner_id) REFERENCES partners(id) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE partner_payout_debit_items ADD FOREIGN KEY (debit_id) REFERENCES partner_debits(id) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE partner_payout_debit_items ADD FOREIGN KEY (payout_id) REFERENCES partner_payouts(id) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE partner_payout_items ADD FOREIGN KEY (order_id) REFERENCES orders(id) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE partner_payout_items ADD FOREIGN KEY (payout_id) REFERENCES partner_payouts(id) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE partner_payouts ADD FOREIGN KEY (partner_id) REFERENCES partners(id) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE payments ADD FOREIGN KEY (order_id) REFERENCES orders(id) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE payout_notifications ADD FOREIGN KEY (payout_id) REFERENCES partner_payouts(id) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE recharge_reminders ADD FOREIGN KEY (order_id) REFERENCES orders(id) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE refund_cases ADD FOREIGN KEY (order_id) REFERENCES orders(id) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE refund_events ADD FOREIGN KEY (case_id) REFERENCES refund_cases(id) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE telegram_partner_applications ADD FOREIGN KEY (partner_id) REFERENCES partners(id) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE recharge_orders ADD FOREIGN KEY (order_id) REFERENCES orders(id) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE recharge_redemptions ADD FOREIGN KEY (order_id) REFERENCES recharge_orders(order_id) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE recharge_refunds ADD FOREIGN KEY (order_id) REFERENCES recharge_orders(order_id) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE recharge_refunds ADD FOREIGN KEY (case_id) REFERENCES refund_cases(id) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE xgift_preflight_backends ADD FOREIGN KEY (token) REFERENCES xgift_preflights(token) ON DELETE CASCADE DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE xgift_purchases ADD FOREIGN KEY (order_id) REFERENCES orders(id) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE xgift_recovery_attempts ADD FOREIGN KEY (order_id) REFERENCES orders(id) DEFERRABLE INITIALLY IMMEDIATE;

CREATE INDEX accounting_items_order ON accounting_items(order_id);

CREATE UNIQUE INDEX accounting_order_once ON accounting_items(order_id) WHERE kind='order';

CREATE INDEX accounting_settlements_time ON accounting_settlements(closed_at DESC);

CREATE INDEX crm_accounts_customer ON crm_accounts(customer_id);

CREATE INDEX crm_customers_owner ON crm_customers(partner_id,created_at DESC);

CREATE INDEX crm_subscriptions_account ON crm_subscriptions(account_id,created_at DESC);

CREATE INDEX invoice_event_history ON invoice_events(invoice_id,created_at);

CREATE UNIQUE INDEX invoice_payment_pending ON invoice_payments(invoice_id) WHERE state='pending';

CREATE INDEX invoice_payment_reconcile ON invoice_payments(state,updated_at);

CREATE UNIQUE INDEX invoice_payment_trade ON invoice_payments(provider,account,trade_no) WHERE trade_no IS NOT NULL;

CREATE INDEX account_supplier_reconcile ON account_supplier_orders(state,updated_at);

CREATE INDEX delivery_supplier_reconcile ON delivery_supplier_orders(state,next_check_at,updated_at);

CREATE UNIQUE INDEX one_open_payment_test ON orders(is_test)
WHERE is_test=1 AND status IN ('pending','session') AND refunded_cents=0;

CREATE INDEX order_events_order ON order_events(order_id,created_at);

CREATE INDEX orders_created ON orders(created_at DESC);

CREATE INDEX orders_customer_email ON orders(customer_email,created_at DESC);

CREATE INDEX orders_owner_created ON orders(owner_hash,created_at DESC);

CREATE INDEX orders_partner_created ON orders(partner_id,created_at DESC);

CREATE INDEX orders_payout ON orders(payout_id);

CREATE INDEX orders_status_created ON orders(status,created_at DESC);

CREATE INDEX partner_debits_available ON partner_debits(partner_id,payout_id);

CREATE INDEX partner_invoices_owner ON partner_invoices(partner_id,created_at DESC);

CREATE INDEX partner_invoices_status ON partner_invoices(status,created_at DESC);

CREATE UNIQUE INDEX partner_one_pending_payout ON partner_payouts(partner_id) WHERE status='pending';

CREATE INDEX payments_pending ON payments(state,updated_at);

CREATE INDEX payout_notifications_due ON payout_notifications(delivered_at,next_attempt_at,locked_until);

CREATE INDEX recharge_reminders_due ON recharge_reminders(next_attempt_at)
  WHERE delivered_at IS NULL AND cancelled_at IS NULL;

CREATE INDEX refund_events_case ON refund_events(case_id,created_at);

CREATE UNIQUE INDEX refund_one_open ON refund_cases(order_id) WHERE status IN ('pending','approved');

CREATE INDEX refund_status_created ON refund_cases(status,created_at DESC);

CREATE INDEX telegram_partner_mail_due ON telegram_partner_applications(mail_status,next_attempt_at);

CREATE INDEX telegram_partner_updates_created ON telegram_partner_updates(created_at);

CREATE INDEX recharge_orders_due ON recharge_orders(updated_at);

CREATE INDEX visitor_days_day ON visitor_days(day);

CREATE INDEX visitor_days_shop_day ON visitor_days(shop_id, day);

CREATE INDEX xgift_preflight_expiry ON xgift_preflights(expires_at);

INSERT INTO accounting_state(id) VALUES(1);
