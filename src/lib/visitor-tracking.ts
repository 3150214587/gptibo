import { storefrontScopeHeaders } from './storefront-routing'
let lastPage = ''
let lastSent = 0
let ephemeralId = ''

export function trackVisitor(path: string) {
  const now = Date.now()
  if (path === lastPage && now - lastSent < 1500) return
  lastPage = path
  lastSent = now
  let visitorId: string
  try {
    const stored = localStorage.getItem('ymx-visitor-id')
    visitorId = stored && /^[a-f0-9-]{36}$/.test(stored) ? stored : crypto.randomUUID()
    localStorage.setItem('ymx-visitor-id', visitorId)
  } catch { visitorId = ephemeralId ||= crypto.randomUUID() }
  void fetch('/api/visitors', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...storefrontScopeHeaders({ pathname: path }) },
    body: JSON.stringify({ visitorId, path }),
    credentials: 'same-origin',
    keepalive: true,
  }).catch(() => { /* Analytics must never interrupt shopping. */ })
}
