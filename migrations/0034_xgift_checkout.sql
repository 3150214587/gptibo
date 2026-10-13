INSERT INTO catalog_plans(id,product_id,name,price_cents,enabled,renewal_only,updated_at,partner_enabled,purchase_note)
VALUES('x-premium-3m','x-premium','Premium · 3 个月',1700,1,0,0,0,'填写 X 用户名，付款前检测赠送资格。付款确认后自动赠送；不需要接收账号的密码或 Cookie。'),
('x-premium-6m','x-premium','Premium · 6 个月',3400,1,0,0,0,'填写 X 用户名，付款前检测赠送资格。付款确认后自动赠送；不需要接收账号的密码或 Cookie。');
CREATE TABLE xgift_purchases (
  order_id TEXT PRIMARY KEY REFERENCES orders(id),
  username TEXT NOT NULL, recipient_id TEXT NOT NULL, product_code TEXT NOT NULL,
  points INTEGER NOT NULL CHECK(points>0), credential_hash TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT 'prepared' CHECK(state IN ('prepared','creating','unknown','pending','succeeded','failed')),
  supplier_order TEXT UNIQUE, receipt TEXT, failure_code TEXT,
  lease_token TEXT, lease_until INTEGER NOT NULL DEFAULT 0, next_check INTEGER NOT NULL DEFAULT 0,
  attempted_at INTEGER, updated_at INTEGER NOT NULL
);
CREATE TRIGGER xgift_purchase_immutable BEFORE UPDATE OF username,recipient_id,product_code,points,credential_hash ON xgift_purchases
BEGIN SELECT RAISE(ABORT,'immutable_xgift_purchase'); END;
CREATE TABLE xgift_preflights (
  token TEXT PRIMARY KEY, owner_hash TEXT NOT NULL, username TEXT NOT NULL, recipient_id TEXT NOT NULL,
  product_code TEXT NOT NULL, points INTEGER NOT NULL, expires_at INTEGER NOT NULL
);
CREATE INDEX xgift_preflight_expiry ON xgift_preflights(expires_at);
-- Initial acceptance authorizes one real gift. Reserve that slot before accepting payment.
CREATE TRIGGER xgift_initial_order_slot BEFORE INSERT ON xgift_purchases
WHEN EXISTS(SELECT 1 FROM xgift_purchases x JOIN orders o ON o.id=x.order_id
 WHERE x.order_id<>NEW.order_id AND x.state<>'failed' AND o.status<>'cancelled')
BEGIN SELECT RAISE(ABORT,'xgift_initial_slot_busy'); END;
