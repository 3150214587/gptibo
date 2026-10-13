import { useCallback, useEffect, useRef, useState } from 'react'
import { api, errorMessage } from './api'

// Keep authentication changes separate from ordinary query errors.
export function useSession<T extends { authenticated: boolean }>(path: string, scope: 'admin' | 'partner', enabled = true) {
  const [session, setSession] = useState<T>()
  const [error, setError] = useState('')
  const generation = useRef(0)
  const clear = useCallback(() => {
    generation.current++
    setSession(previous => previous ? { ...previous, authenticated: false } : undefined)
  }, [])
  const check = useCallback(async () => {
    const request = ++generation.current
    try {
      const result = await api<T>(path)
      if (request === generation.current) { setSession(result); setError('') }
    } catch (e) { if (request === generation.current) setError(errorMessage(e)) }
  }, [path])
  useEffect(() => {
    if (!enabled) return
    // Synchronize with the server when the workspace opens.
    // eslint-disable-next-line react/set-state-in-effect
    void check()
    const visible = () => { if (!document.hidden) void check() }
    const expired = (event: Event) => {
      if ((event as CustomEvent<string>).detail !== scope) return
      clear()
      setError('登录已失效，请重新登录。')
    }
    document.addEventListener('visibilitychange', visible)
    window.addEventListener('ymx-session-expired', expired)
    return () => {
      // This is a request generation, not a DOM ref: invalidate the latest request.
      // eslint-disable-next-line react-hooks/exhaustive-deps
      generation.current++
      document.removeEventListener('visibilitychange', visible)
      window.removeEventListener('ymx-session-expired', expired)
    }
  }, [check, clear, enabled, scope])
  return { session, error, setError, check, clear }
}
