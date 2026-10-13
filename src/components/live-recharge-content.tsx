import { useState } from 'react'
import { ArrowRight, ExternalLink, LoaderCircle, LockKeyhole, TriangleAlert } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Field, FieldLabel } from '@/components/ui/field'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { SuccessMark } from './brand'
import { RechargeSuccessContacts } from './recharge-success-contacts'
import type { SupportContacts } from '../../shared/support-contacts'
import { AsyncButton, RequestNotice } from './payment-feedback'
import { checkoutSession } from '@/lib/checkout-session'
import { SessionAccountCard, type SessionPrecheck } from './session-precheck'
import { RECHARGE_STAGES, rechargeStage } from '../../shared/recharge-progress'
import '@/styles/live-checkout.css'

type SessionFormProps = {
  session: string; confirmed: boolean; touched: boolean; busy?: '' | 'query' | 'check' | 'submit'
  precheckRequired?: boolean; precheck?: SessionPrecheck | null
  retryMode?: 'session' | 'access_token' | null; retryKey?: string | null; failed?: boolean
  onSessionChange: (value: string) => void; onConfirmChange: (value: boolean) => void
  onTouch: () => void; onSubmit: () => void
}

// Shared by the actual order and the isolated walkthrough, with no API side effects.
export function LiveSessionForm({ session, confirmed, touched, busy = '', precheckRequired, precheck = null, retryMode = null, retryKey, failed, onSessionChange, onConfirmChange, onTouch, onSubmit }: SessionFormProps) {
  const input = checkoutSession(session, retryMode)
  // Submission clears the sensitive draft immediately; keep only its display email.
  const [lastEmail, setLastEmail] = useState(input.email)
  if (busy !== 'submit' && !['eligible', 'deferred'].includes(precheck?.state ?? '') && input.email !== lastEmail) setLastEmail(input.email)
  const displayEmail = input.email || (busy === 'submit' || ['eligible', 'deferred'].includes(precheck?.state ?? '') ? lastEmail : null)
  return <form onSubmit={e => { e.preventDefault(); onSubmit() }} className="live-session-form">
          <div className="live-session-heading"><Label htmlFor="live-session">ChatGPT Session <LockKeyhole size={14} /></Label><a href="https://chatgpt.com/api/auth/session" target="_blank" rel="noopener noreferrer">获取 Session <ExternalLink size={12} /></a></div>
          <p className="live-session-hint" id="live-session-hint">在已登录的 ChatGPT 中打开获取入口，复制完整 JSON。</p>
          <Textarea id="live-session" value={session} onChange={e => onSessionChange(e.target.value)} onBlur={onTouch} rows={4} maxLength={32000} autoComplete="off" autoCorrect="off" spellCheck={false} disabled={Boolean(busy)} aria-invalid={touched && Boolean(input.problem)} aria-describedby="live-session-hint live-session-validation" placeholder="在这里粘贴完整 Session JSON" />
          <div className="live-session-validation" id="live-session-validation">{touched && input.problem ? <p role="alert"><TriangleAlert size={12} />{input.problem}</p> : <p><LockKeyhole size={12} />资料将加密留存，仅用于本订单充值及失败补发。</p>}</div>
          <SessionAccountCard email={displayEmail} phase={precheckRequired && busy === 'check' ? 'checking' : busy === 'submit' ? 'submitting' : 'idle'} result={precheckRequired ? precheck : null} />
          <Field orientation="horizontal" className="live-account-confirmation" data-disabled={Boolean(busy) || !input.credential}>
            <Checkbox id="live-account-confirmation" checked={confirmed} onCheckedChange={onConfirmChange} disabled={Boolean(busy) || !input.credential} />
            <FieldLabel htmlFor="live-account-confirmation">我已核对账号，并授权本次充值</FieldLabel>
          </Field>
          <AsyncButton type="submit" className="primary-action" loading={busy === 'submit' || busy === 'check'} pendingLabel={busy === 'check' ? '正在检测账号' : retryKey ? '正在核对原提交' : '正在提交充值'} disabled={Boolean(busy) || !input.credential || !confirmed}>{retryKey ? '核对原提交' : precheckRequired ? precheck ? '重新检测并充值' : '检测账号并充值' : failed ? '重新提交 Session' : '提交并充值'} <ArrowRight size={16} /></AsyncButton>
          {precheckRequired && !precheck && !busy && !retryKey && <p className="live-session-hint">先核验账号与订阅条件，通过后自动提交充值。</p>}
          {busy === 'submit' && !precheck && <RequestNotice label="正在提交，完成后自动显示充值进度。" />}
        </form>
}

