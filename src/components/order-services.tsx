import { storefrontShopSlug } from '@/lib/storefront-routing'
import { useAuthVerification } from './use-auth-verification'
import { OrderVerification } from './order-verification'
import { PaymentMethodSelector, type PaymentMethodOption } from './payment-method-choices'
import { xUsername } from '../../shared/xgift-products'
import { activityCopy } from '@/lib/activity-copy'
import { useEffect, useRef, useState, type RefObject } from 'react'
import { AsyncButton, RequestNotice } from './payment-feedback'
import { ArrowLeft, ArrowRight, Check, LoaderCircle, Mail, RotateCcw, ShieldCheck } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { EmailAutocomplete } from './email-autocomplete'
import { Textarea } from '@/components/ui/textarea'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Feedback } from '@/components/workspace-loading'
import { orderEmailProblem } from '@/lib/email'
import { api, errorMessage } from '@/lib/api'
import { useResource } from '@/lib/use-resource'
import { mutationError } from '@/lib/query-state'
import type { ServerOrder } from '@/lib/use-store'
import { money, date } from '@/partner/model'
import '@/styles/order-services.css'

import { refundCaseLabel, refundLabel, type RefundCase, type RefundEvent } from '@/lib/refunds'

export function PurchaseEmail({ couponAvailable = false, shopSlug, purchasePlan, plan, price, paymentMethods, initialEmail, busy, error, inputRef, onSubmit }: { couponAvailable?: boolean; shopSlug?: string; purchasePlan?: string; plan?: string; price: number; paymentMethods?: PaymentMethodOption[]; initialEmail?: string | null; busy: boolean; error: string; inputRef: RefObject<HTMLInputElement | null>; onSubmit: (email: string, username?: string, preflightToken?: string, paymentMethod?: string, expectedFeeBps?: number, turnstileToken?: string, couponCode?: string, couponPrice?: number) => Promise<void> }) {
  const [verification, setVerification] = useState({ ready: false, token: '' })
  const [verificationRevision, setVerificationRevision] = useState(0)
  const [email, setEmail] = useState(initialEmail ?? '')
  const showCoupon = Boolean(shopSlug && couponAvailable)
  const [couponCode, setCouponCode] = useState(() => showCoupon ? new URLSearchParams(location.search).get('coupon')?.slice(0,32) ?? '' : '')
  const [coupon, setCoupon] = useState<{ code: string; priceCents: number; discountCents: number; email: string; listPriceCents: number } | null>(null)
  const [couponBusy, setCouponBusy] = useState(false)
  const [couponError, setCouponError] = useState('')
  const appliedCoupon = showCoupon && coupon && coupon.code === couponCode.trim().toUpperCase() && coupon.email === email.trim().toLowerCase() && coupon.listPriceCents === Math.round(price * 100) ? coupon : null
  const payablePrice = appliedCoupon ? appliedCoupon.priceCents / 100 : price
  async function applyCoupon() {
    if (!showCoupon || couponBusy || busy) return
    setCouponBusy(true); setCouponError(''); setCoupon(null)
    try { const result = await api<{ code: string; priceCents: number; discountCents: number; listPriceCents: number }>('/api/orders/coupon-quote', { partner: shopSlug, plan: purchasePlan, email, code: couponCode }); if (result.listPriceCents !== Math.round(price * 100)) throw new Error('商品价格已更新，请关闭窗口后重新选择。'); setCoupon({ ...result, email: email.trim().toLowerCase() }) }
    catch (e) { setCouponError(errorMessage(e)) } finally { setCouponBusy(false) }
  }
  const [touched, setTouched] = useState(false)
  const [selectedMethod, setSelectedMethod] = useState('')
  const method = paymentMethods?.some(item => item.method === selectedMethod) ? selectedMethod : paymentMethods?.[0]?.method
  const feeBps = paymentMethods?.find(item => item.method === method)?.feeBps ?? 0
  const feeCents = Math.floor((Math.round(payablePrice * 100) * feeBps + 5000) / 10000)
  const methodMissing = paymentMethods !== undefined && !method
  const [username, setUsername] = useState('')
  const [check, setCheck] = useState<{ username: string; token: string; expires_at: number } | null>(null)
  const [checking, setChecking] = useState(false)
  const [checkError, setCheckError] = useState('')
  const normalized = xUsername(username)
  const verified = !!check && check.username === normalized
  useEffect(() => {
    if (!check) return
    const timer = setTimeout(() => setCheck(null), Math.max(0, check.expires_at - Date.now()))
    return () => clearTimeout(timer)
  }, [check])
  const problem = orderEmailProblem(email)
  const validation = touched ? problem : ''
  const submitting = useRef(false)
  async function submit() {
    if (busy || submitting.current) return
    if (methodMissing || couponBusy) return
    if (showCoupon && couponCode.trim() && !appliedCoupon) { setCouponError('请先应用优惠码，或清空后继续。'); return }
    setTouched(true); if (problem) { inputRef.current?.focus(); return }
    submitting.current = true; setCheckError('')
    try {
      if (plan && !verified) {
        if (!normalized) { setCheckError('请输入 X 用户名（@ 后的英文、数字或下划线），不要填显示名称。'); return }
        setChecking(true)
        const result = await api<{ eligible: boolean; username: string; token?: string; expires_at?: number; message: string }>('/api/orders/xgift/eligibility', { username: normalized, plan })
        if (result.eligible && result.token && result.expires_at) setCheck({ username: result.username, token: result.token, expires_at: result.expires_at })
        else setCheckError(result.message)
        return
      }
      if (!verification.ready) { setCheckError('请先完成人机验证。'); return }
      try { await onSubmit(email.trim().toLowerCase(), plan ? normalized ?? undefined : undefined, plan ? check?.token : undefined, method, paymentMethods ? feeBps : undefined, verification.token, appliedCoupon?.code, appliedCoupon ? appliedCoupon.priceCents / 100 : undefined) }
      finally { setVerification({ ready: false, token: '' }); setVerificationRevision(v => v + 1) }
    } catch (e) { setCheckError(errorMessage(e)) }
    finally { submitting.current = false; setChecking(false) }
  }
  return <form className="purchase-email-form" noValidate onSubmit={e => { e.preventDefault(); void submit() }}>
    {plan && <><label htmlFor="gift-username">X 用户名 <span>必填</span></label>
      <div className="x-username-field" data-verified={verified}><span aria-hidden="true">@</span><Input id="gift-username" value={username} onChange={e => { setUsername(e.target.value.replace(/^@/, '')); setCheck(null); setCheckError('') }} disabled={busy || checking} autoCapitalize="none" autoComplete="off" spellCheck={false} maxLength={16} required aria-describedby="x-account-hint" aria-invalid={Boolean(checkError)} placeholder="username" />{checking ? <LoaderCircle size={17} className="processing-spinner" aria-hidden="true" /> : verified && <Check size={17} aria-hidden="true" />}</div>
      <div id="x-account-hint" className="x-account-hint" data-verified={verified} role="status">{checking ? '正在验证账号，请稍候…' : verified ? `@${check!.username} 可以开通，请核对后继续。` : '填写个人主页中的 @ 用户名，无需密码。'}</div></>}
    <label htmlFor="purchase-email">下单邮箱 <span>必填</span></label>
    <EmailAutocomplete onPointerDown={event => event.currentTarget.focus({ preventScroll: true })} ref={inputRef} id="purchase-email" required maxLength={254} aria-invalid={Boolean(validation)} aria-describedby="purchase-email-hint" onBlur={() => setTouched(true)} value={email} onValueChange={setEmail} disabled={busy} placeholder="you@qq.com" />
    <div className="email-validation" id="purchase-email-hint">{validation ? <p role="alert">{validation}</p> : <p>{plan ? '用于接收通知、找回订单。' : '换设备后，可用这个邮箱验证并找回订单。'}</p>}</div>
    {showCoupon && <div className="purchase-coupon"><label htmlFor="purchase-coupon">优惠码 <span>选填</span></label><div className="purchase-coupon-controls"><Input id="purchase-coupon" value={couponCode} maxLength={32} disabled={busy || couponBusy} onChange={e => {setCouponCode(e.target.value);setCoupon(null);setCouponError('')}} placeholder="输入本店商品优惠码" /><Button type="button" variant="outline" disabled={busy || couponBusy || !couponCode.trim() || Boolean(problem)} onClick={() => void applyCoupon()}>{couponBusy ? '校验中…' : '应用'}</Button></div>{couponError && <p role="alert">{couponError}</p>}{appliedCoupon && <p role="status">已优惠 {money(appliedCoupon.discountCents)}，商品应付 {money(appliedCoupon.priceCents)}</p>}</div>}
    {paymentMethods !== undefined && <PaymentMethodSelector methods={paymentMethods} value={method} busy={busy || checking} onChange={setSelectedMethod} />}
    {paymentMethods !== undefined && method && <dl className="purchase-fee-summary" aria-live="polite"><div><dt>商品金额</dt><dd>¥{payablePrice.toFixed(2)}</dd></div><div><dt>平台手续费（{feeBps / 100}%）</dt><dd>¥{(feeCents / 100).toFixed(2)}</dd></div><div><dt>实付金额</dt><dd>¥{((Math.round(payablePrice * 100) + feeCents) / 100).toFixed(2)}</dd></div></dl>}
    <OrderVerification key={verificationRevision} onChange={setVerification} />
    {(checkError || error) && <p className="purchase-submit-error" role="alert">{checkError || error}</p>}
    <AsyncButton className="primary-action" type="submit" loading={busy || checking} pendingLabel={checking ? "正在验证账号" : "正在创建订单"} disabled={Boolean(problem) || methodMissing || (!!plan && !normalized) || ((!plan || verified) && !verification.ready)}>{plan ? verified ? "确认账号，继续付款" : "验证账号" : "继续付款"} <ArrowRight size={16} /></AsyncButton>
    <div className="checkout-action-feedback">{busy && <RequestNotice label="正在保存订单，完成后自动进入付款。" slowLabel="订单仍在确认中，请勿重复下单。" />}</div>
  </form>
}

