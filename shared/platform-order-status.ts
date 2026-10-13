export type PlatformOrderStatus = { remoteId?: string; paymentStatus: string; refundedCents: number; checkedAt: number | null; financialHold: boolean }
export function confirmedRefundLabel(refundedCents: number, priceCents: number) {
  return refundedCents >= priceCents ? '已退款' : refundedCents > 0 ? '部分退款' : null
}
export function platformPaymentLabel(status: string | null | undefined) {
  return ({ pending: '待付款', paid: '已收款', refunded: '全额退款', partially_refunded: '部分退款', expired: '已过期', closed: '已关闭' } as Record<string, string>)[status ?? ''] ?? '尚未查询'
}
