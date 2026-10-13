import { useEffect, useRef, useState, type CSSProperties } from 'react'
import type { CatalogPlan } from '@/lib/api'

function RollingNumber({ value }: { value: string }) {
  let digitIndex = 0
  return <span className="rolling-number" data-long={value.replaceAll(',', '').length > 5 || undefined} aria-hidden="true">{Array.from(value, (character, index) => {
    if (character === ',') return <span className="rolling-comma" key={index}>,</span>
    if (character === '.') return <span key={index}>.</span>
    const order = digitIndex++
    return <span className="rolling-digit" key={index}><span className="rolling-reel" style={{ '--digit-stop': Number(character) + 20, '--reel-order': order } as CSSProperties}>{Array.from({ length: 30 }, (_, step) => <span key={step}>{step % 10}</span>)}</span></span>
  })}</span>
}

export function HeroStats({ offers }: { offers: CatalogPlan[] }) {
  const elementRef = useRef<HTMLElement>(null)
  const [visible, setVisible] = useState(false)
  const tiers = offers.filter(plan => plan.product_id === 'chatgpt').length
  const smsPrice = offers.find(plan => plan.id === 'codex-sms-once')?.price_cents
  useEffect(() => {
    const element = elementRef.current
    if (!element) return
    const observer = new IntersectionObserver(entries => {
      if (entries.some(entry => entry.isIntersecting)) { setVisible(true); observer.disconnect() }
    }, { threshold: .6 })
    observer.observe(element)
    return () => observer.disconnect()
  }, [])
  return <aside ref={elementRef} className="hero-proof" data-visible={visible || undefined} aria-label="服务与价格">
    <div className="hero-proof-heading"><span>适合你的选择，清楚的价格。</span></div>
    <dl className="hero-stats">
      <div><dt>ChatGPT 订阅档位</dt><dd>{tiers ? <><span className="sr-only">{tiers} 档</span><RollingNumber key={tiers} value={String(tiers)} /><small aria-hidden="true">档</small></> : '—'}</dd></div>
      <div><dt>Codex 单次接码</dt><dd>{smsPrice !== undefined ? <><span className="sr-only">{smsPrice / 100} 元／次</span><small aria-hidden="true">¥</small><RollingNumber key={smsPrice} value={String(smsPrice / 100)} /></> : '—'}</dd></div>
    </dl>
  </aside>
}
