import { useEffect } from 'react'
import { readStorefrontPage } from '@/lib/storefront-routing'
import './site-closed.css'

// Custom domains and referral stores retain their existing storefronts.
export function isClosedHomepage(current: Pick<Location, 'hostname' | 'pathname' | 'search' | 'hash'>) {
  const platform = ['shop.example.invalid', 'localhost', '127.0.0.1', '[::1]'].includes(current.hostname)
  return platform && current.pathname === '/' && readStorefrontPage(current) === 'store'
}

export function SiteClosed() {
  useEffect(() => {
    document.title = '本站已关停'
    document.querySelector('meta[name="description"]')?.setAttribute('content', '本站已关停')
  }, [])

  return <main className="site-closed"><h1>本站已关停</h1></main>
}
