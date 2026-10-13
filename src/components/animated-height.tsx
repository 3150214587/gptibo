import { useLayoutEffect, useRef, type ReactNode } from 'react'

export function AnimatedHeight({ children, className = '' }: { children: ReactNode; className?: string }) {
  const viewport = useRef<HTMLDivElement>(null), content = useRef<HTMLDivElement>(null)
  useLayoutEffect(() => {
    const box = viewport.current!, inner = content.current!
    let frame: number | undefined
    const measure = () => {
      const height = `${inner.offsetHeight}px`
      if (box.style.height !== height) box.style.height = height
    }
    measure()
    // Nested account/checkout containers must not write layout in the same
    // ResizeObserver delivery cycle, which can trigger a resize-loop error.
    const observer = new ResizeObserver(() => {
      if (frame !== undefined) cancelAnimationFrame(frame)
      frame = requestAnimationFrame(() => { frame = undefined; measure() })
    })
    observer.observe(inner)
    return () => { observer.disconnect(); if (frame !== undefined) cancelAnimationFrame(frame) }
  }, [])
  return <div className={`animated-height ${className}`} ref={viewport}><div className="animated-height-content" ref={content}>{children}</div></div>
}
