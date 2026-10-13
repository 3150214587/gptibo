ALTER TABLE catalog_plans ADD COLUMN purchase_note TEXT NOT NULL DEFAULT '';
ALTER TABLE catalog_plans ADD COLUMN delivery_instructions TEXT NOT NULL DEFAULT '';
ALTER TABLE catalog_plans ADD COLUMN partner_enabled INTEGER NOT NULL DEFAULT 1 CHECK(partner_enabled IN (0,1));
ALTER TABLE orders ADD COLUMN purchase_note TEXT NOT NULL DEFAULT '';
INSERT INTO catalog_plans(id,product_id,name,price_cents,enabled,updated_at,purchase_note,delivery_instructions) VALUES
('gemini-pro-18m','gemini','18 个月会员',1990,1,0,'邮件交付领取链接，付款后由工作人员处理。购买前如需确认账号资格或适用地区，请联系支持。','请使用需要领取会员的账号打开链接，并先核对领取页面的资格与提示。遇到问题请通过原订单联系支持。'),
('duolingo-12m','duolingo','1 年会员',1990,1,0,'邮件交付兑换 CDK，付款后由工作人员处理。购买前如需确认账号资格或适用地区，请联系支持。','请按随单说明在对应兑换页面使用 CDK，核对账号后再确认兑换。遇到问题请通过原订单联系支持。');
UPDATE catalog_plans SET partner_enabled=0 WHERE id IN ('gemini-pro-18m','duolingo-12m');
INSERT INTO partner_default_prices(plan,cost_cents) VALUES ('gemini-pro-18m',1990),('duolingo-12m',1990);
INSERT INTO partner_prices(partner_id,plan,cost_cents,price_cents)
SELECT p.id,c.id,1990,1990 FROM partners p CROSS JOIN catalog_plans c WHERE c.id IN ('gemini-pro-18m','duolingo-12m');
