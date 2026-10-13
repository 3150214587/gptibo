// Explicit markers keep brand identity separate from product names and customer text.
export const emailBrand = {
  name: '__RECHARGE_BRAND_NAME__',
  logo: '__RECHARGE_BRAND_LOGO__',
  orders: '__RECHARGE_BRAND_ORDERS_URL__',
  workspace: '__RECHARGE_BRAND_WORKSPACE_URL__',
  admin: '__RECHARGE_BRAND_ADMIN_URL__',
}
export const platformEmailFields: Record<string, string> = {
  BRAND_NAME: 'GPTibo',
  BRAND_LOGO: 'https://shop.example.invalid/email-logo.png',
  BRAND_ORDERS_URL: 'https://shop.example.invalid/orders',
  BRAND_WORKSPACE_URL: 'https://shop.example.invalid/join',
  BRAND_ADMIN_URL: 'https://shop.example.invalid/admin',
}
export function platformEmail(value: string) {
  return value.replace(/__RECHARGE_(BRAND_[A-Z_]+)__/g, (marker, key: string) => platformEmailFields[key] ?? marker)
}
