import { useEffect, useRef, useState } from 'react'
import { Download, RefreshCw } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Feedback } from '@/components/workspace-loading'
import { api, errorMessage, RequestError } from '@/lib/api'
import type { ServerOrder } from '@/lib/use-store'

export type RechargeCredential = { attemptId: string; saved: boolean; mode: 'session' | 'access_token'; savedAt: number }

export function RechargeRecoveryPanel({ order, upstreamOrderId, credential, onUpdated }: { order: ServerOrder; upstreamOrderId?: string; credential?: RechargeCredential; onUpdated: () => void }) {
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [message, setMessage] = useState('')
  const [checked, setChecked] = useState(false)
  const inFlight = useRef(false)
  const updated = useRef(onUpdated)
  useEffect(() => { updated.current = onUpdated }, [onUpdated])
  useEffect(() => {
    let closed = false
    const controller = new AbortController()
    async function sync() {
      if (inFlight.current || document.visibilityState === 'hidden') return
      inFlight.current = true; setBusy('sync')
      try {
        await api(`/api/admin/orders/${order.id}/platform-status`, {}, controller.signal)
        if (!closed) { setChecked(true); setError(''); updated.current() }
      } catch (e) {
        if (!closed) { setChecked(false); setError(`上游同步未完成，当前显示上次记录：${errorMessage(e)}`) }
      } finally { inFlight.current = false; if (!closed) setBusy('') }
    }
    void sync()
    const timer = window.setInterval(() => void sync(), 15000)
    return () => { closed = true; controller.abort(); window.clearInterval(timer) }
  }, [order.id])
  async function act(action: 'credential' | 'retry') {
    if (!credential || inFlight.current) return
    inFlight.current = true; setBusy(action); setError(''); setMessage('')
    try {
      if (action === 'credential') {
        if (!upstreamOrderId) throw new RequestError('暂无上游订单号，无法导出充值资料。', 409)
        const result = await api<{ credential: { mode: string; session?: string; access_token?: string } }>(`/api/admin/orders/${order.id}/recharge-credential`, { attemptId: credential.attemptId })
        const content = result.credential.mode === 'session' ? result.credential.session : result.credential.access_token
        if (!content) throw new RequestError('未获取到充值资料，请刷新后重试。', 503)
        const filename = `${upstreamOrderId.replace(/[^a-zA-Z0-9_-]/g, '_')}.txt`
        const url = URL.createObjectURL(new Blob([content], { type: 'text/plain;charset=utf-8' }))
        const link = document.createElement('a')
        link.href = url; link.download = filename
        document.body.appendChild(link)
        try { link.click() } finally {
          link.remove()
          window.setTimeout(() => URL.revokeObjectURL(url), 1000)
        }
        setMessage(`已导出 ${filename}，可到上游手动处理。`)
      } else {
        await api(`/api/admin/orders/${order.id}/recharge-retry`, { attemptId: credential.attemptId })
        setMessage('补发请求已提交，将继续同步充值结果。')
      }
    } catch (e) { setError(errorMessage(e)) }
    finally { inFlight.current = false; setBusy(''); updated.current() }
  }
  const canRetry = checked && credential?.saved && order.fulfillment?.canSubmit && ['failed', 'cancelled'].includes(order.fulfillment?.rechargeStatus ?? '') && order.status !== 'success'
  return <section className="admin-platform-status" aria-label="充值资料与补发">
    <h3>充值资料与补发</h3>
    {busy === 'sync' && <p className="admin-subtle" role="status">正在同步上游…</p>}
    {order.fulfillment?.message && <p>{order.fulfillment.message}</p>}
    {order.fulfillment?.accountEmailMasked && <p className="admin-subtle">充值账号：{order.fulfillment.accountEmailMasked}</p>}
    <p className="admin-subtle">{credential?.saved ? `已加密留存 ${credential.mode === 'session' ? 'Session' : 'Access Token'}，仅总后台可导出，文件以上游订单号命名。` : '该次充值未留存资料。历史 Session 无法补回，需要用户在原订单重新提交。'}</p>
    {credential?.saved && !upstreamOrderId && <p className="admin-subtle">暂无上游订单号，暂不可导出。</p>}
    <div className="admin-platform-status-heading">
      <Button variant="outline" size="sm" disabled={!!busy || !credential?.saved || !upstreamOrderId} onClick={() => void act('credential')}><Download size={14} />{busy === 'credential' ? '导出中…' : credential?.mode === 'access_token' ? '导出 Access Token' : '导出 Session'}</Button>
      <Button size="sm" disabled={!!busy || !canRetry} onClick={() => void act('retry')}><RefreshCw size={14} />{busy === 'retry' ? '补发中…' : '自动补发'}</Button>
    </div>
    <p className="admin-subtle">每 15 秒核对上游。自动补发使用已保存资料；仅原任务已失败且上游允许重试时可用。手动补发后请先同步状态。</p>
    <Feedback error={error} />{message && <p role="status" className="admin-success">{message}</p>}
  </section>
}
