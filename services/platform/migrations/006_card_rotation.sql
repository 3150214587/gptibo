ALTER TABLE card_rules ADD COLUMN refill_threshold_minor bigint NOT NULL DEFAULT 0 CHECK(refill_threshold_minor>=0);
ALTER TABLE card_rules ADD COLUMN open_amount_minor bigint NOT NULL DEFAULT 0 CHECK(open_amount_minor>=0);
ALTER TABLE card_rules ADD COLUMN replace_on_decline boolean NOT NULL DEFAULT false;
ALTER TABLE card_rules ADD COLUMN max_replacements integer NOT NULL DEFAULT 2 CHECK(max_replacements BETWEEN 0 AND 5);
CREATE TABLE card_rule_runtime (
 rule_id text NOT NULL, rule_version bigint NOT NULL, environment text NOT NULL,
 card_product text NOT NULL, card_id bigint NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(rule_id,rule_version,environment)
);
CREATE TABLE card_retirements (
 environment text NOT NULL, card_id bigint NOT NULL, order_id text NOT NULL REFERENCES orders(id),
 reason text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(environment,card_id)
);
CREATE TRIGGER card_retirements_immutable BEFORE UPDATE OR DELETE ON card_retirements FOR EACH ROW EXECUTE FUNCTION immutable_record();
