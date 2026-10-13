import { credentialExpiryProblem } from '../../shared/credential-expiry.ts'

export function checkoutSession(value: string, retryMode?: 'session' | 'access_token' | null) {
  if (!value.trim()) return { email: '', problem: '', credential: null }
  let session: { user?: { email?: unknown }; accessToken?: unknown; expires?: unknown }
  try { session = JSON.parse(value) }
  catch { return { email: '', problem: '请复制并粘贴完整的 Session JSON。', credential: null } }
  if (!session || typeof session.user?.email !== 'string' || !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(session.user.email) || typeof session.accessToken !== 'string' || !session.accessToken.trim()) {
    return { email: '', problem: 'Session 缺少账号信息，请重新获取完整内容。', credential: null }
  }
  // An unknown original request must retain its exact payload, even if it has
  // since expired. The server independently verifies the original key/hash.
  const problem = retryMode ? '' : credentialExpiryProblem(session.accessToken, session.expires)
  if (problem) return { email: '', problem, credential: null }
  // Recover an existing uncertain token submission without changing its request body.
  const credential = retryMode === 'access_token' ? { mode: 'access_token' as const, access_token: session.accessToken } : { mode: 'session' as const, session: value }
  return { email: session.user.email, problem: '', credential }
}
