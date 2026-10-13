export type DefaultSupplyPlan = {
  id: string
  product_id: string
  name: string
  price_cents: number
  cost_cents: number
  catalog_version: number
  renewal_only: number
}
export type DefaultSupplySettings = { version: number; plans: DefaultSupplyPlan[] }