export function RecoveryDialog({ open, onClose, onUpdated }: { open: boolean; onClose: () => void; onUpdated: () => void }) {
  const [busy, setBusy] = useState(false)
  const titleRef = useRef<HTMLHeadingElement>(null)
  return <Dialog open={open} disablePointerDismissal onOpenChange={value => { if (!value && !busy) onClose() }}><DialogContent className="motion-dialog service-dialog recovery-dialog" initialFocus={titleRef} showCloseButton={!busy}><RecoveryContent titleRef={titleRef} onClose={onClose} onUpdated={onUpdated} onBusyChange={setBusy} /></DialogContent></Dialog>
}
function RecoveryContent({ titleRef, onClose, onUpdated, onBusyChange }: { titleRef?: RefObject<HTMLHeadingElement | null>; onClose: () => void; onUpdated: () => void; onBusyChange: (busy: boolean) => void }) {
  const [revision, setRevision] = useState(0)
  const sendHuman = useAuthVerification('customer_send')
  const loginHuman = useAuthVerification('customer_verify')
  const session = useResource<{ email: string | null; mailReady: boolean }>('/api/orders/recovery/session', revision)
  const [email, setEmail] = useState(''); const [code, setCode] = useState(''); const [challenge, setChallenge] = useState(''); const [cooldown, setCooldown] = useState(0); const [busy, setBusy] = useState(false); const [error, setError] = useState('')
  useEffect(() => { onBusyChange(busy); return () => onBusyChange(false) }, [busy, onBusyChange])
  useEffect(() => { if (!cooldown) return; const timer = setTimeout(() => setCooldown(v => v - 1), 1000); return () => clearTimeout(timer) }, [cooldown])
  async function run(action: 'send' | 'verify' | 'logout') {
    if (busy || (action === 'send' && (cooldown > 0 || !sendHuman.ready)) || (action === 'verify' && !loginHuman.ready)) return
    setBusy(true); setError('')
    try {
      const result = await api<{ challenge?: string }>('/api/orders/recovery/' + action, action === 'send' ? { email, shop: storefrontShopSlug(location.pathname), ...sendHuman.consume() } : action === 'verify' ? { challenge, code, ...loginHuman.consume() } : {})
      if (action === 'send') { setChallenge(result.challenge!); setCode(''); setCooldown(60) }
      else { onUpdated(); if (action === 'verify') onClose(); else { setRevision(v => v + 1); setChallenge('') } }
    } catch (e) { setError(errorMessage(e)) } finally { setBusy(false) }
  }
  return <><DialogHeader><DialogTitle ref={titleRef} tabIndex={-1}>通过邮箱找回订单</DialogTitle><DialogDescription>使用下单时填写的邮箱，验证后查看该邮箱的最近 100 笔订单。查询权限保留 8 小时。</DialogDescription></DialogHeader><Feedback {...session} shape="compact" /><Feedback error={error} />{session.data?.email ? <div className="service-form"><p>已验证：{session.data.email}</p><Button variant="outline" disabled={busy} onClick={() => void run('logout')}>退出邮箱查询</Button><p>退出后，仍可查看此浏览器直接创建的订单。</p></div> : session.data && (session.data.mailReady ? <form className="service-form" onSubmit={e => { e.preventDefault(); void run(challenge ? 'verify' : 'send') }}><label htmlFor="recovery-email">下单邮箱</label><Input onPointerDown={event => event.currentTarget.focus({ preventScroll: true })} id="recovery-email" type="email" autoComplete="email" required maxLength={254} disabled={busy} value={email} onChange={e => { setEmail(e.target.value); setChallenge('') }} />{sendHuman.widget}{challenge && <><p>如果该邮箱有订单，验证码将发送至邮箱，10 分钟内有效。</p><label htmlFor="recovery-code">邮箱验证码</label><Input onPointerDown={event => event.currentTarget.focus({ preventScroll: true })} id="recovery-code" inputMode="numeric" autoComplete="one-time-code" pattern="[0-9]{6}" required maxLength={6} value={code} onChange={e => setCode(e.target.value.replace(/\D/g, '').slice(0,6))} /><Button type="button" variant="ghost" disabled={!sendHuman.ready || busy || cooldown > 0} onClick={() => void run('send')}>{cooldown ? `${cooldown} 秒后可重发` : '重新发送'}</Button></>}{challenge && loginHuman.widget}<Button type="submit" disabled={(challenge ? !loginHuman.ready : !sendHuman.ready) || busy || (!challenge && cooldown > 0) || (Boolean(challenge) && code.length !== 6)}>{busy ? '请稍候…' : challenge ? '验证并查看订单' : '发送验证码'}</Button></form> : <div className="service-callout"><Mail size={20} /><strong>邮箱找回即将启用</strong><p>邮件服务尚未配置。请先使用下单时的浏览器查询，保留 Cookie；新订单已保存下单邮箱，邮件启用后即可找回。</p><Button variant="outline" onClick={() => setRevision(v => v + 1)}>重新检查</Button></div>)}</>
}

