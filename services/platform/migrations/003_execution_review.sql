ALTER TABLE orders ADD COLUMN account_label text NOT NULL DEFAULT '';
CREATE FUNCTION guard_account_label() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.account_label<>'' AND NEW.account_label<>OLD.account_label THEN RAISE EXCEPTION 'immutable_account'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER order_account_guard BEFORE UPDATE ON orders FOR EACH ROW EXECUTE FUNCTION guard_account_label();
ALTER TABLE card_rules ADD COLUMN max_open_fee_minor bigint NOT NULL DEFAULT 0 CHECK(max_open_fee_minor>=0);
ALTER TABLE card_operations ADD COLUMN fee_minor bigint NOT NULL DEFAULT 0 CHECK(fee_minor>=0);
ALTER TABLE card_operations ADD COLUMN resolution_note text NOT NULL DEFAULT '';
ALTER TABLE card_operations DROP CONSTRAINT card_operations_status_check;
ALTER TABLE card_operations ADD CONSTRAINT card_operations_status_check CHECK(status IN ('submitting','succeeded','unknown','failed'));
CREATE UNIQUE INDEX one_order_reservation ON ledger(order_id) WHERE kind='reserve';
CREATE UNIQUE INDEX one_order_settlement ON ledger(order_id) WHERE kind IN ('consume','release');
-- The runtime role can lock wallets but changes monetary columns only through this trigger.
ALTER FUNCTION ledger_apply() SECURITY DEFINER SET search_path FROM CURRENT;
CREATE FUNCTION guard_wallet_owner() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.user_id<>OLD.user_id THEN RAISE EXCEPTION 'immutable_wallet_owner'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER wallet_owner_guard BEFORE UPDATE ON wallets FOR EACH ROW EXECUTE FUNCTION guard_wallet_owner();
CREATE INDEX outbox_channel_page ON outbox(user_id,created_at DESC,id DESC);
CREATE INDEX audit_page ON audit(created_at DESC,id DESC);
CREATE INDEX ledger_channel_page ON ledger(user_id,created_at DESC,id DESC);
