CREATE TABLE users (
 id text PRIMARY KEY, email text NOT NULL UNIQUE, name text NOT NULL,
 role text NOT NULL CHECK(role IN ('admin','channel')),
 status text NOT NULL CHECK(status IN ('invited','active','banned','deleted')),
 password_hash text NOT NULL DEFAULT '', created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE wallets (user_id text PRIMARY KEY REFERENCES users(id), available_minor bigint NOT NULL DEFAULT 0 CHECK(available_minor>=0), frozen_minor bigint NOT NULL DEFAULT 0 CHECK(frozen_minor>=0));
CREATE TABLE sessions (token_hash text PRIMARY KEY, user_id text NOT NULL REFERENCES users(id), expires_at timestamptz NOT NULL);
CREATE TABLE invitations (token_hash text PRIMARY KEY, user_id text NOT NULL REFERENCES users(id), expires_at timestamptz NOT NULL, used_at timestamptz);
CREATE TABLE login_limits (id text PRIMARY KEY, attempts int NOT NULL DEFAULT 0, reset_at timestamptz NOT NULL);
CREATE TABLE api_keys (id text PRIMARY KEY, user_id text NOT NULL REFERENCES users(id), label text NOT NULL, secret_cipher text NOT NULL, revoked boolean NOT NULL DEFAULT false, created_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE nonces (key_id text NOT NULL REFERENCES api_keys(id), nonce text NOT NULL, expires_at timestamptz NOT NULL, PRIMARY KEY(key_id,nonce));
CREATE TABLE products (
 code text PRIMARY KEY, name text NOT NULL, business text NOT NULL CHECK(business IN ('gpt','x')),
 supply_minor bigint NOT NULL DEFAULT 0 CHECK(supply_minor>=0), enabled boolean NOT NULL DEFAULT false,
 upstream_plan text NOT NULL DEFAULT '', config jsonb NOT NULL DEFAULT '{}', version bigint NOT NULL DEFAULT 1, sort_order int NOT NULL DEFAULT 0
);
INSERT INTO products(code,name,business,config) VALUES
 ('gpt-plus','ChatGPT Plus','gpt','{}'),('gpt-5x','ChatGPT Pro 5×','gpt','{}'),
 ('gpt-10x','ChatGPT Pro 10×','gpt','{}'),('gpt-25x','ChatGPT Pro 25×','gpt','{}'),
 ('x-3m','X Premium · 3 个月','x','{"months":3,"amount_minor":0,"currency":"","stripe_product":"","stripe_merchant":""}'),
 ('x-6m','X Premium · 6 个月','x','{"months":6,"amount_minor":0,"currency":"","stripe_product":"","stripe_merchant":""}');
UPDATE products SET sort_order=CASE code WHEN 'gpt-plus' THEN 10 WHEN 'gpt-5x' THEN 20 WHEN 'gpt-10x' THEN 30 WHEN 'gpt-25x' THEN 40 WHEN 'x-3m' THEN 50 WHEN 'x-6m' THEN 60 END;
CREATE TABLE channel_prices (user_id text REFERENCES users(id), product_code text REFERENCES products(code), supply_minor bigint NOT NULL CHECK(supply_minor>0), PRIMARY KEY(user_id,product_code));
CREATE TABLE settings (id text PRIMARY KEY, payload_cipher text NOT NULL, version bigint NOT NULL DEFAULT 1, updated_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE card_rules (
 id text PRIMARY KEY, business text NOT NULL CHECK(business IN ('gpt','x')), product_code text NOT NULL DEFAULT '',
 card_product text NOT NULL, card_id bigint NOT NULL DEFAULT 0, auto_open boolean NOT NULL DEFAULT false,
 funding_minor bigint NOT NULL CHECK(funding_minor>0), max_order_minor bigint NOT NULL CHECK(max_order_minor>0),
 daily_limit_minor bigint NOT NULL CHECK(daily_limit_minor>=funding_minor), enabled boolean NOT NULL DEFAULT true,
 version bigint NOT NULL DEFAULT 1, UNIQUE(business,product_code)
);
CREATE TABLE x_accounts (id text PRIMARY KEY, name text NOT NULL, payload_cipher text NOT NULL, daily_limit int NOT NULL CHECK(daily_limit BETWEEN 1 AND 300), enabled boolean NOT NULL DEFAULT true);
CREATE TABLE orders (
 id text PRIMARY KEY, user_id text NOT NULL REFERENCES users(id), merchant_order_no text NOT NULL, idempotency_key text NOT NULL,
 request_hash text NOT NULL, product_code text NOT NULL REFERENCES products(code), business text NOT NULL,
 supply_minor bigint NOT NULL CHECK(supply_minor>0), status text NOT NULL DEFAULT 'queued' CHECK(status IN ('queued','running','unknown','succeeded','failed')),
 recipient text NOT NULL DEFAULT '', credential_cipher text NOT NULL DEFAULT '', config_cipher text NOT NULL,
 job_cipher text NOT NULL DEFAULT '', stage text NOT NULL DEFAULT 'preflight', upstream_ref text NOT NULL DEFAULT '',
 failure_code text NOT NULL DEFAULT '', receipt text NOT NULL DEFAULT '',
 lease_token text NOT NULL DEFAULT '', lease_until timestamptz, next_at timestamptz NOT NULL DEFAULT now(),
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(user_id,merchant_order_no), UNIQUE(user_id,idempotency_key)
);
CREATE INDEX orders_due ON orders(next_at) WHERE status IN ('queued','running','unknown');
CREATE INDEX orders_channel ON orders(user_id,created_at DESC);
CREATE TABLE ledger (
 id text PRIMARY KEY, user_id text NOT NULL REFERENCES users(id), order_id text REFERENCES orders(id),
 kind text NOT NULL CHECK(kind IN ('credit','reserve','consume','release')),
 available_delta bigint NOT NULL, frozen_delta bigint NOT NULL, reference text NOT NULL, note text NOT NULL,
 actor_id text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(user_id,reference)
);
CREATE FUNCTION ledger_apply() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT ((NEW.kind='credit' AND NEW.available_delta>0 AND NEW.frozen_delta=0 AND NEW.order_id IS NULL) OR
 (NEW.kind='reserve' AND NEW.available_delta<0 AND NEW.frozen_delta=-NEW.available_delta AND EXISTS(SELECT 1 FROM orders WHERE id=NEW.order_id AND user_id=NEW.user_id AND status='queued' AND supply_minor=NEW.frozen_delta)) OR
 (NEW.kind='consume' AND NEW.available_delta=0 AND NEW.frozen_delta<0 AND EXISTS(SELECT 1 FROM orders WHERE id=NEW.order_id AND user_id=NEW.user_id AND status='succeeded' AND supply_minor=-NEW.frozen_delta)) OR
 (NEW.kind='release' AND NEW.available_delta>0 AND NEW.frozen_delta=-NEW.available_delta AND EXISTS(SELECT 1 FROM orders WHERE id=NEW.order_id AND user_id=NEW.user_id AND status='failed' AND supply_minor=NEW.available_delta))) THEN RAISE EXCEPTION 'invalid_ledger'; END IF;
 UPDATE wallets SET available_minor=available_minor+NEW.available_delta,frozen_minor=frozen_minor+NEW.frozen_delta WHERE user_id=NEW.user_id;
 IF NOT FOUND THEN RAISE EXCEPTION 'wallet_missing'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER ledger_apply BEFORE INSERT ON ledger FOR EACH ROW EXECUTE FUNCTION ledger_apply();
CREATE FUNCTION immutable_record() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'immutable_record'; END $$;
CREATE TRIGGER ledger_immutable BEFORE UPDATE OR DELETE ON ledger FOR EACH ROW EXECUTE FUNCTION immutable_record();
CREATE FUNCTION guard_order() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (NEW.user_id,NEW.merchant_order_no,NEW.idempotency_key,NEW.request_hash,NEW.product_code,NEW.supply_minor,NEW.business,NEW.recipient,NEW.config_cipher) IS DISTINCT FROM (OLD.user_id,OLD.merchant_order_no,OLD.idempotency_key,OLD.request_hash,OLD.product_code,OLD.supply_minor,OLD.business,OLD.recipient,OLD.config_cipher) THEN RAISE EXCEPTION 'immutable_order'; END IF;
 IF OLD.status IN ('succeeded','failed') AND NEW.status<>OLD.status THEN RAISE EXCEPTION 'terminal_order'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER order_guard BEFORE UPDATE ON orders FOR EACH ROW EXECUTE FUNCTION guard_order();
CREATE TABLE card_operations (
 reference text PRIMARY KEY, order_id text NOT NULL REFERENCES orders(id), rule_id text NOT NULL,
 operation text NOT NULL CHECK(operation IN ('open','fund')), amount_minor bigint NOT NULL CHECK(amount_minor>0),
 card_id bigint NOT NULL DEFAULT 0, status text NOT NULL CHECK(status IN ('submitting','succeeded','unknown')),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE resource_locks (resource text PRIMARY KEY, order_id text NOT NULL REFERENCES orders(id));
CREATE TABLE account_usage (account_id text REFERENCES x_accounts(id), order_id text PRIMARY KEY REFERENCES orders(id), payload_cipher text NOT NULL, day date NOT NULL DEFAULT (now() AT TIME ZONE 'Asia/Shanghai')::date);
CREATE TABLE card_bindings (environment text NOT NULL, card_id bigint NOT NULL, business text NOT NULL, PRIMARY KEY(environment,card_id));
CREATE TABLE webhook_configs (user_id text PRIMARY KEY REFERENCES users(id), url text NOT NULL, secret_cipher text NOT NULL);
CREATE TABLE outbox (
 id text PRIMARY KEY, user_id text NOT NULL REFERENCES users(id), kind text NOT NULL CHECK(kind IN ('webhook','invitation')),
 payload_cipher text NOT NULL, status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','sent','dead')),
 attempts int NOT NULL DEFAULT 0, next_at timestamptz NOT NULL DEFAULT now(), lease_token text NOT NULL DEFAULT '', lease_until timestamptz,
 last_error text NOT NULL DEFAULT '', created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE audit (id text PRIMARY KEY, actor_id text NOT NULL, action text NOT NULL, target text NOT NULL, note text NOT NULL DEFAULT '', created_at timestamptz NOT NULL DEFAULT now());
CREATE TRIGGER audit_immutable BEFORE UPDATE OR DELETE ON audit FOR EACH ROW EXECUTE FUNCTION immutable_record();
CREATE TABLE worker_heartbeats (id text PRIMARY KEY, updated_at timestamptz NOT NULL DEFAULT now());
