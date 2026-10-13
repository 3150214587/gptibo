import { useEffect, useRef, useState, type ReactNode } from 'react'

export function RechargeSceneCanvas({ children }: { children: (width: number) => ReactNode }) {
  const viewport = useRef<HTMLDivElement>(null)
  const [size, setSize] = useState({ width: window.innerWidth, scale: 1 })
  useEffect(() => {
    const measure = () => {
      if (!viewport.current) return
      const width = window.innerWidth
      const scale = viewport.current.clientWidth / width
      setSize(previous => previous.width === width && previous.scale === scale ? previous : { width, scale })
    }
    measure()
    const observer = new ResizeObserver(measure)
    observer.observe(viewport.current!)
    window.addEventListener('resize', measure)
    return () => { observer.disconnect(); window.removeEventListener('resize', measure) }
  }, [])
  return <div ref={viewport} className="rg-scaled-viewport" style={{ height: 720 * size.scale }}><div className="rg-reference-canvas" style={{ width: size.width, height: 720, transform: `scale(${size.scale})` }}>{children(size.width)}</div></div>
}
