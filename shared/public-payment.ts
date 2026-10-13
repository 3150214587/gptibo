// Only the official Alipay QR payload may cross the storefront boundary.
// Supplier cashier URLs and their access tokens remain server-side.
export function publicAlipayQr(value: unknown): string | null {
  return typeof value === 'string' && value === value.trim() && /^https:\/\/qr\.alipay\.com\/[A-Za-z0-9]{8,128}$/.test(value) ? value : null
}
