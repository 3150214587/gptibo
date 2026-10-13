-- Update customer copy without overwriting independently edited product notes.
UPDATE catalog_plans
SET purchase_note='美国个人 Gmail 全新白号，已开启 2FA，附验证链接。付款确认后，账号资料自动发送至下单邮箱。请使用稳定、干净的登录环境。质保仅限购买当天首次登录，登录后的风控、封禁及操作异常不在质保范围内。如需售后，请通过原订单联系支持。',
    version=version+1,
    updated_at=CAST(strftime('%s','now') AS INTEGER)*1000
WHERE id='gmail-account'
  AND purchase_note='美国个人 Gmail 全新白号，已开 2FA，带验证链接。付款确认后自动采购，资料发送至下单邮箱。需稳定、干净的登录环境。供应商仅质保购买当天首次登录，登录后的风控、封禁及操作异常不在供应商质保范围。购买前请确认适用条件，售后通过原订单申请。';

UPDATE catalog_plans
SET delivery_instructions=REPLACE(REPLACE(delivery_instructions,'供应商提示首次登录后先保持常用环境','首次登录后请保持常用环境'),'供应商仅质保购买当天首次登录','质保仅限购买当天首次登录'),
    version=version+1,
    updated_at=CAST(strftime('%s','now') AS INTEGER)*1000
WHERE id='gmail-account'
  AND (INSTR(delivery_instructions,'供应商提示首次登录后先保持常用环境')>0
       OR INSTR(delivery_instructions,'供应商仅质保购买当天首次登录')>0);
