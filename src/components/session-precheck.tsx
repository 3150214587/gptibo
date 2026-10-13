import { Check, LoaderCircle, TriangleAlert, UserRound } from 'lucide-react'
import { AnimatedHeight } from './animated-height'
import '@/styles/interaction-polish.css'
import '@/styles/session-precheck.css'

export type SessionPrecheck = {
  state: 'eligible' | 'wait' | 'expired' | 'invalid' | 'ineligible' | 'unknown' | 'limited' | 'timeout' | 'recovery' | 'deferred'
  code: string; target: string; eligibleAt?: string | null; checkedAt?: string; receipt?: string
}
export type CheckPhase = 'idle' | 'checking' | 'submitting'

// The existing target-account card owns the entire check state, including errors.
export function SessionAccountCard({ email, phase, result }: { email: string | null; phase: CheckPhase; result: SessionPrecheck | null }) {
  if (!email && phase === 'idle' && !result) return null
  const checking = phase === 'checking'
  const passed = !checking && result?.state === 'eligible'
  const failed = !checking && result && !['eligible', 'recovery', 'deferred'].includes(result.state)
  const state = checking ? 'checking' : passed ? 'passed' : failed ? 'failed' : result?.state === 'deferred' ? 'deferred' : 'identified'
  const copy: Record<SessionPrecheck['state'], [string, string]> = {
    eligible: ['检测通过', '检测通过，正在提交充值。'],
    wait: ['暂不可充值', result?.code === 'store_wait_72h' ? 'iOS／Google Play 订阅到期后等 72 小时。' : '网页订阅到期后可续费。'],
    expired: ['Session 已过期', '请重新获取并粘贴完整 Session。'],
    invalid: ['校验无效', '当前 Session 无效，请重新获取 Session 重试，或联系客服。'],
    ineligible: ['不符合升级条件', '您当前订阅的账单地区不符合升级条件。'],
    limited: ['查询繁忙', '查询限流，请稍后重试。'],
    timeout: ['检测超时', '检测超时，请重新检测。'],
    unknown: ['暂无法确认', '订阅或账单信息不全，请重试。'],
    deferred: ['继续提交', '正在提交充值，由充值服务继续核验。'],
    recovery: ['核对原提交', '正在核对原提交，请稍候。'],
  }
  const [label, description] = checking ? ['检测中', '正在检测，通过后自动充值。'] : result ? copy[result.state] : ['已识别', '']
  const pending = checking || result?.state === 'deferred'
  const Icon = pending ? LoaderCircle : failed ? TriangleAlert : passed ? Check : UserRound
  const eligibleTime = result?.eligibleAt ? Date.parse(result.eligibleAt) : NaN
  const contentKey = checking ? 'checking' : result ? `${result.state}:${result.code}` : 'identified'
  return <div className="live-account live-account-check state-enter" data-check-state={state} role="status" aria-live="polite" aria-atomic="true" aria-busy={checking}>
    <AnimatedHeight className="live-account-check-height">
    <div className="live-account-avatar"><Icon key={state} size={16} className={pending ? 'processing-spinner' : 'state-enter'} /></div>
    <div className="live-account-info"><span className="live-account-label">充值目标账号</span><strong className="live-account-email">{email || '已提交的账号'}</strong></div>
    <span key={contentKey} className="live-account-badge state-enter">{!pending && !failed && <Check size={12} strokeWidth={2.5} />}{label}</span>
    {description && <div key={`detail:${contentKey}`} className="live-account-check-detail state-enter"><p>{description}</p>
      {result?.state === 'wait' && !checking && Number.isFinite(eligibleTime) && <p className="live-account-check-date"><span>可充值时间</span><time dateTime={result.eligibleAt!}>{new Date(eligibleTime).toLocaleString('zh-CN', { timeZone: 'Asia/Shanghai', hour12: false })}</time></p>}
    </div>}
    </AnimatedHeight>
  </div>
}
