-- NULL preserves the previous single-channel configuration until explicitly saved.
ALTER TABLE payment_settings ADD COLUMN enabled_providers text[];
ALTER TABLE payment_settings ADD CONSTRAINT enabled_payment_providers CHECK (
    enabled_providers IS NULL OR (
        cardinality(enabled_providers) BETWEEN 1 AND 2
        AND enabled_providers <@ ARRAY['alipay','gatewaya-alipay','gatewaya-wxpay']::text[]
        AND array_position(enabled_providers,NULL) IS NULL
        AND (cardinality(enabled_providers)=1 OR enabled_providers[1]<>enabled_providers[2])
        AND NOT (enabled_providers @> ARRAY['alipay','gatewaya-alipay']::text[])
    )
);
