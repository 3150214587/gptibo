-- User approved normal sales after the first gift: wallet balances and atomic
-- supplier freezes govern capacity; completed gifts must not block new buyers.
DROP TRIGGER IF EXISTS xgift_initial_order_slot;
