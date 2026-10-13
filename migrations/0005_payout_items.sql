-- Keep the contents of each request even if rejection releases its reservation.
CREATE TABLE partner_payout_items (
  payout_id TEXT NOT NULL REFERENCES partner_payouts(id),
  order_id TEXT NOT NULL REFERENCES orders(id),
  profit_cents INTEGER NOT NULL CHECK(profit_cents > 0),
  PRIMARY KEY(payout_id,order_id)
);
INSERT INTO partner_payout_items(payout_id,order_id,profit_cents)
SELECT payout_id,id,price_cents-cost_cents FROM orders WHERE payout_id IS NOT NULL AND price_cents>cost_cents;
