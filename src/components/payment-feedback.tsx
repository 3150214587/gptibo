import { useEffect, useState, type ComponentProps, type ReactNode } from 'react'
import { Check, CreditCard, LoaderCircle, TriangleAlert, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import '@/styles/payment-feedback.css'

// Both labels occupy the same grid cell: network feedback never changes width.
export function AsyncButton({ loading, pendingLabel, children, disabled, className = '', ...props }: ComponentProps<typeof Button> & { loading?: boolean; pendingLabel: string }) {
  return <Button {...props} className={`async-button ${className}`} disabled={disabled || loading} aria-busy={loading || undefined}>
    <span className="async-button-label" aria-hidden={loading} data-hidden={Boolean(loading)}>{children}</span>
    <span className="async-button-label" aria-hidden={!loading} data-hidden={!loading}><LoaderCircle className="processing-spinner" aria-hidden="true" />{pendingLabel}</span>
  </Button>
}

export function RequestNotice({ label, slowLabel = '响应比平时慢，请稍候。无需重复提交。' }: { label: string; slowLabel?: string }) {
  const [slow, setSlow] = useState(false)
  useEffect(() => { const timer = setTimeout(() => setSlow(true), 6000); return () => clearTimeout(timer) }, [])
  return <div className="request-notice" role="status"><span className="request-indicator" aria-hidden="true" /><span>{slow ? slowLabel : label}</span></div>
}

export function PaymentMark({ state }: { state: 'loading' | 'success' | 'expired' | 'closed' | 'idle' }) {
  return <div className={`payment-status-mark payment-status-${state}`} aria-hidden="true">
    {state === 'success' ? <span className="t-success-check" data-state="in"><svg width="32" height="32" viewBox="0 0 40 40" fill="none"><path pathLength="1" d="m10 20 7 7 14-14" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round" /></svg></span>
      : state === 'loading' ? <LoaderCircle className="processing-spinner" /> : state === 'expired' ? <TriangleAlert /> : state === 'closed' ? <X /> : <CreditCard />}
  </div>
}

export function PaymentMessage({ state, title, children }: { state: ComponentProps<typeof PaymentMark>['state']; title: string; children: ReactNode }) {
  return <div className="payment-message state-enter" key={state} role="status"><PaymentMark state={state} /><strong>{title}</strong><p>{children}</p></div>
}

export function PaymentSkeleton() {
  return <div className="payment-code-skeleton" aria-hidden="true"><span /><span /><span /><CreditCard size={26} strokeWidth={1.3} /></div>
}

export function PaymentConfirmed({ children }: { children: ReactNode }) {
  return <div className="inline-confirm payment-confirmed" role="status"><Check size={17} aria-hidden="true" />{children}</div>
}
