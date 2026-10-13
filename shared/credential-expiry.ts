// Input timestamps are only a rejection hint, never proof of authentication.
// Unknown formats must remain unknown until a live provider checks the credential.
export function credentialExpiryProblem(token: string, expires?: unknown, now = Date.now()): string {
  try {
    const parts = token.trim().split('.')
    if (parts.length === 3 && /^[A-Za-z0-9_-]+$/.test(parts[1])) {
      const base64 = parts[1].replace(/-/g, '+').replace(/_/g, '/')
      const bytes = Uint8Array.from(atob(base64.padEnd(Math.ceil(base64.length / 4) * 4, '=')), c => c.charCodeAt(0))
      const claims = JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(bytes))
      if (typeof claims?.exp === 'number' && Number.isFinite(claims.exp) && claims.exp * 1000 <= now) {
        return 'Session 中的 Access Token 已过期，请在 ChatGPT 重新获取完整 Session 后再提交。'
      }
    }
  } catch { /* An unreadable claim is not evidence of expiry. */ }
  if (typeof expires === 'string' && /^\d{4}-\d\d-\d\dT/.test(expires)) {
    const timestamp = Date.parse(expires)
    if (Number.isFinite(timestamp) && timestamp <= now) return 'Session 已超过有效期，请在 ChatGPT 重新获取完整 Session 后再提交。'
  }
  return ''
}
