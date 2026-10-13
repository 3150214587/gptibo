import { createContext, useContext } from 'react'
import { translateStorefront, type StorefrontLocale } from './storefront-locale'

export type StorefrontI18n = { locale: StorefrontLocale; setLocale: (locale: StorefrontLocale) => void; t: (message: string, values?: Record<string, string | number>) => string }
// Shared dialogs also appear in Chinese-only workspaces, outside this provider.
export const StorefrontI18nContext = createContext<StorefrontI18n>({ locale: 'zh-CN', setLocale: () => {}, t: (message, values) => translateStorefront('zh-CN', message, values) })

export function useStorefrontI18n() { return useContext(StorefrontI18nContext) }
