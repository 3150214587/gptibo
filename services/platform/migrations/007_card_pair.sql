ALTER TABLE card_rules ADD COLUMN backup_card_id bigint NOT NULL DEFAULT 0 CHECK(backup_card_id>=0);
CREATE TABLE card_pool_slots (
 rule_id text NOT NULL, environment text NOT NULL, slot integer NOT NULL CHECK(slot IN (1,2)),
 card_product text NOT NULL, card_id bigint NOT NULL CHECK(card_id>0), last_four text NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(rule_id,environment,slot), UNIQUE(environment,card_id)
);
CREATE TABLE card_declines (
 environment text NOT NULL, card_id bigint NOT NULL, order_id text NOT NULL REFERENCES orders(id),
 intent_id text NOT NULL, method_id text NOT NULL, reason text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(environment,order_id,card_id),
 UNIQUE(environment,intent_id,method_id)
);
CREATE TRIGGER card_declines_immutable BEFORE UPDATE OR DELETE ON card_declines FOR EACH ROW EXECUTE FUNCTION immutable_record();
CREATE TABLE card_replacements (
 environment text NOT NULL, old_card_id bigint NOT NULL, rule_id text NOT NULL, slot integer NOT NULL CHECK(slot IN (1,2)),
 order_id text NOT NULL REFERENCES orders(id), config_cipher text NOT NULL,
 status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','closing','closed','opening','completed')),
 close_balance_minor bigint, refund_minor bigint NOT NULL DEFAULT 0,
 prior_flow_ids jsonb NOT NULL DEFAULT '[]', job_cipher text NOT NULL DEFAULT '',
 new_card_id bigint NOT NULL DEFAULT 0, last_error text NOT NULL DEFAULT '',
 lease_token text NOT NULL DEFAULT '', lease_until timestamptz, next_at timestamptz NOT NULL DEFAULT now(),
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(environment,old_card_id)
);
