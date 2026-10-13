import { useEffect, useRef, useState } from 'react'
import { MessageSquare, Plus, RefreshCw } from 'lucide-react'
import { api, errorMessage, RequestError } from '@/lib/api'
import { useResource } from '@/lib/use-resource'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Feedback } from '@/components/workspace-loading'
import { WorkspaceSelect } from '@/partner/workspace-ui'
import './ticket-workspace.css'

const statuses = { open: '待处理', processing: '处理中', waiting: '待代理回复', resolved: '已解决' }
type Status = keyof typeof statuses
const options = Object.entries(statuses).map(([value, label]) => ({ value, label }))
const date = (value: number) => new Date(value).toLocaleString('zh-CN', { timeZone: 'Asia/Shanghai', hour12: false })
type Ticket = { id: string; order_id: string; subject: string; status: Status; version: number; created_at: number; updated_at: number; partner_name: string; partner_email: string }
type Detail = Ticket & { events: { id: string; actor_role: string; body: string; status: Status; created_at: number }[] }

function useTicketRefresh(active: boolean) {
  const [revision, setRevision] = useState(0)
  useEffect(() => {
    if (!active) return
    const refresh = () => { if (!document.hidden) setRevision(v => v + 1) }
    const timer = window.setInterval(refresh, 15000)
    window.addEventListener('focus', refresh)
    return () => { window.clearInterval(timer); window.removeEventListener('focus', refresh) }
  }, [active])
  return [revision, () => setRevision(v => v + 1)] as const
}

export function TicketCreator({ orderId = '', onClose, onCreated }: { orderId?: string; onClose: () => void; onCreated: (id: string) => void }) {
  const [order, setOrder] = useState(orderId)
  const [subject, setSubject] = useState('')
  const [body, setBody] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const attempt = useRef<{ fingerprint: string; key: string } | null>(null)
  async function submit() {
    if (busy) return
    const payload = { orderId: order.trim(), subject: subject.trim(), body: body.trim() }
    const fingerprint = JSON.stringify(payload)
    if (attempt.current?.fingerprint !== fingerprint) attempt.current = { fingerprint, key: crypto.randomUUID() }
    setBusy(true); setError('')
    try {
      const result = await api<{ id: string }>('/api/partner/tickets', { ...payload, key: attempt.current.key })
      onCreated(result.id)
    } catch (e) { setError(errorMessage(e)) } finally { setBusy(false) }
  }
  return <Dialog open onOpenChange={open => { if (!open && !busy) onClose() }}><DialogContent className="ticket-dialog motion-dialog" showCloseButton={!busy}>
    <DialogHeader><DialogTitle>提交订单工单</DialogTitle><DialogDescription>向平台描述订单问题，处理进度和回复都会保存在工单中。</DialogDescription></DialogHeader>
    <form className="ticket-form" onSubmit={e => { e.preventDefault(); void submit() }}>
      <label htmlFor="ticket-order">本站订单号</label><Input id="ticket-order" value={order} onChange={e => setOrder(e.target.value)} required maxLength={128} disabled={busy || Boolean(orderId)} placeholder="从订单明细复制本站订单号" />
      <label htmlFor="ticket-subject">问题标题</label><Input id="ticket-subject" value={subject} onChange={e => setSubject(e.target.value)} required maxLength={120} disabled={busy} placeholder="例如：付款后一直未完成充值" />
      <label htmlFor="ticket-description">问题描述</label><Textarea id="ticket-description" value={body} onChange={e => setBody(e.target.value)} required maxLength={5000} rows={6} disabled={busy} placeholder="请说明遇到的问题、发生时间、页面提示以及希望如何处理。请勿填写密码或 Session。" />
      <Feedback error={error} /><Button type="submit" disabled={busy || !order.trim() || !subject.trim() || !body.trim()}>{busy ? '提交中…' : '提交工单'}</Button>
    </form>
  </DialogContent></Dialog>
}

