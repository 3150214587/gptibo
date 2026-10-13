export const PAYMENT_WINDOW_MS = 15 * 60 * 1000

export function paymentDeadline(createdAt: number, supplierExpiry?: number | null) {
  return Math.min(createdAt + PAYMENT_WINDOW_MS, supplierExpiry != null && Number.isFinite(supplierExpiry) ? supplierExpiry : Infinity)
}
