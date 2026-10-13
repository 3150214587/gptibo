import { useRef, useState } from 'react'
import { Mail, Send } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Feedback } from '@/components/workspace-loading'
import { api, errorMessage } from '@/lib/api'
import { useResource } from '@/lib/use-resource'
import type { ServerOrder } from '@/lib/use-store'
import { emailProduct, type AdminDelivery } from '../../shared/email-products'
import '@/styles/membership-products.css'
import { DeliverySupplierOrderPanel } from './DeliverySupplierOrderPanel'

export function EmailDeliveryPanel({ order, onSaved }: { order: ServerOrder; onSaved: () => void }) {
  const endpoint = `/api/admin/orders/${order.id}/delivery`
  const { data, error, loading } = useResource<{ delivery: AdminDelivery; recipient: string; orderVersion: number }>(endpoint, order.version)
  if (!data) return <Feedback error={error} loading={loading} data={data} shape="compact" />
  return <>{data.delivery.automatic && <DeliverySupplierOrderPanel order={order} onSaved={onSaved} />}<DeliveryForm key={data.delivery.version + ':' + data.orderVersion} order={order} onSaved={onSaved} data={data} /></>
}

function DeliveryForm({ order, onSaved, data }: { order: ServerOrder; onSaved: () => void; data: { delivery: AdminDelivery; recipient: string; orderVersion: number } }) {
  const endpoint = `/api/admin/orders/${order.id}/delivery`
  const [now] = useState(Date.now)
  const [value, setValue] = useState(data.delivery.value), [instructions, setInstructions] = useState(data.delivery.instructions), [confirmed, setConfirmed] = useState(false), [note, setNote] = useState('')
  const [busy, setBusy] = useState(false), [failure, setFailure] = useState('')
  const lock = useRef(false)
  const product = emailProduct(order.plan)!
  const editable = order.plan !== 'gmail-account' && !data.delivery.automatic && ['waiting', 'draft'].includes(data.delivery.status)
  const dirty = value !== data?.delivery.value || instructions !== data?.delivery.instructions
  const canDeliver = order.paymentStatus === 'paid' && !order.isTest && !order.refundHold && !order.refundedCents && !order.redemptionBlocked && ['session', 'processing'].includes(order.status)
  async function submit(action: 'save' | 'send' | 'confirm-sent') {
    if (!data || lock.current) return
    lock.current = true; setBusy(true); setFailure('')
    try { await api(endpoint, { action, value, instructions, version: data.delivery.version, orderVersion: data.orderVersion, note }); onSaved() }
    catch (e) { setFailure(errorMessage(e)); if (action !== 'save') onSaved() }
    finally { lock.current = false; setBusy(false) }
  }
  return <section className="email-delivery-panel" aria-label="邮件发货">
    <h3><Mail size={16} className="inline-block" /> 邮件发货 · {product.label}</h3>
    <p>收件邮箱</p><p className="email-delivery-recipient">{order.customerEmail}</p>
    <Feedback error={failure} loading={false} data={data} shape="compact" />
    {data && <>
      {data.delivery.status === 'sent' ? <p role="status">已发货 · {data.delivery.sentAt && new Date(data.delivery.sentAt).toLocaleString('zh-CN')}<br />{order.plan === 'gmail-account' ? '邮件服务已接收，实际账号登录结果请自行核对。' : '邮件服务已接收；这不代表客户已经领取或激活会员。'}</p> : !canDeliver ? <p>付款确认后即可填写发货资料。退款或暂停中的订单无法发货。</p> : <>
        {['unknown', 'sending'].includes(data.delivery.status) && <p role="alert">原邮件发送结果待核对。安全时间内重试会复用同一封邮件，不替换内容。超过 23 小时请核查邮件服务记录。</p>}
        {(order.plan === 'gmail-account' || data.delivery.automatic) && data.delivery.status === 'waiting' && <p>系统会在付款确认后自动采购；发货资料由原采购单生成。</p>}
        <label htmlFor="delivery-value">{product.label}</label>
        <Input id="delivery-value" type={product.kind === 'link' ? 'url' : 'text'} value={value} onChange={e => { setValue(e.target.value); setConfirmed(false) }} maxLength={product.kind === 'link' ? 2048 : 512} disabled={busy || !editable} placeholder={product.kind === 'link' ? 'https://…' : '输入本订单专属兑换 CDK'} autoComplete="off" />
        <label htmlFor="delivery-instructions">本次领取说明</label><textarea id="delivery-instructions" value={instructions} onChange={e => { setInstructions(e.target.value); setConfirmed(false) }} maxLength={2000} rows={4} disabled={busy || !editable} />
        <div className="email-delivery-actions">{editable && <Button variant="outline" disabled={busy || !value.trim() || !instructions.trim()} onClick={() => void submit('save')}>{busy ? '处理中…' : '保存并预览邮件'}</Button>}</div>
      </>}
      {data.delivery.preview && <details className="email-delivery-mail" open={data.delivery.status === 'draft'}><summary>邮件预览：{data.delivery.preview.subject}</summary><iframe title="发货邮件预览" className="email-delivery-preview" sandbox="" referrerPolicy="no-referrer" srcDoc={data.delivery.preview.html} /></details>}
      {canDeliver && data.delivery.preview && data.delivery.status !== 'sent' && <>
        <label className="admin-checkbox"><input type="checkbox" checked={confirmed} onChange={e => setConfirmed(e.target.checked)} disabled={busy || dirty} />已核对收件邮箱、{product.label}和邮件说明，确认发货</label>
        <div className="email-delivery-actions"><Button disabled={busy || dirty || !confirmed || (data.delivery.firstAttemptAt !== null && now - data.delivery.firstAttemptAt >= 23 * 3600000)} onClick={() => void submit('send')}><Send size={14} />{busy ? '正在处理…' : data.delivery.firstAttemptAt ? '核对并重试原邮件' : '确认发送发货邮件'}</Button></div>
        {data.delivery.firstAttemptAt !== null && <details><summary>已在邮件服务后台核实原邮件发出</summary><label htmlFor="delivery-check-note">核查说明（填写邮件记录或核查结果）</label><Input id="delivery-check-note" value={note} onChange={e => setNote(e.target.value)} maxLength={500} disabled={busy} /><div className="email-delivery-actions"><Button variant="outline" disabled={busy || !confirmed || note.trim().length < 4} onClick={() => void submit('confirm-sent')}>登记原邮件已发送</Button></div><p>仅更新核验结果，不再发送邮件。</p></details>}
      </>}
    </>}
  </section>
}
