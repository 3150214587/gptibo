import { englishMessages } from './storefront-messages.ts'

export type StorefrontLocale = 'zh-CN' | 'en'
export const STOREFRONT_LOCALE_KEY = 'ymx-locale'

export function resolveStorefrontLocale(saved: string | null, languages: readonly string[]): StorefrontLocale {
  if (saved === 'zh-CN' || saved === 'en') return saved
  for (const language of languages) {
    if (/^en(?:-|$)/i.test(language)) return 'en'
    if (/^zh(?:-|$)/i.test(language)) return 'zh-CN'
  }
  return 'zh-CN'
}

export function translateStorefront(locale: StorefrontLocale, message: string, values: Record<string, string | number> = {}) {
  const translated = locale === 'en' && Object.hasOwn(englishMessages, message) ? englishMessages[message] : message
  return translated.replace(/\{(\w+)\}/g, (match, key: string) => Object.hasOwn(values, key) ? String(values[key]) : match)
}
