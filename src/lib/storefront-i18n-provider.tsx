import { StorefrontI18nContext, type StorefrontI18n } from './storefront-i18n'
import { useEffect, useState, type ReactNode } from 'react'
import { resolveStorefrontLocale, STOREFRONT_LOCALE_KEY, translateStorefront, type StorefrontLocale } from './storefront-locale'

function initialLocale() {
  let saved = null
  try { saved = localStorage.getItem(STOREFRONT_LOCALE_KEY) } catch { /* Storage can be disabled. */ }
  return resolveStorefrontLocale(saved, navigator.languages ?? [navigator.language])
}

export function StorefrontI18nProvider({ children }: { children: ReactNode }) {
  const [locale, updateLocale] = useState<StorefrontLocale>(initialLocale)
  const t: StorefrontI18n['t'] = (message, values) => translateStorefront(locale, message, values)
  function setLocale(next: StorefrontLocale) {
    updateLocale(next)
    try { localStorage.setItem(STOREFRONT_LOCALE_KEY, next) } catch { /* Keep the choice for this visit. */ }
  }
  useEffect(() => {
    document.documentElement.lang = locale
  }, [locale])
  return <StorefrontI18nContext.Provider value={{ locale, setLocale, t }}>{children}</StorefrontI18nContext.Provider>
}
