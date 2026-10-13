// Schedule from completion: slow requests never overlap or queue extra polls.
// A hidden tab stops polling and resumes immediately when the user returns.
export function visiblePolling(work: () => Promise<unknown>, interval: number, doc: Pick<Document, 'hidden' | 'addEventListener' | 'removeEventListener'> = document, win: Pick<Window, 'addEventListener' | 'removeEventListener'> = window) {
  let stopped = false
  let running = false
  let timer: ReturnType<typeof setTimeout> | undefined
  const poll = async () => {
    if (stopped || running) return
    clearTimeout(timer)
    if (doc.hidden) return
    running = true
    try { await work() } catch { /* The caller displays errors; polling still recovers. */ }
    finally {
      running = false
      if (!stopped && !doc.hidden) timer = setTimeout(() => { void poll() }, interval)
    }
  }
  const resume = () => { void poll() }
  doc.addEventListener('visibilitychange', resume)
  win.addEventListener('focus', resume)
  void poll()
  return () => {
    stopped = true
    clearTimeout(timer)
    doc.removeEventListener('visibilitychange', resume)
    win.removeEventListener('focus', resume)
  }
}
