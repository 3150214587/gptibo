import { useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { toString as qrToString } from 'qrcode'
import { RefreshCw, TriangleAlert } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { LiveSessionForm, LiveRechargeProcessing, LiveRechargeSuccess } from './live-recharge-content'
import { CheckoutStageTransition } from './checkout-stage-transition'
import { PaymentCard } from './payment-card'
import { PaymentConfirmed, PaymentMessage } from './payment-feedback'
import { useOrderSupportGuide } from '@/lib/order-support-guide'
import { api, errorMessage, RequestError } from '@/lib/api'
import { requestLane } from '@/lib/request-lane'
import { visiblePolling } from '@/lib/visible-polling'
import { checkoutSession } from '@/lib/checkout-session'
import type { SessionPrecheck } from './session-precheck'
import type { ServerOrder } from '@/lib/use-store'
import { productForPlan } from '@/lib/products'
import { paymentDeadline } from '../../shared/payment-window'
import { publicAlipayQr } from '../../shared/public-payment'
import '@/styles/live-checkout.css'

type Props = { order: ServerOrder; open: boolean; onUpdated?: () => void; onOrderUpdated?: (order: ServerOrder) => void; onOrders: () => void; error?: string; refreshTarget?: HTMLElement | null; refreshDisabled?: boolean; transport?: typeof api; initialSession?: string }
export function LiveCheckout({ order, open, onUpdated, onOrderUpdated, onOrders, error, refreshTarget, refreshDisabled, transport = api, initialSession = '' }: Props) {
  const [session, setSession] = useState(initialSession)
  const [confirmed, setConfirmed] = useState(false)
  const [touched, setTouched] = useState(false)
  const [busy, setBusy] = useState<'' | 'query' | 'check' | 'submit'>('')
  const [precheck, setPrecheck] = useState<SessionPrecheck | null>(null)
  const checkAbort = useRef<AbortController | null>(null)
  useEffect(() => { if (!open) checkAbort.current?.abort() }, [open])
  useEffect(() => () => { checkAbort.current?.abort() }, [])
  const [failure, setFailure] = useState('')
  const [submitFailure, setSubmitFailure] = useState('')
  const [clock, setClock] = useState(() => Date.now())
  const [qrImage, setQrImage] = useState<{ orderId: string; source: string; data: string } | null>(null)
  const [qrError, setQrError] = useState<{ orderId: string; source: string } | null>(null)
  const submitting = useRef(false)
  const lane = useRef(requestLane())
  const attemptKey = useRef<string | undefined>(undefined)
  const callbacks = useRef({ onUpdated, onOrderUpdated, transport })
  useEffect(() => { callbacks.current = { onUpdated, onOrderUpdated, transport } }, [onUpdated, onOrderUpdated, transport])
  const info = order.fulfillment!
  const product = productForPlan(order.plan)
  const paused = (order.redemptionBlocked && order.status !== 'success') || order.refundHold || (order.refundedCents ?? 0) >= Math.round(order.price * 100)
  const input = checkoutSession(session, info.retryMode)
  const unknown = info.rechargeStatus === 'unknown'
  const failed = order.status === 'failed' || info.rechargeStatus === 'rejected'
  const failureTitle = info.failureCode === 'account_has_subscription' ? '账号已有订阅' : info.message?.includes('登录凭据已失效') ? 'Session 已失效' : '充值未完成'
  const deadline = paymentDeadline(order.createdAt, info.expiresAt)
  const expired = order.paymentStatus === 'unpaid' && (info.paymentExpired || deadline <= clock)
  const qr = open && !paused && !expired && order.status === 'pending' && order.paymentStatus === 'unpaid' ? publicAlipayQr(info.paymentQr) : null
  const image = qrImage?.orderId === order.id && qrImage.source === qr ? qrImage.data : null
  const imageError = qrError?.orderId === order.id && qrError.source === qr
  const remaining = Math.max(0, Math.ceil((deadline - clock) / 1000))
  const watching = open && !paused && (order.status === 'pending' || order.status === 'processing' || unknown)
  const supportProblem = submitFailure || failure || error || (failed ? info.message ?? '充值未完成' : touched ? input.problem : null)
  useOrderSupportGuide(!paused && !busy && order.status !== 'success' && supportProblem ? `${order.id}:${supportProblem}` : null)

  useEffect(() => {
    let active = true
    if (qr) void qrToString(qr, { type: 'svg', width: 156, margin: 4, errorCorrectionLevel: 'M', color: { dark: '#171719', light: '#ffffff' } })
      .then(svg => { if (active) { setQrImage({ orderId: order.id, source: qr, data: `data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg)}` }); setQrError(null) } })
      .catch(() => { if (active) setQrError({ orderId: order.id, source: qr }) })
    return () => { active = false }
  }, [order.id, qr])

  useEffect(() => {
    if (order.status !== 'pending') return
    const frame = requestAnimationFrame(() => setClock(Date.now()))
    const timer = setInterval(() => setClock(Date.now()), 1000)
    return () => { cancelAnimationFrame(frame); clearInterval(timer) }
  }, [open, order.id, order.status, info.expiresAt])

  // Prepare and reconcile the same order until payment/recharge is confirmed.
  // A transient failure or an existing cashier URL without a QR must keep retrying.
  useEffect(() => {
    if (!watching) return
    const controller = new AbortController()
    const stop = visiblePolling(() => lane.current.run(async () => {
      try {
        const result = await callbacks.current.transport<{ order: ServerOrder }>(`/api/orders/${order.id}/fulfillment`, { action: 'poll' }, controller.signal)
        if (!controller.signal.aborted) {
          callbacks.current.onOrderUpdated?.(result.order); setFailure('')
          if (result.order.fulfillment?.rechargeStatus !== 'unknown') setSubmitFailure('')
        }
      } catch (e) {
        if (!controller.signal.aborted && !(e instanceof RequestError && e.status === 409)) setFailure(errorMessage(e))
      }
    }, true), 5000)
    return () => { stop(); controller.abort() }
  }, [watching, order.id])

  async function run(submit = false) {
    if (submitting.current || busy || (!submit && refreshDisabled)) return
    const currentInput = checkoutSession(session, info.retryMode)
    if (submit && (!currentInput.credential || !confirmed)) { setTouched(true); setSubmitFailure(currentInput.problem); return }
    submitting.current = true; setBusy(submit ? 'submit' : 'query'); setFailure('')
    const credential = currentInput.credential
    if (submit) { setSubmitFailure(''); setPrecheck(null); setTouched(false) }
    try {
      await lane.current.run(async () => {
        if (submit) {
          if (['rejected', 'failed', 'cancelled'].includes(info.rechargeStatus ?? '')) attemptKey.current = undefined
          attemptKey.current = info.retryKey ?? attemptKey.current ?? crypto.randomUUID()
          let precheckReceipt: string | undefined
          if (info.precheckRequired && !info.retryKey) {
            setBusy('check'); const controller = new AbortController(); checkAbort.current = controller
            try {
              const checked = await transport<{ precheck: SessionPrecheck }>(`/api/orders/${order.id}/session-precheck`, { key: attemptKey.current, credential }, controller.signal)
              if (controller.signal.aborted) return
              if (checked.precheck.target !== order.plan || (['eligible', 'deferred'].includes(checked.precheck.state) && !checked.precheck.receipt)) { setPrecheck({ state: 'unknown', code: 'check_unavailable', target: order.plan }); return }
              setPrecheck(checked.precheck)
              if (checked.precheck.state !== 'recovery' && (!['eligible', 'deferred'].includes(checked.precheck.state) || !checked.precheck.receipt)) return
              precheckReceipt = checked.precheck.receipt
            } catch (e) {
              if (!controller.signal.aborted) setPrecheck({ state: e instanceof RequestError && e.status === 429 ? 'limited' : e instanceof DOMException && e.name === 'TimeoutError' ? 'timeout' : 'unknown', code: 'check_unavailable', target: order.plan })
              return
            } finally { if (checkAbort.current === controller) checkAbort.current = null }
          }
          setBusy('submit'); setSession(''); setConfirmed(false)
          const submittedAt = performance.now()
          const result = await transport<{ order: ServerOrder }>(`/api/orders/${order.id}/fulfillment`, { action: 'submit', key: attemptKey.current, customerConfirmedEmail: true, credential, precheckReceipt })
          // Dispatch immediately; only the visual handoff has a brief minimum dwell.
          if (precheckReceipt && !matchMedia('(prefers-reduced-motion: reduce)').matches) await new Promise(resolve => setTimeout(resolve, Math.max(0, 450 - (performance.now() - submittedAt))))
          callbacks.current.onOrderUpdated?.(result.order)
          setSubmitFailure('')
          attemptKey.current = undefined
        } else {
          const result = await transport<{ order: ServerOrder }>(`/api/orders/${order.id}/fulfillment`, { action: 'query' })
          callbacks.current.onOrderUpdated?.(result.order)
          if (result.order.fulfillment?.rechargeStatus !== 'unknown') setSubmitFailure('')
        }
      })
    } catch (e) { if (submit) { setPrecheck(null); setSubmitFailure(errorMessage(e)) } else setFailure(errorMessage(e)) }
    finally { submitting.current = false; setBusy(''); callbacks.current.onUpdated?.() }
  }
  // Place the original refresh control in the footer while sharing the submit lock and errors.
  const refresh = <Button variant="ghost" className="flow-refresh-action" disabled={Boolean(busy) || refreshDisabled} onClick={() => void run()}><RefreshCw className={busy === 'query' ? 'processing-spinner' : undefined} size={13} />{busy === 'query' ? '正在核对…' : expired ? '核对付款结果' : '刷新进度'}</Button>
  return <div className="checkout-body live-checkout" aria-busy={Boolean(busy)}>
    {paused ? <div className="order-service-paused">{order.redemptionBlocked ? '本订单已由客服处理，无法再次兑换。如有疑问，请联系支持。' : info.financialHold ? info.message : order.refundHold ? '售后申请正在处理中，服务暂时暂停。' : '本订单已全额退款。'}{!order.redemptionBlocked && <><br />可在下方查看售后进度。</>}</div> : <>
      {order.status === 'pending' && <div className="live-payment">
        {expired ? <PaymentMessage state="expired" title="订单已过期">付款有效期已结束，请重新下单。若已付款，请先核对付款结果，勿重复支付。</PaymentMessage> : qr ? <PaymentCard qr={qr} image={image} imageError={Boolean(imageError)} remaining={remaining} price={order.price} quantity={product.quantity} /> : <PaymentMessage state="loading" title="正在生成支付二维码">订单已保存，准备好后自动显示。长时间未显示，请刷新进度或联系客服。</PaymentMessage>}
      </div>}

      <CheckoutStageTransition stage={info.canSubmit ? 'input' : order.status === 'processing' && !unknown ? 'processing' : 'other'}>{stage => <>
      {stage === 'input' && <>
        {unknown && busy !== 'submit' ? <Alert className="live-notice" role="status"><TriangleAlert aria-hidden="true" /><AlertTitle>提交待确认</AlertTitle><AlertDescription>{submitFailure && <>{submitFailure}<br /></>}充值服务尚未确认收到本次提交，暂不能确认已开始充值。先刷新查询；仍未确认时，粘贴原 Session 核对同一次提交。请勿重新付款。</AlertDescription></Alert>
          : failed && busy !== 'submit' ? <Alert className="live-notice" role="status"><TriangleAlert aria-hidden="true" /><AlertTitle>{failureTitle}</AlertTitle><AlertDescription>{info.message ?? '请获取新的 Session 后重试。'} 本订单无需重复付款。</AlertDescription></Alert>
          : <PaymentConfirmed>付款已确认，请连接要充值的账号。</PaymentConfirmed>}
        <LiveSessionForm session={session} confirmed={confirmed} touched={touched} busy={busy} precheckRequired={info.precheckRequired} precheck={precheck} retryMode={info.retryMode} retryKey={info.retryKey} failed={failed} onSessionChange={value => { setSession(value); setConfirmed(false); setTouched(false); setPrecheck(null) }} onConfirmChange={setConfirmed} onTouch={() => setTouched(true)} onSubmit={() => void run(true)} />
      </>}

      {stage === 'processing' && <LiveRechargeProcessing progressStage={info.progressStage} progressUpdatedAt={info.progressUpdatedAt} rechargeStatus={info.rechargeStatus} syncError={Boolean(info.syncError || failure || error)} />}
      </>}</CheckoutStageTransition>
      {order.status === 'success' && <LiveRechargeSuccess accountEmailMasked={info.accountEmailMasked} finishedAt={info.finishedAt} supportContacts={order.supportContacts} partnerName={order.partnerName} onOrders={onOrders} />}
      {order.status === 'cancelled' && (info.paymentExpired ? <PaymentMessage state="expired" title="订单已过期">付款有效期已结束，请重新下单。若已付款，请先核对付款结果，勿重复支付。</PaymentMessage> : <PaymentMessage state="closed" title="订单已关闭">此订单不再接受付款。</PaymentMessage>)}
      {order.status === 'failed' && !info.canSubmit && <div className="result-body"><div className="result-icon warning"><TriangleAlert size={30} /></div><h3>{failureTitle}</h3><p>{info.message ?? '请联系客服核对。'}</p></div>}
      {!info.canSubmit && info.message && !['failed', 'success'].includes(order.status) && <Alert className="live-notice" role="status"><TriangleAlert aria-hidden="true" /><AlertDescription>{info.message}</AlertDescription></Alert>}
    </>}
    {((!unknown && submitFailure) || failure || error) && <div className="live-request-error" role="alert"><p>{(!unknown && submitFailure) || failure || error}</p></div>}
    {order.status !== 'success' && (order.status !== 'cancelled' || info.paymentExpired) && (refreshTarget ? createPortal(refresh, refreshTarget) : <div className="live-sync">{refresh}</div>)}
  </div>
}
