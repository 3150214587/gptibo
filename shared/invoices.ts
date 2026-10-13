export const INVOICE_FEE_PERCENT = 5
export const INVOICE_STATUSES = ['awaiting_payment', 'awaiting_submission', 'submitted', 'completed'] as const
export type InvoiceStatus = typeof INVOICE_STATUSES[number]
export const INVOICE_STATUS_LABELS: Record<InvoiceStatus, string> = {
  awaiting_payment: '待支付手续费', awaiting_submission: '待审核', submitted: '待开票', completed: '已开票',
}
export type Invoice = {
  id: string; partner_id: string; partner_name?: string; partner_email?: string
  title: string; tax_number: string; invoice_type: 'ordinary' | 'special'; email: string
  address: string; phone: string; bank_name: string; bank_account: string; note: string
  base_cents: number; fee_cents: number; include_fee: number; invoice_cents: number
  status: InvoiceStatus; paid_at: number | null; submitted_at: number | null; completed_at: number | null
  platform_mode: 'manual'; platform_reference: string; admin_note: string; version: number
  created_at: number; updated_at: number
}
export type InvoicePaymentView = {
  provider?: string
  status: 'pending' | 'paid' | 'closed'; qr: string | null; expiresAt: number; amountCents: number
}
export type InvoiceEvent = { id: string; action: string; actor: string; note: string; created_at: number }
export type InvoiceDetail = { invoice: Invoice; events: InvoiceEvent[]; payment: InvoicePaymentView | null }
export type InvoiceList = { rows: Invoice[]; total: number; page: number }

// All money is integer cents. Round the 5% fee once, to the nearest cent.
export function invoiceAmounts(baseCents: number, includeFee: boolean) {
  if (!Number.isSafeInteger(baseCents) || baseCents < 10 || baseCents > 10000000) throw new Error('原始金额须为 0.10–100000 元，最多两位小数。')
  const feeCents = Math.floor((baseCents * INVOICE_FEE_PERCENT + 50) / 100)
  return { baseCents, feeCents, invoiceCents: baseCents + (includeFee ? feeCents : 0) }
}
export function parseInvoiceAmount(value: string) {
  const match = /^(0|[1-9]\d{0,5})(?:\.(\d{1,2}))?$/.exec(value.trim())
  if (!match) return null
  const cents = Number(match[1]) * 100 + Number((match[2] ?? '').padEnd(2, '0'))
  try { invoiceAmounts(cents, false); return cents } catch { return null }
}
