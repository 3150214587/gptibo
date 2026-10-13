-- Initialize legacy Telegram shops that never saved contact settings.
-- Keep edited settings (version >= 1), nonempty values and public email intact.
INSERT INTO support_contacts(owner,partner_id,wechat,qq,email,version,change_id,updated_at)
SELECT a.partner_id,a.partner_id,a.wechat,a.qq,'',1,
       'telegram-contacts-backfill/' || a.partner_id,
       CAST(strftime('%s','now') AS INTEGER) * 1000
FROM telegram_partner_applications a
JOIN partners p ON p.id=a.partner_id
LEFT JOIN support_contacts sc ON sc.owner=a.partner_id
WHERE sc.owner IS NULL OR (sc.version=0 AND ((sc.wechat='' AND a.wechat!='') OR (sc.qq='' AND a.qq!='')))
ON CONFLICT(owner) DO UPDATE SET
  wechat=CASE WHEN support_contacts.wechat='' THEN excluded.wechat ELSE support_contacts.wechat END,
  qq=CASE WHEN support_contacts.qq='' THEN excluded.qq ELSE support_contacts.qq END,
  version=support_contacts.version+1,
  change_id=excluded.change_id,
  updated_at=excluded.updated_at
WHERE support_contacts.version=0 AND
      ((support_contacts.wechat='' AND excluded.wechat!='') OR (support_contacts.qq='' AND excluded.qq!=''));

INSERT INTO admin_audit(id,target_id,action,note,created_at)
SELECT change_id,owner,'support-contacts','从 Telegram 申请补齐未设置的商店微信和 QQ',updated_at
FROM support_contacts
WHERE change_id='telegram-contacts-backfill/' || owner
ON CONFLICT(id) DO NOTHING;
