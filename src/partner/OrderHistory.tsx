import { activityCopy } from '@/lib/activity-copy'

export type PartnerOrderEvent = {
  id: string
  actor: 'customer' | 'system' | 'admin'
  action: string
  to_status: string
  note: string
  created_at: number
}

const historyTime = new Intl.DateTimeFormat('zh-CN', {
  timeZone: 'Asia/Shanghai', year: 'numeric', month: '2-digit', day: '2-digit',
  hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false,
})
const actors = { customer: '用户', system: '系统', admin: '管理员' }

export function OrderHistory({ events, labels, loading, error }: {
  events?: PartnerOrderEvent[]
  labels: Record<string, string>
  loading: boolean
  error: string
}) {
  return <section className="partner-order-history" aria-label="订单处理记录" aria-busy={loading}>
    <div className="partner-order-history-heading"><h3>处理记录</h3><span>北京时间</span></div>
    {events === undefined
      ? <p>{error ? '处理记录读取失败，请刷新重试。' : '正在读取处理记录…'}</p>
      : events.length === 0
        ? <p>暂无处理记录。</p>
        : <ol className="admin-timeline">{events.map(event => <li key={event.id}>
          <div><strong>{activityCopy(event.note || event.action)}</strong><time dateTime={new Date(event.created_at).toISOString()}>{historyTime.format(event.created_at)}</time></div>
          <span>{actors[event.actor] ?? '系统'} · {labels[event.to_status] ?? event.to_status}</span>
        </li>)}</ol>}
  </section>
}
