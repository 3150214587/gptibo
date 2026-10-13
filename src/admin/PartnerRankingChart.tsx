import { useState, type CSSProperties } from 'react'
import { ChartNoAxesColumnIncreasing } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Feedback } from '@/components/workspace-loading'
import { useResource } from '@/lib/use-resource'
import './overview-charts.css'

type Ranking = {
  total: number
  orderCount: number
  sellingPartners: number
  rows: { id: string; rank: number; email: string; wechat: string; active: number; orderCount: number }[]
}
const periods = [{ days: 1, label: '今天' }, { days: 7, label: '7 天' }, { days: 30, label: '30 天' }]

export default function PartnerRankingChart({ revision }: { revision: number }) {
  const [days, setDays] = useState(7)
  const [page, setPage] = useState(1)
  const { data, error, loading } = useResource<Ranking>(`/api/admin/partners/ranking?days=${days}&page=${page}`, revision)
  const max = Math.max(1, ...(data?.rows.map(row => row.orderCount) ?? []))

  return <section className="admin-panel partner-ranking-chart" aria-label="代理出单排行" aria-busy={loading}>
    <header className="partner-ranking-heading">
      <div><h2><ChartNoAxesColumnIncreasing size={17} aria-hidden="true" />代理出单排行</h2><p>已付款且履约成功 · 北京时间</p></div>
      <div className="visitor-period" role="group" aria-label="排行时间范围">
        {periods.map(period => <Button key={period.days} variant="ghost" size="sm" className="visitor-period-btn" aria-pressed={days === period.days} onClick={() => { setDays(period.days); setPage(1) }}>{period.label}</Button>)}
      </div>
    </header>
    <div className="partner-ranking-totals" aria-live="polite">
      {data ? <><span>完成 <strong>{data.orderCount.toLocaleString('zh-CN')}</strong> 单</span><span>{data.sellingPartners} / {data.total} 位代理有出单</span></> : <span>{error ? '数据暂不可用' : '正在加载排行…'}</span>}
    </div>
    <div key={`${days}-${page}`} className="partner-ranking-body">
      <Feedback error={error} loading={loading} data={data} shape="compact" />
      {data && (data.rows.length ? <ol className="partner-ranking-bars" aria-label="按出单量从高到低排列">
        {data.rows.map(partner => <li key={partner.id} className="partner-ranking-row" data-zero={partner.orderCount === 0}>
          <span className="partner-ranking-position">{partner.rank}</span>
          <div className="partner-ranking-agent"><span className="partner-ranking-email" title={partner.email}>{partner.email}</span><small>微信：{partner.wechat || '未填写'}{!partner.active && ' · 已停用'}</small></div>
          <span className="partner-ranking-count"><strong>{partner.orderCount.toLocaleString('zh-CN')}</strong><small> 单</small></span>
          <div className="partner-ranking-track" aria-hidden="true"><span style={{ '--bar-width': `${partner.orderCount / max * 100}%` } as CSSProperties} /></div>
        </li>)}
      </ol> : <div className="partner-ranking-empty"><ChartNoAxesColumnIncreasing size={26} aria-hidden="true" /><strong>{data.total ? '本页暂无代理' : '还没有代理'}</strong><p>{data.total ? '返回上一页查看排行。' : '开通代理后，出单数据会显示在这里。'}</p></div>)}
    </div>
    <footer className="partner-ranking-footnote">
      <span>按订单创建时间统计，排除测试与全额退款。</span>
      {data && data.total > 20 && <div className="partner-ranking-pagination"><span>{page} / {Math.ceil(data.total / 20)}</span><Button variant="ghost" size="sm" disabled={loading || page <= 1} onClick={() => setPage(page - 1)}>上一页</Button><Button variant="ghost" size="sm" disabled={loading || page * 20 >= data.total} onClick={() => setPage(page + 1)}>下一页</Button></div>}
    </footer>
  </section>
}
