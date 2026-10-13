export const xgiftPlans = { 'x-premium-3m': { months: 3, priceCents: 3500, points: 1700 }, 'x-premium-6m': { months: 6, priceCents: 5500, points: 3400 } } as const
export const isXGiftPlan = (plan: string): plan is keyof typeof xgiftPlans => Object.hasOwn(xgiftPlans, plan)
export function xUsername(value: unknown) {
  if (typeof value !== 'string') return null
  const username = value.trim().replace(/^@/, '').toLowerCase()
  return /^[a-z0-9_]{1,15}$/.test(username) ? username : null
}