export function AfterSales({ order, onOpen, disabled, expanded, testingEnabled }: { testingEnabled?: boolean; order: ServerOrder; onOpen: (trigger: HTMLButtonElement) => void; disabled?: boolean; expanded: boolean }) {
  if (['pending','cancelled'].includes(order.status)) return null
  return <Button className="after-sales-action" variant="ghost" disabled={disabled} aria-controls="purchase-refund-panel" aria-expanded={expanded} onClick={event => onOpen(event.currentTarget)}>{order.mode !== 'live' && !testingEnabled || order.refundHold || order.refundedCents ? '查看售后' : '申请售后'}</Button>
}
export function RefundContent({ order, onUpdated, onBusyChange, onBack, testingEnabled }: { testingEnabled?: boolean; order: ServerOrder; onUpdated?: () => void; onBusyChange: (busy: boolean) => void; onBack: () => void }) {
  const [revision, setRevision] = useState(0); const [reason, setReason] = useState(''); const [key, setKey] = useState(() => crypto.randomUUID()); const [busy, setBusy] = useState(false); const [error, setError] = useState('')
  const data = useResource<{ automaticPlatformRefund?: boolean; supportsWarranty?: boolean; warrantyMessage?: string; cases: RefundCase[]; events: RefundEvent[]; orderVersion: number; refundableCents: number; refundableFeeCents?: number }>(`/api/orders/${order.id}/refunds`, revision, { retainOnError: true })
  const warrantySupported = data.data?.supportsWarranty !== false
  const submitting = useRef(false)
  useEffect(() => { onBusyChange(busy); return () => onBusyChange(false) }, [busy, onBusyChange])
  const active = data.data?.cases.find(c => ['pending','approved'].includes(c.status))
  const activeID = active?.id
  useEffect(() => {
    if (!activeID || busy) return
    const timer = window.setInterval(() => {
      if (document.visibilityState === 'visible') setRevision(v => v + 1)
    }, 15000)
    return () => window.clearInterval(timer)
  }, [activeID, busy])
  const settled = data.data?.cases.filter(c => c.status === 'refunded').map(c => c.id).join(',')
  const notifiedSettlement = useRef('')
  useEffect(() => {
    if (settled && notifiedSettlement.current !== settled) {
      notifiedSettlement.current = settled
      onUpdated?.()
    }
  }, [settled, onUpdated])
  async function submit(cancel?: RefundCase) {
    if ((!cancel && !warrantySupported) || order.mode !== 'live' && !testingEnabled || submitting.current || busy || !data.data || data.loading || data.stale) return
    submitting.current = true
    setBusy(true); setError('')
    try { await api(`/api/orders/${order.id}/refunds`, cancel ? { action: 'cancel', id: cancel.id, version: cancel.version } : { reason, key, version: data.data.orderVersion }); setRevision(v => v + 1); setKey(crypto.randomUUID()); setReason(''); onUpdated?.() } catch (e) { setError(mutationError(e)); setRevision(v => v + 1); onUpdated?.() } finally { submitting.current = false; setBusy(false) }
  }
  return <div className="refund-content" aria-busy={busy}><div className="refund-scroll"><Feedback {...data} shape="compact" /><Feedback error={error} />{data.stale && <p className="service-stale">显示上次读取的记录。刷新成功后可继续操作。</p>}{data.data && <><p className="service-policy"><ShieldCheck size={16} />{!warrantySupported ? data.data.warrantyMessage || '该商品在购买前的下单须知中已明确说明不支持售后质保，因此无法提交售后申请。如有疑问，请联系商店客服。' : order.mode !== 'live' ? testingEnabled ? '可检查售后申请、审核与退款进度。测试订单不涉及资金往来。' : '此历史记录仅支持查看售后进度，不涉及收款或转账。' : data.data.automaticPlatformRefund ? '申请将自动提交处理。确认未提交充值或扣款前已明确拒绝的订单可自动原路退款，其他情况需核对。申请后暂停充值及对应代理收益，提交后不可自行撤回。' : '未交付或交付失败可申请全额退款；已完成订单由平台审核。审核通过后安排退款，申请中暂停服务及对应代理收益。'}</p>{data.data.cases.map(c => <article className="refund-record" key={c.id}><div><strong>{order.mode !== 'live' && c.status === 'refunded' ? '记录已结束' : refundCaseLabel(c)}</strong><span>{money((c.approved_cents ?? c.requested_cents) + (c.fee_cents ?? 0))}</span></div><p>{c.reason}</p><ol>{data.data!.events.filter(e => e.case_id === c.id).map((e,i) => <li key={i}><time>{date(e.created_at)}</time><span>{order.mode !== 'live' && e.status === 'refunded' ? '记录已结束' : refundLabel(e.status)} · {activityCopy(e.note)}</span></li>)}</ol>{(order.mode === 'live' || testingEnabled) && c.status === 'pending' && <Button variant="outline" disabled={busy || data.loading || data.stale} onClick={() => void submit(c)}>撤回申请</Button>}</article>)}{(order.mode === 'live' || testingEnabled) && warrantySupported && !active && data.data.refundableCents > 0 && <form className="service-form" onSubmit={e => { e.preventDefault(); void submit() }}><label htmlFor="refund-reason">描述你遇到的问题</label><Textarea id="refund-reason" required maxLength={500} rows={3} disabled={busy} value={reason} onChange={e => setReason(e.target.value)} placeholder="例如：付款后未开通，想申请退款。请勿填写密码或 Session。" /><p>本次可申请商品款 {money(data.data.refundableCents)}{Boolean(data.data.refundableFeeCents) && <>，另含平台手续费 {money(data.data.refundableFeeCents ?? 0)}，随商品款同比例退还</>}，最终金额以审核结果为准。</p><Button type="submit" disabled={busy || data.loading || data.stale || !reason.trim()}>{busy ? '提交中…' : '提交售后申请'}</Button></form>}</>}</div><div className="refund-actions"><Button variant="ghost" disabled={busy || data.loading} onClick={() => setRevision(v => v + 1)}><RotateCcw size={14} />{data.error ? '重新读取进度' : '刷新进度'}</Button><Button variant="ghost" disabled={busy} onClick={onBack}><ArrowLeft size={14} />返回订单</Button></div></div>
}
