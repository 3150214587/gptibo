// Reloading /admin can reuse an Access cookie that the origin no longer accepts.
// Cloudflare owns this HttpOnly cookie; let its logout endpoint clear/revoke it.
export async function restartAdminAccess() {
  const response = await fetch('/cdn-cgi/access/logout', {
    credentials: 'same-origin', cache: 'no-store', redirect: 'manual',
    signal: AbortSignal.timeout(15000),
  })
  if (!response.ok && response.type !== 'opaqueredirect' && !(response.status >= 300 && response.status < 400)) {
    throw new Error('Access logout failed')
  }
  // Do not restore the cached SPA from the same URL after clearing the cookie.
  window.location.replace('/admin?access_refresh=' + Date.now())
}
