export const refundLabel = (status: string) => ({ pending: '待审核', approved: '待退款', refunded: '退款完成', rejected: '未通过', cancelled: '已撤回' })[status] ?? status
export type RefundCase = { platform_status?: string | null; fee_cents?: number | null; id: string; status: string; reason: string; note: string; version: number; requested_cents: number; approved_cents: number | null; created_at: number }
export type RefundEvent = { id?: string; case_id: string; status: string; note: string; created_at: number }

export function refundCaseLabel(refund: Pick<RefundCase, 'status' | 'platform_status'>) {
  if (refund.status !== 'approved' || !refund.platform_status) return refundLabel(refund.status)
  return ({ unknown: '提交待确认', requested: '退款待审核', approved: '退款处理中', processing: '退款处理中', succeeded: '退款结果核对中', failed: '退款异常，核对中', rejected: '退款结果核对中', cancelled: '退款结果核对中' })[refund.platform_status] ?? '退款处理中'
}
