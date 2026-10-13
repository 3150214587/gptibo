import { useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Feedback } from '@/components/workspace-loading'
import { api, errorMessage } from '@/lib/api'
import { useResource } from '@/lib/use-resource'
import type { ServerOrder } from '@/lib/use-store'

type Purchase = { state: string; supplierOrder: string | null; actualUsdMicros?: number | null; actualUsdCents: number | null; actualCostCents: number | null; cnyPerUsd: number; updatedAt: number }
export function DeliverySupplierOrderPanel({ order, onSaved }: { order: ServerOrder; onSaved: () => void }) {
  const endpoint = `/api/admin/orders/${order.id}/delivery_supplier`
  const resource = useResource<{ purchase: Purchase | null }>(endpoint, order.version)
  const [busy, setBusy] = useState(false), [error, setError] = useState(''), lock = useRef(false)
  const [confirmed, setConfirmed] = useState(false), [note, setNote] = useState('')
  async function sync(resolve = false) {
    if (lock.current) return
    lock.current = true; setBusy(true); setError('')
    try { await api(endpoint, resolve ? { action: 'confirm-not-purchased', confirmed, note, orderVersion: order.version, updatedAt: resource.data?.purchase?.updatedAt } : { action: 'sync' }); onSaved() }
    catch (e) { setError(errorMessage(e)); onSaved() }
    finally { lock.current = false; setBusy(false) }
  }
  const purchase = resource.data?.purchase
  const usd = purchase?.actualUsdMicros != null ? purchase.actualUsdMicros / 1_000_000 : purchase?.actualUsdCents != null ? purchase.actualUsdCents / 100 : null
  const labels: Record<string, string> = { prepared: '等待付款确认或补充库存、余额', creating: '采购处理中', unknown: '原采购结果待核对', pending: '供应商正在准备交付', purchased: '采购已核验，领取资料已交付', rejected: '采购未完成，请处理原订单售后' }
  return <section className="email-delivery-panel" aria-label="自动采购记录">
    <h3>自动采购记录</h3>
    <Feedback error={error || resource.error} loading={resource.loading} data={resource.data} shape="compact" />
    {purchase && <p>{labels[purchase.state]}<br />供应商单号：{purchase.supplierOrder ?? '尚未确认'}<br />成本：{usd !== null ? `${usd.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 6, useGrouping: false })} USDT / ¥${(purchase.actualCostCents! / 100).toFixed(2)}` : '待核验'} · 汇率 {purchase.cnyPerUsd}</p>}
    <Button variant="outline" disabled={busy} onClick={() => void sync()}>{busy ? '正在核对…' : '核对采购与发货进度'}</Button>
    {purchase && !purchase.supplierOrder && ['creating', 'unknown'].includes(purchase.state) && <details>
      <summary>已核实原采购没有扣款及发货</summary>
      <p>请先在供应商后台核对原订单。登记后停止自动采购，允许售后退款。</p>
      <label><input type="checkbox" checked={confirmed} onChange={e => setConfirmed(e.target.checked)} disabled={busy} /> 已核实无扣款、无发货</label>
      <Input aria-label="采购核查说明" value={note} onChange={e => setNote(e.target.value)} maxLength={500} disabled={busy} />
      <Button variant="outline" disabled={busy || !confirmed || note.trim().length < 6} onClick={() => void sync(true)}>登记采购未发生</Button>
    </details>}
  </section>
}
