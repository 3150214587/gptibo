import { referralCodePattern } from '../../shared/partner-link.ts'
type StorefrontLocation = { pathname: string; search: string; hash: string }

export function storefrontShopSlug(pathname: string) {
  if (pathname.startsWith('/r/')) { const code = pathname.split('/')[2]; return referralCodePattern.test(code ?? '') ? code : '!' }
  return pathname.startsWith('/s/') ? pathname.split('/')[2] || '!' : undefined
}

function basePath(pathname: string) {
  const slug = storefrontShopSlug(pathname)
  return slug === undefined ? '' : `/${pathname.startsWith('/r/') ? 'r' : 's'}/${pathname.split('/')[2] ?? ''}`
}

export function storefrontHref(page: string, current: StorefrontLocation = location) {
  const suffix = page === 'store' ? '/' : page === 'orders' ? '/orders' : `/product/${encodeURIComponent(page.slice('product/'.length))}`
  const base = basePath(current.pathname)
  return (page === 'store' && base ? base : base + suffix) + current.search
}

function pathPage(current: StorefrontLocation) {
  const path = current.pathname.slice(basePath(current.pathname).length).replace(/\/$/, '')
  if (path === '/orders') return 'orders'
  const product = path.match(/^\/product\/([^/]+)$/)
  if (product) {
    try { return `product/${decodeURIComponent(product[1])}` } catch { return `product/${product[1]}` }
  }
  return 'store'
}

export function readStorefrontPage(current: StorefrontLocation = location) {
  const hash = current.hash.slice(1)
  if (hash === 'orders') return 'orders'
  if (['product', 'products'].includes(hash)) return 'store'
  if (/^product\/[a-z0-9_-]+$/i.test(hash)) return hash
  const page = pathPage(current)
  if (page === 'store' && ['help', 'subscriptions', 'how-it-works'].includes(hash)) return 'product/chatgpt'
  return page
}

// Replace known legacy routes while leaving ordinary section anchors intact.
export function canonicalStorefrontUrl(current: StorefrontLocation = location) {
  const legacy = ['#product', '#products', '#orders'].includes(current.hash) || /^#product\/[a-z0-9_-]+$/i.test(current.hash)
  const section = ['#help', '#subscriptions', '#how-it-works'].includes(current.hash) && pathPage(current) === 'store'
  if (!legacy && !section) return null
  return storefrontHref(readStorefrontPage(current), current) + (section ? current.hash : '')
}

// Explicit shop selection accompanies buyer APIs; the server validates the
// relationship against the hostname, never trusting this header as authority.
export function storefrontScopeHeaders(current: Pick<StorefrontLocation, 'pathname'> = location): Record<string, string> {
  const slug = storefrontShopSlug(current.pathname)
  return slug === undefined ? {} : { 'X-Storefront-Shop': slug }
}
