-- Default stores preserve existing IDs, referral codes and account-level settings.
CREATE TABLE partner_stores (
 id text PRIMARY KEY,
 partner_id text NOT NULL REFERENCES partners(id),
 slot smallint NOT NULL CHECK(slot BETWEEN 1 AND 5),
 slug text NOT NULL UNIQUE,
 name text NOT NULL CHECK(char_length(name) BETWEEN 1 AND 32),
 active boolean NOT NULL DEFAULT true,
 version bigint NOT NULL DEFAULT 0,
 settings jsonb NOT NULL DEFAULT '{}',
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(partner_id,slot), UNIQUE(id,partner_id)
);
INSERT INTO partner_stores(id,partner_id,slot,slug,name) SELECT id,id,1,slug,left(name,32) FROM partners;
CREATE FUNCTION provision_default_store() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO partner_stores(id,partner_id,slot,slug,name) VALUES(NEW.id,NEW.id,1,NEW.slug,left(NEW.name,32));
 RETURN NEW;
END $$;
CREATE TRIGGER partner_default_store AFTER INSERT ON partners FOR EACH ROW EXECUTE FUNCTION provision_default_store();
CREATE TABLE store_prices (
 store_id text NOT NULL REFERENCES partner_stores(id),
 plan text NOT NULL REFERENCES catalog_plans(id),
 price_cents bigint NOT NULL CHECK(price_cents BETWEEN 1 AND 10000000),
 enabled boolean NOT NULL DEFAULT true,
 version bigint NOT NULL DEFAULT 0,
 PRIMARY KEY(store_id,plan)
);
-- Account-level downstream supply quotes never depend on a particular retail store.
ALTER TABLE partner_prices ADD COLUMN child_cost_cents bigint;
UPDATE partner_prices SET child_cost_cents=price_cents;
ALTER TABLE partner_prices ADD CONSTRAINT child_quote_floor CHECK(child_cost_cents IS NULL OR child_cost_cents>=cost_cents);
CREATE TABLE partner_coupons (
 id text PRIMARY KEY,
 partner_id text NOT NULL REFERENCES partners(id),
 code text NOT NULL CHECK(code=upper(code) AND code ~ '^[A-Z0-9_-]{3,32}$'),
 discount_cents bigint NOT NULL CHECK(discount_cents BETWEEN 1 AND 10000000),
 starts_at timestamptz NOT NULL,
 ends_at timestamptz NOT NULL CHECK(ends_at>starts_at),
 max_uses integer NOT NULL CHECK(max_uses BETWEEN 1 AND 1000000),
 once_per_email boolean NOT NULL DEFAULT true,
 active boolean NOT NULL DEFAULT true,
 version bigint NOT NULL DEFAULT 0,
 UNIQUE(partner_id,code), UNIQUE(id,partner_id)
);
CREATE TABLE coupon_targets (
 coupon_id text NOT NULL,
 partner_id text NOT NULL,
 store_id text NOT NULL,
 plan text NOT NULL REFERENCES catalog_plans(id),
 PRIMARY KEY(coupon_id,store_id,plan),
 FOREIGN KEY(coupon_id,partner_id) REFERENCES partner_coupons(id,partner_id),
 FOREIGN KEY(store_id,partner_id) REFERENCES partner_stores(id,partner_id)
);
ALTER TABLE orders ADD COLUMN store_id text REFERENCES partner_stores(id);
ALTER TABLE orders ADD COLUMN store_name text;
ALTER TABLE orders ADD COLUMN coupon_id text REFERENCES partner_coupons(id);
ALTER TABLE orders ADD COLUMN coupon_code text;
ALTER TABLE orders ADD COLUMN list_price_cents bigint;
ALTER TABLE orders ADD COLUMN discount_cents bigint NOT NULL DEFAULT 0;
ALTER TABLE orders ADD CONSTRAINT order_discount_snapshot CHECK(discount_cents>=0 AND (coupon_id IS NULL AND discount_cents=0 OR coupon_id IS NOT NULL AND list_price_cents=price_cents+discount_cents AND cost_cents IS NOT NULL AND price_cents>=cost_cents));
CREATE INDEX orders_store ON orders(store_id,created_at DESC);
CREATE INDEX orders_coupon ON orders(coupon_id,customer_email) WHERE coupon_id IS NOT NULL;
-- Historical orders are not rewritten. A NULL store_id on an existing partner order means its default store.
ALTER TABLE partner_domains DROP CONSTRAINT partner_domains_pkey;
ALTER TABLE partner_domains ADD COLUMN id text;
UPDATE partner_domains SET id=partner_id;
ALTER TABLE partner_domains ALTER COLUMN id SET NOT NULL;
ALTER TABLE partner_domains ADD PRIMARY KEY(id);
ALTER TABLE partner_domains ADD COLUMN store_id text REFERENCES partner_stores(id);
UPDATE partner_domains SET store_id=partner_id;
ALTER TABLE partner_domains ADD COLUMN free boolean NOT NULL DEFAULT false;
UPDATE partner_domains SET free=true;
CREATE UNIQUE INDEX partner_free_domain ON partner_domains(partner_id) WHERE free;
ALTER TABLE partner_domains ADD COLUMN expires_at timestamptz;
ALTER TABLE partner_domains ADD COLUMN billing_version bigint NOT NULL DEFAULT 0;
ALTER TABLE partner_domains ADD COLUMN billing_checked_at timestamptz;
CREATE TABLE domain_charges (
 id text PRIMARY KEY,
 domain_id text NOT NULL,
 partner_id text NOT NULL REFERENCES partners(id),
 hostname text NOT NULL,
 period bigint NOT NULL,
 amount_cents bigint NOT NULL DEFAULT 2000 CHECK(amount_cents=2000),
 source text NOT NULL CHECK(source IN ('balance','gatewayb','admin')),
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','paid','closed')),
 provider text NOT NULL DEFAULT '', account text NOT NULL DEFAULT '',
 trade_no text, qr text,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 paid_at timestamptz,
 lease_token text, lease_until timestamptz,
 UNIQUE(domain_id,period)
);
CREATE UNIQUE INDEX domain_charge_trade ON domain_charges(provider,account,trade_no) WHERE trade_no IS NOT NULL;
ALTER TABLE partner_debits ALTER COLUMN refund_id DROP NOT NULL;
ALTER TABLE partner_debits ADD COLUMN domain_charge_id text UNIQUE REFERENCES domain_charges(id);
ALTER TABLE partner_debits ADD CONSTRAINT debit_source CHECK((refund_id IS NOT NULL)::int+(domain_charge_id IS NOT NULL)::int=1);
-- Compatibility for existing domain callers; new callers always supply a binding ID and store.
CREATE FUNCTION default_domain_binding() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.id IS NULL THEN NEW.free:=true; END IF;
 NEW.id:=COALESCE(NEW.id,NEW.partner_id);
 NEW.store_id:=COALESCE(NEW.store_id,NEW.partner_id);
 RETURN NEW;
