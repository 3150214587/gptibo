import { useEffect, useRef, useState } from 'react'
import { Check, LoaderCircle, TriangleAlert } from 'lucide-react'
import { ProductLogo, ProductMark } from './brand'
import '@/styles/x-premium.css'

export function XPremiumScene() {
  const ref = useRef<HTMLDivElement>(null)
  const [paused, setPaused] = useState(false)
  useEffect(() => {
    let visible = true
    const update = () => setPaused(!visible || document.hidden)
    const observer = new IntersectionObserver(([entry]) => { visible = entry.isIntersecting; update() }, { threshold: .1 })
    if (ref.current) observer.observe(ref.current)
    document.addEventListener('visibilitychange', update)
    update()
    return () => { observer.disconnect(); document.removeEventListener('visibilitychange', update) }
  }, [])
  return <div ref={ref} className="x-premium-scene" data-paused={paused} aria-hidden="true">
    <div className="x-card-float">
      <div className="x-member-card">
        <div className="x-scene-wordmark"><ProductLogo productId="x-premium" /><span>Premium</span></div>
        <div className="x-card-caption"><span>MEMBERSHIP</span><span>3 / 6 <small>MONTHS</small></span></div>
        <div className="x-card-sheen" />
      </div>
    </div>
  </div>
}

export function XAccount({ username }: { username: string }) {
  return <div className="x-account"><ProductMark small productId="x-premium" /><div><span>充值账号</span><strong>@{username}</strong></div><span className="x-account-tag">Premium</span></div>
}

export function XRechargeStatus({ status, state }: { status: 'session' | 'processing' | 'success' | 'failed'; state: string }) {
  const success = status === 'success'
  const failed = status === 'failed'
  const pending = !success && !failed
  const verifying = state === 'unknown'
  const title = success ? '订单已完成' : failed ? '充值未完成' : verifying ? '正在核对结果' : '正在充值 Premium'
  return <section className="x-recharge-status" data-state={success ? 'success' : failed ? 'failed' : 'processing'} role="status" aria-live="polite" aria-atomic="true">
    <div className="x-status-art" aria-hidden="true"><div className="x-status-orbit" /><div className="x-status-core">{success ? <Check size={34} strokeWidth={2.4} /> : failed ? <TriangleAlert size={30} /> : <ProductLogo productId="x-premium" />}</div><span className="x-status-satellite" /></div>
    <h3>{title}</h3>
    <p>{success ? '赠送付款已完成，可前往 X 查看 Premium 状态。' : failed ? '请联系下方支持处理，无需重新付款。' : verifying ? '正在确认原订单，无需重新下单。' : '进度会自动更新，稍后也可在订单中查看。'}</p>
    {!failed && <ol className="x-status-milestones" aria-label="充值进度"><li data-done="true"><Check size={13} />付款确认</li><li data-done={success}>{pending ? <LoaderCircle className="processing-spinner" size={13} /> : <Check size={13} />}赠送付款</li><li data-done={success}>{success ? <Check size={13} /> : <span className="x-milestone-dot" />}已完成</li></ol>}
  </section>
}
