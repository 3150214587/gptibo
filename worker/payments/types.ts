// Business code depends on this contract, never on a gateway's field names.
export type PaymentOrder = { id: string; amountCents: number; subject: string; expiresAt: number }
export type PaymentResult = { orderId: string; amountCents: number; status: 'pending' | 'paid' | 'closed' | 'not_found'; tradeNo?: string }
export type RefundRequest = { id: string; amountCents: number }
export type RefundResult = { refunded: boolean; tradeNo?: string }
export interface PaymentProvider {
  id: string
  account: string
  create(order: PaymentOrder, notifyUrl: string): Promise<{ qr: string }>
  query(order: PaymentOrder): Promise<PaymentResult>
  close(order: PaymentOrder): Promise<boolean>
  refund(order: PaymentOrder, refund: RefundRequest): Promise<RefundResult>
  queryRefund(order: PaymentOrder, refund: RefundRequest): Promise<RefundResult>
  notification(request: Request): Promise<PaymentResult>
  acknowledge(accepted: boolean): Response
}
export function amountText(cents: number) {
  if (!Number.isSafeInteger(cents) || cents < 1 || cents > 10000000) throw new Error('Invalid payment amount')
  return `${Math.floor(cents / 100)}.${String(cents % 100).padStart(2, '0')}`
}
export function amountCents(raw: unknown) {
  const match = /^(0|[1-9]\d{0,7})(?:\.(\d{1,2}))?$/.exec(String(raw ?? ''))
  if (!match) throw new Error('Invalid gateway amount')
  return Number(match[1]) * 100 + Number((match[2] ?? '').padEnd(2, '0'))
}