END $$;
CREATE TRIGGER domain_binding_defaults BEFORE INSERT ON partner_domains FOR EACH ROW EXECUTE FUNCTION default_domain_binding();
CREATE FUNCTION initialize_supply_quote() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 NEW.child_cost_cents:=COALESCE(NEW.child_cost_cents,NEW.price_cents);
 RETURN NEW;
END $$;
CREATE TRIGGER partner_supply_quote BEFORE INSERT ON partner_prices FOR EACH ROW EXECUTE FUNCTION initialize_supply_quote();
-- An expired local timer does not prove that an upstream cashier is closed.
-- Only cancelled, still-unpaid orders with no uncertain payment can release a coupon.
CREATE FUNCTION coupon_reserved(o orders) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT NOT (o.payment_status='unpaid' AND o.status='cancelled'
  AND NOT EXISTS(SELECT 1 FROM payments p WHERE p.order_id=o.id AND p.state<>'closed')
  AND NOT EXISTS(SELECT 1 FROM recharge_orders t WHERE t.order_id=o.id AND t.payment_status NOT IN ('closed','expired')))
$$;
-- Products added after the migration receive independent starting prices in every extra store.
CREATE FUNCTION provision_store_prices() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO store_prices(store_id,plan,price_cents)
 SELECT id,NEW.plan,NEW.price_cents FROM partner_stores WHERE partner_id=NEW.partner_id AND slot>1
 ON CONFLICT DO NOTHING;
 RETURN NEW;
END $$;
CREATE TRIGGER partner_store_prices AFTER INSERT ON partner_prices FOR EACH ROW EXECUTE FUNCTION provision_store_prices();
