import { useLayoutEffect, useRef, type ReactNode } from 'react'
import { dialogViewport } from '@/lib/dialog-viewport'
import '@/styles/dialog-viewport.css'

export function DialogViewport({ children }: { children: ReactNode }) {
  const frame = useRef<HTMLDivElement>(null)
  useLayoutEffect(() => {
    const element = frame.current!, viewport = window.visualViewport
    let pending = 0
    function update() {
      const size = dialogViewport(window.innerHeight, viewport)
      element.style.setProperty('--dialog-viewport-height', `${size.height}px`)
      element.style.top = `${size.top}px`
      if (viewport && Math.abs(viewport.scale - 1) > .05) return
      // Scroll only the dialog pane, never the background or the whole page.
      const active = document.activeElement
      if (!(active instanceof HTMLElement) || !element.contains(active) || !active.matches('input,textarea,[contenteditable=true]')) return
      for (let pane = active.parentElement; pane && pane !== element; pane = pane.parentElement) {
        if (!/auto|scroll/.test(getComputedStyle(pane).overflowY) || pane.scrollHeight <= pane.clientHeight) continue
        const field = active.getBoundingClientRect(), bounds = pane.getBoundingClientRect()
        const bottom = Math.min(bounds.bottom, size.top + size.height - 16), top = Math.max(bounds.top, size.top + 16)
        const delta = field.bottom > bottom - 12 ? field.bottom - bottom + 12 : field.top < top + 12 ? field.top - top - 12 : 0
        if (delta) pane.scrollBy({ top: delta, behavior: 'instant' })
        break
      }
    }
    function schedule() {
      cancelAnimationFrame(pending)
      pending = requestAnimationFrame(() => {
        update()
        // Focus can also make the browser scroll the field after layout changes.
        pending = requestAnimationFrame(update)
      })
    }
    update()
    viewport?.addEventListener('resize', schedule)
    viewport?.addEventListener('scroll', schedule)
    window.addEventListener('resize', schedule)
    element.addEventListener('focusin', schedule)
    return () => { cancelAnimationFrame(pending); viewport?.removeEventListener('resize', schedule); viewport?.removeEventListener('scroll', schedule); window.removeEventListener('resize', schedule); element.removeEventListener('focusin', schedule) }
  }, [])
  return <div ref={frame} className="dialog-viewport" data-slot="dialog-viewport">{children}</div>
}
