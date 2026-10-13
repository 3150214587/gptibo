import './store-settings.css'
import { useEffect, useState } from 'react'
import { Check, Mail, RefreshCw } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Feedback } from '@/components/workspace-loading'
import { api, errorMessage } from '@/lib/api'
import { useResource } from '@/lib/use-resource'

type Settings = { orderCompletedEmail: boolean; recipient: string; version: number }

function NotificationForm({ endpoint, settings, readOnly, loading, onSaved, onEditing }: { endpoint: string; settings: Settings; readOnly: boolean; loading: boolean; onSaved: () => void; onEditing: () => void }) {
  const [enabled, setEnabled] = useState(settings.orderCompletedEmail)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  async function save() {
    if (busy || loading || readOnly || enabled === settings.orderCompletedEmail) return
    setBusy(true); setError('')
    try { await api<Settings>(endpoint, { orderCompletedEmail: enabled, version: settings.version }); onSaved() }
    catch (e) { setError(errorMessage(e)) } finally { setBusy(false) }
  }
  return <form onSubmit={event => { event.preventDefault(); void save() }}>
    <label className="partner-notification-toggle"><Checkbox checked={enabled} onCheckedChange={value => { setEnabled(value); setError(''); onEditing() }} disabled={busy || loading || readOnly} /><span>订单完成时，给我发送邮件通知</span></label>
    <p className="partner-notification-recipient">接收邮箱 <strong>{settings.recipient}</strong><span>使用你的代理登录邮箱。</span></p>
    <Feedback error={error} />
    {!readOnly && <Button type="submit" disabled={busy || loading || enabled === settings.orderCompletedEmail}>{busy ? '保存中…' : '保存通知设置'}</Button>}
  </form>
}

export function NotificationSettings({ endpoint, revision, readOnly, onSummaryChange }: { endpoint: string; revision: number; readOnly: boolean; onSummaryChange?: (summary: string) => void }) {
  const [localRevision, setLocalRevision] = useState(0)
  const [message, setMessage] = useState('')
  const { data, error, loading } = useResource<Settings>(endpoint, revision + localRevision)
  useEffect(() => { onSummaryChange?.(error ? '读取失败，展开后重试' : data ? data.orderCompletedEmail ? `已开启 · ${data.recipient}` : '未开启 · 订单完成后发送邮件' : '正在读取…') }, [data, error, onSummaryChange])
  return <section className="partner-notification-settings">
    <div className="partner-notification-heading"><h3><Mail size={18} />订单邮件通知</h3><Button type="button" variant="ghost" size="icon" aria-label="刷新通知设置" disabled={loading} onClick={() => { setMessage(''); setLocalRevision(value => value + 1) }}><RefreshCw size={15} /></Button></div>
    <p className="partner-notification-description">开启后，你的商店每完成一笔正式订单，都会收到包含商品、金额、客户邮箱和订单号的邮件。仅通知开启后完成的订单。</p>
    <Feedback error={error} loading={loading} data={data} shape="compact" />
    {data && <NotificationForm key={endpoint + ':' + data.version} endpoint={endpoint} settings={data} readOnly={readOnly} loading={loading} onEditing={() => setMessage('')} onSaved={() => { setMessage('通知设置已保存'); setLocalRevision(value => value + 1) }} />}
    {message && <p className="partner-notification-success" role="status"><Check size={14} />{message}</p>}
  </section>
}
