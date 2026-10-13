export type PartnerOrderPeriod = 'all' | 'today' | '7' | '30'
export type PartnerOrderTotals = { orderCount: number; completedCount: number; profitCents: number; heldCount: number }
export type PartnerProductSummary = PartnerOrderTotals & { productId: string }
export type PartnerOrderSummary = {
  period: PartnerOrderPeriod
  since: number | null
  asOf: number
  totals: PartnerOrderTotals
  products: PartnerProductSummary[]
}