export function TicketDetail({ id, admin = false, onClose, onSaved, onOrder }: { id: string; admin?: boolean; onClose: () => void; onSaved?: () => void; onOrder?: (id: string) => void }) {
  const [revision, refresh] = useTicketRefresh(true)
  const path = `/api/${admin ? 'admin' : 'partner'}/tickets/${encodeURIComponent(id)}`
  const { data, error, loading } = useResource<Detail>(path, revision, { retainOnError: true })
  const [body, setBody] = useState('')
  const [status, setStatus] = useState('waiting')
  const [busy, setBusy] = useState(false)
  const [saveError, setSaveError] = useState('')
  const [notice, setNotice] = useState('')
  const attempt = useRef<{ fingerprint: string; key: string; version: number } | null>(null)
  async function reply() {
    if (!data || busy) return
    const payload = { body: body.trim(), status: admin ? status : 'open' }
    const fingerprint = JSON.stringify(payload)
    if (attempt.current?.fingerprint !== fingerprint) attempt.current = { fingerprint, key: crypto.randomUUID(), version: data.version }
    setBusy(true); setSaveError(''); setNotice('')
    try {
      await api(path + '/replies', { ...payload, key: attempt.current.key, version: attempt.current.version })
      attempt.current = null; setBody(''); setNotice('回复已保存'); refresh(); onSaved?.()
    } catch (e) {
      setSaveError(errorMessage(e))
      if (e instanceof RequestError && e.status === 409) { attempt.current = null; refresh() }
    } finally { setBusy(false) }
  }
  return <Dialog open onOpenChange={open => { if (!open && !busy) onClose() }}><DialogContent className="ticket-dialog motion-dialog" showCloseButton={!busy}>
    <DialogHeader><DialogTitle>{data?.subject ?? '工单详情'}</DialogTitle><DialogDescription>工单 {id}</DialogDescription></DialogHeader>
    <Feedback error={error} loading={loading && !data} />
    {data && <>
      <div className="ticket-meta"><Badge variant="secondary">{statuses[data.status]}</Badge><span>订单：{data.order_id}</span>{admin && <span>{data.partner_name} · {data.partner_email}</span>}{onOrder && <Button size="sm" variant="outline" onClick={() => onOrder(data.order_id)}>查看关联订单</Button>}</div>
      <ol className="ticket-messages">{data.events.map(event => <li key={event.id} data-role={event.actor_role}><div><strong>{event.actor_role === 'admin' ? '平台客服' : '代理'}</strong><time>{date(event.created_at)}</time></div><p>{event.body}</p><small>{statuses[event.status]}</small></li>)}</ol>
      <form className="ticket-form" onSubmit={e => { e.preventDefault(); void reply() }}>
        <label htmlFor="ticket-reply">{admin ? '处理回复' : data.status === 'resolved' ? '仍有问题？追加说明后重新处理' : '补充问题 / 回复平台'}</label>
        <Textarea id="ticket-reply" value={body} onChange={e => setBody(e.target.value)} maxLength={5000} rows={4} required disabled={busy} placeholder={admin ? '填写处理进展或解决结果，代理可直接看到。' : '补充问题详情，请勿填写密码或 Session。'} />
        {admin && <WorkspaceSelect label="回复后的工单状态" value={status} onValueChange={setStatus} options={options} disabled={busy} />}
        <Feedback error={saveError} />{notice && <p role="status">{notice}</p>}
        <div className="ticket-actions"><Button type="submit" disabled={busy || loading || Boolean(error) || !body.trim()}>{busy ? '保存中…' : admin ? '发送回复并更新状态' : '发送回复'}</Button><Button type="button" variant="outline" disabled={busy || loading} onClick={refresh}><RefreshCw size={14} />刷新</Button></div>
      </form>
    </>}
    {!data && !loading && <Button variant="outline" onClick={refresh}>重新读取</Button>}
  </DialogContent></Dialog>
}

export default function TicketWorkspace({ admin = false, revision = 0, active = true, onOrder }: { admin?: boolean; revision?: number; active?: boolean; onOrder?: (id: string) => void }) {
  const [page, setPage] = useState(1)
  const [draft, setDraft] = useState('')
  const [query, setQuery] = useState('')
  const [status, setStatus] = useState('')
  const [selected, setSelected] = useState<string>()
  const [creating, setCreating] = useState(false)
  const [poll, refresh] = useTicketRefresh(active)
  const params = new URLSearchParams({ page: String(page), q: query, status })
  const { data, error, loading } = useResource<{ rows: Ticket[]; total: number }>(`/api/${admin ? 'admin' : 'partner'}/tickets?${params}`, revision + poll)
  return <section className="admin-panel ticket-workspace">
    <div className="admin-panel-title"><div><h2>{admin ? '代理工单' : '我的工单'}</h2><p>{admin ? '统一处理订单问题，回复会同步至代理工作台。' : '订单遇到问题，直接提交工单并在这里跟进。'}</p></div>{!admin && <Button onClick={() => setCreating(true)}><Plus size={15} />提交工单</Button>}</div>
    <form className="ticket-filters" onSubmit={e => { e.preventDefault(); setQuery(draft.trim()); setPage(1) }}><Input aria-label="搜索工单" placeholder={admin ? '订单号、标题或代理邮箱' : '订单号或问题标题'} value={draft} maxLength={320} onChange={e => setDraft(e.target.value)} /><WorkspaceSelect label="工单状态" value={status} onValueChange={value => { setStatus(value); setPage(1) }} options={[{ value: '', label: '全部状态' }, ...options]} /><Button type="submit" variant="outline">搜索</Button><Button type="button" variant="ghost" onClick={refresh} aria-label="刷新工单"><RefreshCw size={16} /></Button></form>
    <Feedback error={error} loading={loading && !data} />
    {data && <>{data.rows.length ? <div className="ticket-list">{data.rows.map(ticket => <button className="ticket-row" key={ticket.id} onClick={() => setSelected(ticket.id)}><div><strong>{ticket.subject}</strong><Badge variant={ticket.status === 'open' ? 'default' : 'secondary'}>{statuses[ticket.status]}</Badge></div><span>订单 {ticket.order_id}</span>{admin && <span>{ticket.partner_name} · {ticket.partner_email}</span>}<small>更新于 {date(ticket.updated_at)}</small></button>)}</div> : <div className="admin-empty"><MessageSquare size={26} /><h3>{query || status ? '没有符合条件的工单' : '暂无工单'}</h3><p>{admin ? '代理提交的问题会显示在这里。' : '可从订单明细直接发起工单，也可以在这里填写订单号。'}</p></div>}<div className="admin-pagination"><span>共 {data.total} 条 · 第 {page} 页</span><div><Button variant="outline" disabled={page <= 1} onClick={() => setPage(v => v - 1)}>上一页</Button><Button variant="outline" disabled={page * 20 >= data.total} onClick={() => setPage(v => v + 1)}>下一页</Button></div></div></>}
    {creating && <TicketCreator onClose={() => setCreating(false)} onCreated={id => { setCreating(false); setSelected(id); setPage(1); setStatus(''); setQuery(''); setDraft(''); refresh() }} />}
    {selected && active && <TicketDetail key={selected} id={selected} admin={admin} onClose={() => setSelected(undefined)} onSaved={refresh} onOrder={onOrder ? id => { setSelected(undefined); onOrder(id) } : undefined} />}
  </section>
}
