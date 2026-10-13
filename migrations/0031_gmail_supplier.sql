INSERT INTO catalog_plans(id,product_id,name,price_cents,enabled,updated_at,purchase_note,delivery_instructions,partner_enabled)
VALUES ('gmail-account','gmail','美国长效白号 · 带 2FA',1880,1,0,'美国个人 Gmail 全新白号，已开 2FA，带验证链接。付款确认后自动采购，资料发送至下单邮箱。需稳定、干净的登录环境。供应商仅质保购买当天首次登录，登录后的风控、封禁及操作异常不在供应商质保范围。购买前请确认适用条件，售后通过原订单申请。','资料格式：账号----密码----2FA 密钥----验证卡----验证链接（以实际交付资料为准）。\n请前往 accounts.google.com 登录，使用可信的身份验证器生成 2FA 验证码，勿将密钥提交至陌生网站。保持稳定的登录环境，避免频繁切换设备与 IP。供应商提示首次登录后先保持常用环境，再完善账号安全设置；具体风险与操作请自行评估。\n供应商仅质保购买当天首次登录，遇到无法登录请及时通过原订单联系支持。账号、密码与验证资料请勿转发。',1);
INSERT INTO partner_default_prices(plan,cost_cents) VALUES ('gmail-account',1000);
INSERT INTO partner_prices(partner_id,plan,cost_cents,price_cents)
SELECT id,'gmail-account',1000,1880 FROM partners;

CREATE TABLE account_supplier_orders (
  order_id TEXT PRIMARY KEY REFERENCES orders(id),
  goods_id INTEGER NOT NULL,
  goods_name TEXT NOT NULL,
  api_base TEXT NOT NULL,
  app_id TEXT NOT NULL,
  max_cost_cents INTEGER NOT NULL CHECK(max_cost_cents>=0),
  instructions TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT 'prepared' CHECK(state IN ('prepared','creating','unknown','purchased','rejected')),
  supplier_order TEXT UNIQUE,
  actual_cost_cents INTEGER,
  cards TEXT,
  attempted_at INTEGER,
  lease_token TEXT,
  lease_until INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL
);
CREATE INDEX account_supplier_reconcile ON account_supplier_orders(state,updated_at);

-- Gmail procurement uses each order's confirmed supplier cost, never a later
-- editable default cost. Preserve the existing settlement eligibility rules.
DROP VIEW accounting_candidates;
CREATE VIEW accounting_candidates AS
SELECT o.id order_id,o.plan,o.plan_name,'order' kind,o.price_cents sale_cents,
  CASE WHEN o.plan='gmail-account' THEN k.actual_cost_cents ELSE c.cost_cents END cost_cents,
  MAX(o.refunded_cents,COALESCE(t.refunded_cents,0)) refund_cents,o.created_at
FROM orders o LEFT JOIN recharge_orders t ON t.order_id=o.id LEFT JOIN accounting_costs c ON c.plan=o.plan LEFT JOIN account_supplier_orders k ON k.order_id=o.id
WHERE o.mode='live' AND o.is_test=0 AND o.payment_status='paid' AND o.status='success'
  AND o.refund_hold=0 AND COALESCE(t.financial_hold,0)=0
  AND NOT EXISTS(SELECT 1 FROM accounting_items i WHERE i.order_id=o.id AND i.kind='order')
UNION ALL
SELECT o.id,o.plan,o.plan_name,'refund',0,0,
  MAX(o.refunded_cents,COALESCE(t.refunded_cents,0))-(SELECT SUM(i.refund_cents) FROM accounting_items i WHERE i.order_id=o.id),o.created_at
FROM orders o LEFT JOIN recharge_orders t ON t.order_id=o.id
WHERE o.mode='live' AND o.is_test=0 AND o.payment_status='paid' AND o.refund_hold=0 AND COALESCE(t.financial_hold,0)=0
  AND EXISTS(SELECT 1 FROM accounting_items i WHERE i.order_id=o.id AND i.kind='order')
  AND MAX(o.refunded_cents,COALESCE(t.refunded_cents,0))>(SELECT SUM(i.refund_cents) FROM accounting_items i WHERE i.order_id=o.id);
