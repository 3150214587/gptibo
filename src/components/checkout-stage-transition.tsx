import { useEffect, useState, type ReactNode } from 'react'
import { AnimatedHeight } from './animated-height'
import '@/styles/interaction-polish.css'
import '@/styles/motion.css'

type Stage = 'input' | 'processing' | 'other'

// Keep the outgoing form visible through its exit, then reveal the next stage.
export function CheckoutStageTransition({ stage, children }: { stage: Stage; children: (stage: Stage) => ReactNode }) {
  const [shown, setShown] = useState(stage)
  const leaving = shown !== stage
  useEffect(() => {
    if (!leaving) return
    const preference = matchMedia('(prefers-reduced-motion: reduce)')
    const finish = () => setShown(stage)
    const duration = parseFloat(getComputedStyle(document.documentElement).getPropertyValue('--text-swap-dur')) || 150
    const timer = setTimeout(finish, preference.matches ? 0 : duration)
    const changed = () => { if (preference.matches) finish() }
    preference.addEventListener('change', changed)
    return () => { clearTimeout(timer); preference.removeEventListener('change', changed) }
  }, [stage, leaving])
  return <AnimatedHeight className="live-checkout-stage-height"><div key={shown} className="live-checkout-stage state-enter" data-leaving={leaving} data-stage={shown} inert={leaving}>{children(shown)}</div></AnimatedHeight>
}
