import { emailProduct } from '../../shared/email-products'
import { useState } from 'react'
import { Check } from 'lucide-react'
import { Checkbox } from '@base-ui/react/checkbox'
import { Button } from '@/components/ui/button'
import { Feedback } from '@/components/workspace-loading'
import { api, errorMessage } from '@/lib/api'
import type { ServerOrder } from '@/lib/use-store'
import { canRecoverXGift } from './xgift-recovery'

export function OrderActionForm({ order, loading, onSaved, onRefresh }: { order: ServerOrder; loading: boolean; onSaved: (message: string) => void; onRefresh: () => void }) {
  const [note, setNote] = useState('')
  const [action, setAction] = useState('note')
  const [blocked, setBlocked] = useState(Boolean(order.redemptionBlocked))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const changed = blocked !== Boolean(order.redemptionBlocked)
  async function submit() {
    if (busy || loading || !note.trim()) return
    setBusy(true); setError('')
    try {
      await api(`/api/admin/orders/${order.id}/actions`, { action: changed ? 'redemption-control' : action, blocked, note, version: order.version })
      setNote(''); setAction('note')
      onSaved(changed ? blocked ? '已禁止再次兑换' : '已解除禁止兑换' : action === 'xgift-recovery' ? 'X 补发已受理，请刷新核对交付结果' : '处理记录已保存')
    } catch (e) { setError(errorMessage(e)) }
    finally { setBusy(false) }
  }
  return <form className="admin-note-form" onSubmit={e => { e.preventDefault(); void submit() }}>
    <h3>添加处理记录</h3>
    <select aria-label="处理方式" value={action} onChange={e => setAction(e.target.value)} disabled={busy || loading || changed}>
      <option value="note">仅添加内部备注</option>
      {canRecoverXGift(order) && <option value="xgift-recovery">补发 X 赠送</option>}
      {order.manualFulfillment && !emailProduct(order.plan) && order.mode === 'live' && !order.isTest && order.paymentStatus === 'paid' && !order.redemptionBlocked && !order.refundHold && (order.refundedCents ?? 0) < Math.round(order.price * 100) && <>
        {order.status === 'session' && <option value="manual-start">开始人工开通</option>}
        {['session', 'processing'].includes(order.status) && <option value="manual-complete">确认人工开通完成</option>}
      </>}
      {order.mode === 'demo' && !order.redemptionBlocked && order.status === 'pending' && <option value="cancel">关闭未付款记录</option>}
      {order.mode === 'demo' && !order.redemptionBlocked && order.status === 'failed' && !order.refundHold && !order.refundedCents && <option value="retry">重置历史记录</option>}
    </select>
    {action === 'xgift-recovery' && <p className="admin-subtle">提交时会重新核验原供货未付款、收款和退款状态。受理后请刷新查看补发结果。</p>}
    <div className="admin-redemption-control">
      <label className="admin-checkbox">
        <Checkbox.Root className="admin-check-control" checked={blocked} onCheckedChange={value => { setBlocked(Boolean(value)); setAction('note') }} disabled={busy || loading} aria-describedby="admin-redemption-help">
          <Checkbox.Indicator><Check size={13} /></Checkbox.Indicator>
        </Checkbox.Root>
        禁止再次兑换
      </label>
      <p id="admin-redemption-help" className="admin-subtle">人工退款或作废处理后勾选，保存后客户不能再提交或重试。此操作不会发起退款。</p>
    </div>
    <label htmlFor="admin-order-note">处理说明</label>
    <textarea id="admin-order-note" value={note} onChange={e => setNote(e.target.value)} maxLength={500} rows={3} placeholder="例如：已人工退款，或作废不退款。不要填写账号密码或 Session。" disabled={busy || loading} />
    <Feedback error={error} />
    <div>
      <Button type="button" variant="outline" onClick={onRefresh} disabled={busy || loading}>刷新详情</Button>
      <Button type="submit" disabled={busy || loading || !note.trim()}>{busy ? '提交中…' : changed ? blocked ? '保存并禁止兑换' : '保存并解除禁止' : action === 'note' ? '保存备注' : action === 'xgift-recovery' ? '确认补发 X' : '确认处理'}</Button>
    </div>
  </form>
}
