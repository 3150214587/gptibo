import '@/styles/order-verification.css'
import { useEffect, useRef, useState } from 'react'
import { RotateCw } from 'lucide-react'
import { api } from '@/lib/api'

type Turnstile = {
  render: (element: HTMLElement, options: Record<string, unknown>) => string
  remove: (id: string) => void
}
declare global { interface Window { turnstile?: Turnstile } }

let scriptReady: Promise<Turnstile> | undefined
function loadTurnstile() {
  if (window.turnstile) return Promise.resolve(window.turnstile)
  return scriptReady ??= new Promise<Turnstile>((resolve, reject) => {
    const script = document.createElement('script')
    script.src = 'https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit'
    script.async = true
    const fail = () => { clearTimeout(timer); script.remove(); scriptReady = undefined; reject(new Error('verification unavailable')) }
    const timer = setTimeout(fail, 15000)
    script.onload = () => { clearTimeout(timer); if (window.turnstile) resolve(window.turnstile); else fail() }
    script.onerror = fail
    document.head.appendChild(script)
  })
}

// Read public configuration at checkout time so API enforcement and widget keys agree.
export function OrderVerification({ onChange, action = "order_create", label = "安全验证" }: { action?: string; label?: string; onChange: (value: { ready: boolean; token: string }) => void }) {
  const container = useRef<HTMLDivElement>(null)
  const [enabled, setEnabled] = useState(true)
  const [error, setError] = useState('')
  const [phase, setPhase] = useState<'loading' | 'verifying' | 'passed' | 'expired' | 'error'>('loading')
  const [retry, setRetry] = useState(0)
  useEffect(() => {
    let cancelled = false
    let widget: string | undefined
    let client: Turnstile | undefined
    onChange({ ready: false, token: '' })
    const abort = new AbortController()
    void api<{ enabled: boolean; siteKey?: string; purpose?: string }>('/api/orders/verification' + (action === 'order_create' ? '' : '?purpose=auth'), undefined, abort.signal).then(async config => {
      if (cancelled) return
      if (config.enabled === false || (action !== "order_create" && config.purpose !== "auth")) { setEnabled(false); onChange({ ready: true, token: '' }); return }
      if (config.enabled !== true || !config.siteKey) throw new Error('missing site key')
      client = await loadTurnstile()
      if (cancelled || !container.current) return
      widget = client.render(container.current, {
        sitekey: config.siteKey, action, theme: 'auto', size: container.current.clientWidth < 300 ? 'compact' : 'flexible', language: 'zh-cn',
        'response-field': false,
        callback: (token: string) => { if (!cancelled) { setError(''); setPhase('passed'); onChange({ ready: true, token }) } },
        'expired-callback': () => { if (!cancelled) { setError('验证已过期，请重新验证。'); setPhase('expired'); onChange({ ready: false, token: '' }) } },
        'error-callback': () => { if (!cancelled) { setError('验证暂时不可用，请重试。'); setPhase('error'); onChange({ ready: false, token: '' }) } },
        'timeout-callback': () => { if (!cancelled) { setError('验证超时，请重试。'); setPhase('error'); onChange({ ready: false, token: '' }) } },
      })
      setPhase(current => current === 'loading' ? 'verifying' : current)
    }).catch(() => { if (!cancelled) { setError('验证暂时不可用，请检查网络后重试。'); setPhase('error') } })
    return () => { cancelled = true; abort.abort(); if (widget !== undefined) client?.remove(widget) }
  }, [retry, onChange, action])
  return <div className="order-verification" hidden={!enabled} role="group" aria-label={label}>
    <div className="order-verification-heading">
      <span>{label}</span>
      {(phase === 'error' || phase === 'expired') && <button className="order-verification-retry" type="button" onClick={() => { onChange({ ready: false, token: '' }); setError(''); setPhase('loading'); setRetry(v => v + 1) }}><RotateCw size={14} aria-hidden="true" />重新验证</button>}
    </div>
    <div className="order-verification-body" data-phase={phase}>
      <div className="order-verification-widget" ref={container} />
      {phase === 'loading' && <div className="order-verification-loading" role="status">正在加载验证…</div>}
    </div>
    {error && <p className="order-verification-error" role="alert">{error}</p>}
  </div>
}
