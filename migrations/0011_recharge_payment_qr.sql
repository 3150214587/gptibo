-- The Alipay 当面付 payload is separate from the platform cashier URL.
-- Historical orders retain their original immutable supplier request and channel.
ALTER TABLE recharge_orders ADD COLUMN payment_qr TEXT;
