import { useLayoutEffect } from 'react'

export function useScrollReveal(page: string) {
  useLayoutEffect(() => {
    const media = matchMedia('(prefers-reduced-motion: reduce)')
    const elements = [...document.querySelectorAll<HTMLElement>('[data-scroll-reveal]')]
    const observer = new IntersectionObserver(entries => {
      for (const entry of entries) if (entry.isIntersecting) { (entry.target as HTMLElement).dataset.reveal = 'visible'; observer.unobserve(entry.target) }
    }, { threshold: .12 })
    if (!media.matches) for (const element of elements) { element.dataset.reveal = 'waiting'; observer.observe(element) }
    const reduce = () => { if (media.matches) { observer.disconnect(); elements.forEach(element => delete element.dataset.reveal) } }
    media.addEventListener('change', reduce)
    return () => { observer.disconnect(); media.removeEventListener('change', reduce); elements.forEach(element => delete element.dataset.reveal) }
  }, [page])
}
