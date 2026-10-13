let timer: number | undefined
export function applyAppearance(dark: boolean) {
  const root = document.documentElement
  // Initial mount already has the saved class, so it never starts a color transition.
  if (root.classList.contains('dark') !== dark && !matchMedia('(prefers-reduced-motion: reduce)').matches) {
    clearTimeout(timer)
    root.classList.add('theme-transition')
    timer = window.setTimeout(() => root.classList.remove('theme-transition'), 300)
  }
  root.classList.toggle('dark', dark)
  root.style.colorScheme = dark ? 'dark' : 'light'
  try { localStorage.setItem('ymx-theme', dark ? 'dark' : 'light') } catch { /* keep the in-memory preference */ }
}
