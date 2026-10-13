import { StorefrontI18nProvider } from './lib/storefront-i18n-provider'
import { WorkspaceBoot } from './components/workspace-loading'
import { lazy, Suspense } from 'react'
import App from './App'

const Admin = lazy(() => import('./admin/Admin'))
const Partner = lazy(() => import('./partner/Partner'))
export default function Root() {
  return location.pathname === '/admin' || location.pathname.startsWith('/admin/')
    ? <Suspense fallback={<WorkspaceBoot />}><Admin /></Suspense>
    : ['/join', '/join/', '/partner'].includes(location.pathname)
      ? <Suspense fallback={<WorkspaceBoot />}><Partner /></Suspense>
      : <StorefrontI18nProvider><App /></StorefrontI18nProvider>
}
