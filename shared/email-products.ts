import { isXGiftPlan } from './xgift-products.ts'
export const EMAIL_PRODUCTS = {
  'gmail-account': { productId: 'gmail', name: 'Gmail 邮箱账号', kind: 'cdk', label: '账号资料', duration: '1 个账号' },
  'gemini-pro-18m': { productId: 'gemini', name: 'Gemini Pro 18 个月会员', kind: 'link', label: '领取链接', duration: '18 个月' },
  'duolingo-12m': { productId: 'duolingo', name: '多邻国 1 年会员', kind: 'cdk', label: '兑换 CDK', duration: '1 年' },
} as const
export type EmailPlan = keyof typeof EMAIL_PRODUCTS
export function emailProduct(plan: string) {
  return Object.hasOwn(EMAIL_PRODUCTS, plan) ? EMAIL_PRODUCTS[plan as EmailPlan] : null
}
export function productNameForPlan(plan: string) {
  return isXGiftPlan(plan) ? 'X Premium' : emailProduct(plan)?.name ?? (plan === 'codex-sms-once' ? 'Codex 接码' : 'ChatGPT')
}
export function productNameForId(id: string) {
  return ({ 'x-premium': 'X Premium', chatgpt: 'ChatGPT', 'codex-sms': 'Codex 接码', gemini: 'Gemini Pro', duolingo: '多邻国', gmail: 'Gmail 账号' } as Record<string, string>)[id] ?? id
}
export type DeliverySummary = { automatic?: boolean; value?: string; instructions?: string; kind: 'link' | 'cdk'; status: 'waiting' | 'draft' | 'sending' | 'unknown' | 'sent'; sentAt: number | null }
export type AdminDelivery = DeliverySummary & { value: string; instructions: string; version: number; firstAttemptAt: number | null; providerId: string | null; preview: { subject: string; html: string; text: string } | null }