function RechargeStageLabel({ title }: { title: string }) {
  const [swap, setSwap] = useState({ current: title, previous: null as string | null, revision: 0 })
  // Keep the outgoing label mounted for its exit; unchanged polling never restarts motion.
  if (swap.current !== title) setSwap({ current: title, previous: swap.current, revision: swap.revision + 1 })
  return <div className="live-stage-swap">
    {swap.previous !== null && <p key={`out-${swap.revision}`} className="live-current-stage live-watch live-stage-out" aria-hidden="true"><i />{swap.previous}</p>}
    <p key={`in-${swap.revision}`} className={`live-current-stage live-watch${swap.previous !== null ? ' live-stage-in' : ''}`} onAnimationEnd={event => {
      if (event.target === event.currentTarget) setSwap(current => current.revision === swap.revision && current.previous !== null ? { ...current, previous: null } : current)
    }}><i aria-hidden="true" />{swap.current}</p>
  </div>
}

export function LiveRechargeProcessing({ progressStage, progressUpdatedAt, rechargeStatus, syncError = false }: { progressStage?: string | null; progressUpdatedAt?: number | null; rechargeStatus?: string | null; syncError?: boolean }) {
  const stage = rechargeStage(progressStage)
  const activeStage = stage && !['completed', 'failed', 'cancelled'].includes(stage) ? stage : null
  const progress = activeStage ? RECHARGE_STAGES[activeStage] : rechargeStatus === 'queued' ? RECHARGE_STAGES.queued : { title: '充值处理中', description: '正在等待充值结果，进度有变化时会自动更新。' }
  return <div className="result-body live-processing" role="status" aria-live="polite" aria-atomic="true">
    <div className="result-icon"><LoaderCircle className="processing-spinner" size={34} strokeWidth={1.5} /></div>
    <h3>正在充值</h3>
    <RechargeStageLabel title={progress.title} />
    <div className="processing-track" aria-hidden="true"><span /></div>
    {syncError && <span className="live-progress-help">进度暂未更新，正在核对原任务</span>}
    {progressUpdatedAt != null && <span className="live-progress-time">更新于 <time dateTime={new Date(progressUpdatedAt).toISOString()}>{new Date(progressUpdatedAt).toLocaleTimeString('zh-CN', { hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit' })}</time></span>}
    {activeStage === 'requires_action' && <span className="live-progress-help">请联系下方客服核对，无需重新付款。</span>}
  </div>
}

export function LiveRechargeSuccess({ accountEmailMasked, finishedAt, onOrders, supportContacts, partnerName }: { accountEmailMasked?: string | null; finishedAt?: number | null; onOrders: () => void; supportContacts?: SupportContacts; partnerName?: string | null }) {
  const hasContacts = Boolean(partnerName && supportContacts && Object.values(supportContacts).some(value => value.trim()))
  return <div className={`result-body${hasContacts ? ' live-success-with-contacts' : ''}`}>
    <div className="live-success-layout">
      <div className="live-success-summary">
        <SuccessMark /><h3>充值已完成</h3>
        <p>{accountEmailMasked && <>账号：{accountEmailMasked}<br /></>}订阅已开通，可前往 ChatGPT 查看。</p>
      </div>
      <RechargeSuccessContacts contacts={supportContacts} name={partnerName} />
    </div>
    {finishedAt && <div className="result-receipt"><span>完成时间</span><span>{new Date(finishedAt).toLocaleString('zh-CN')}</span></div>}
    <Button className="primary-action" onClick={onOrders}>查看我的订单 <ArrowRight size={16} /></Button>
  </div>
}
