-- A bank receipt funds exactly one channel. Order references remain scoped by channel.
CREATE UNIQUE INDEX credit_receipt_unique ON ledger(reference) WHERE kind='credit';
