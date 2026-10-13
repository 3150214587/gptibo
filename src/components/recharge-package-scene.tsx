import { useEffect, useMemo, useRef, useState } from 'react'
import { RechargeSceneCanvas } from './recharge-scene-canvas'
import type { CatalogPlan } from '@/lib/api'

function sceneDocument() {
  const escape = (value: string) => value.replaceAll('&', '&amp;').replaceAll('"', '&quot;').replaceAll('<', '&lt;')
  const styles = Array.from(document.querySelectorAll<HTMLLinkElement>('link[rel="stylesheet"]')).map(link => `<link rel="stylesheet" href="${escape(link.href)}">`).join('')
  const scripts = Array.from(document.querySelectorAll<HTMLScriptElement>('script[type="module"][src]')).map(script => `<script type="module" src="${escape(script.src)}"></script>`).join('')
  return `<!doctype html><html lang="zh-CN"><head><base href="${escape(location.origin)}/"><meta name="viewport" content="width=device-width, initial-scale=1.0">${styles}</head><body><div id="root"></div>${scripts}</body></html>`
}

export function RechargePackageScene({ offers, elapsed, reduced, running, view = 'package' }: { offers: CatalogPlan[]; elapsed: number; reduced: boolean; running: boolean; view?: 'package' | 'payment' | 'session' | 'result' }) {
  const frame = useRef<HTMLIFrameElement>(null)
  const [dark, setDark] = useState(document.documentElement.classList.contains('dark'))
  const source = useMemo(() => sceneDocument(), [])
  const phase = view === 'session' ? reduced ? 2 : elapsed < 1400 ? 0 : elapsed < 3700 ? 1 : elapsed < 6500 ? 2 : 3
    : view === 'result' ? reduced || elapsed >= 2600 ? 1 : 0
    : view === 'payment' ? reduced ? 1 : elapsed < 900 ? 0 : elapsed < 4200 ? 1 : elapsed < 5800 ? 2 : 3
    : reduced ? 2 : elapsed < 1200 ? 0 : elapsed < 2400 ? 1 : elapsed < 4500 ? 2 : elapsed < 6500 ? 3 : 4
  const state = useRef({ offers, phase, dark, reduced, view, running })
  useEffect(() => { state.current = { offers, phase, dark, reduced, view, running }; frame.current?.contentWindow?.postMessage({ type: 'recharge-guide/site-scene', ...state.current }, location.origin) }, [offers, phase, dark, reduced, view, running])
  useEffect(() => {
    const ready = (event: MessageEvent) => {
      if (event.origin === location.origin && event.source === frame.current?.contentWindow && event.data?.type === 'recharge-guide/site-ready') frame.current?.contentWindow?.postMessage({ type: 'recharge-guide/site-scene', ...state.current }, location.origin)
    }
    window.addEventListener('message', ready)
    const observer = new MutationObserver(() => setDark(document.documentElement.classList.contains('dark')))
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ['class'] })
    return () => { window.removeEventListener('message', ready); observer.disconnect() }
  }, [])
  return <RechargeSceneCanvas>{width => <div className="rg-package-reference" data-phase={phase} data-view={view}><iframe ref={frame} srcDoc={source} width={width} height={720} data-guide-scene="package" title="本站下单、付款与充值流程" tabIndex={-1} inert /></div>}</RechargeSceneCanvas>
}
