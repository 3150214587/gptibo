CREATE TABLE payment_settings (
    id boolean PRIMARY KEY DEFAULT true CHECK (id),
    provider text NOT NULL DEFAULT '',
    version bigint NOT NULL DEFAULT 0 CHECK (version >= 0),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (provider IN ('','alipay','gatewaya-alipay','gatewaya-wxpay'))
);
INSERT INTO payment_settings(id) VALUES(true);
