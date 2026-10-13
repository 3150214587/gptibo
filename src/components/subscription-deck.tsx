import { useStorefrontI18n } from '@/lib/storefront-i18n'
import { useEffect, useRef } from 'react'
import { formatCatalogPrice, type DisplaySubscriptionPlan } from '@/lib/catalog-display'
import { DECK_CYCLE_MS, deckFrame, deckTimeline, fanFrame, mixPose } from '@/lib/subscription-motion'

export function SubscriptionDeck({ plans }: { plans: DisplaySubscriptionPlan[] }) {
  const { t } = useStorefrontI18n()
  const deck = useRef<HTMLDivElement>(null)
  const hovered = useRef(false)
  const focused = useRef(false)
  useEffect(() => {
    const element = deck.current
    if (!element) return
    const cards = Array.from(element.querySelectorAll<HTMLDivElement>('[data-tier]'))
    const reduced = matchMedia('(prefers-reduced-motion: reduce)')
    let index = 0, elapsed = 0, last: number | null = null, frame = 0
    let expanded = false, settling = 0
    let current = deckFrame(0, 0, cards[0].offsetWidth)
    const tick = (now: number) => {
      const delta = last === null ? 0 : now - last
      last = now
      if (!document.hidden) {
        const open = hovered.current || focused.current
        if (open !== expanded) {
          if (!open) elapsed = 0
          expanded = open
          settling = 600
        }
        const cycle = reduced.matches ? 3000 : DECK_CYCLE_MS
        if (!open && settling === 0) elapsed += delta
        if (elapsed >= cycle) { index = (index + Math.floor(elapsed / cycle)) % cards.length; elapsed %= cycle }
        const { progress, phase } = deckTimeline(reduced.matches ? 0 : elapsed)
        const target = open ? fanFrame(element.clientWidth, cards[0].offsetWidth, element.clientHeight) : deckFrame(index, progress, cards[0].offsetWidth, phase)
        const blend = reduced.matches || settling === 0 ? 1 : 1 - Math.exp(-delta / 80)
        current = target.map((pose, i) => ({ ...pose, position: mixPose(current[i].position, pose.position, blend), scale: current[i].scale + (pose.scale - current[i].scale) * blend }))
        settling = Math.max(0, settling - delta)
        element.dataset.phase = open ? 'inspect' : phase
        cards.forEach((card, i) => {
          const { position: p, scale, z, visible } = current[i]
          card.style.transform = `translate(calc(-50% + ${p[0]}px),calc(-50% + ${p[1]}px)) rotate(${p[2]}deg) scale(${scale})`
          card.style.setProperty('--tier-z', String(z))
          card.dataset.copyVisible = String(visible)
        })
      }
      frame = requestAnimationFrame(tick)
    }
    const resetClock = () => { last = null }
    document.addEventListener('visibilitychange', resetClock)
    frame = requestAnimationFrame(tick)
    return () => { cancelAnimationFrame(frame); document.removeEventListener('visibilitychange', resetClock) }
  }, [])
  return <div className="store-deck" ref={deck} role="group" tabIndex={0} aria-label={t('ChatGPT 四档月度套餐，悬停或聚焦展开全部')}
    onPointerEnter={event => { if (event.pointerType === 'mouse') hovered.current = true }}
    onPointerLeave={() => { hovered.current = false; focused.current = false }}
    onFocus={() => { focused.current = true }}
    onBlur={() => { focused.current = false }}>
    {plans.map((plan, i) => <div className="store-tier" data-tier={i} key={plan.id}>
      <div className="store-tier-face">
      <span className="store-tier-copy"><span className="store-tier-brand">ChatGPT</span><strong className="store-tier-name">{plan.name}</strong></span>
      <span className="store-tier-price">{plan.price === null ? <small>{t('价格待加载')}</small> : <>¥{formatCatalogPrice(plan.price)}<small>{t('/ 月')}</small></>}</span>
      </div>
    </div>)}
  </div>
}
