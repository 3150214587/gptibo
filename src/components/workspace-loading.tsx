import { useStorefrontI18n } from '@/lib/storefront-i18n'
import { BrandMark } from './brand'
import { CircleAlert } from 'lucide-react'
import { Alert, AlertDescription } from './ui/alert'
import './workspace-loading.css'

export function WorkspaceBoot() {
  return <main className="workspace-boot" aria-busy="true"><div role="status"><BrandMark /><span>正在打开工作台<span aria-hidden="true">…</span></span></div></main>
}

export function Feedback({ error, loading, data, shape = 'table' }: { error?: string; loading?: boolean; data?: unknown; shape?: 'table' | 'overview' | 'detail' | 'compact' }) {
  const { t } = useStorefrontI18n()
  if (error) return <Alert variant="destructive" className="workspace-error border-destructive/25 bg-destructive/5"><CircleAlert aria-hidden="true" /><AlertDescription>{error}</AlertDescription></Alert>
  if (!loading) return null
  if (data !== undefined) return <div className="workspace-refresh" role="status"><span className="sr-only">{t('正在更新数据')}</span><i aria-hidden="true" /></div>
  return <div className={`workspace-skeleton workspace-skeleton-${shape}`} role="status" aria-busy="true"><span className="sr-only">{t('正在加载内容')}</span><div className="workspace-skeleton-visual" aria-hidden="true">{shape === 'overview' && <div className="workspace-skeleton-metrics">{[0, 1, 2, 3].map(i => <div key={i}><i /><b /></div>)}</div>}{Array.from({ length: shape === 'compact' ? 2 : shape === 'detail' ? 5 : 4 }, (_, i) => <div className="workspace-skeleton-row" key={i}><i /><i /><i /></div>)}</div></div>
}
