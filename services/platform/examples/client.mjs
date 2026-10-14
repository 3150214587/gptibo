import { createHash, createHmac, randomUUID } from 'node:crypto'

const base = process.env.PLATFORM_BASE_URL || 'https://supply.example.invalid'
const keyId = process.env.PLATFORM_KEY_ID
const secret = process.env.PLATFORM_API_SECRET
if (!keyId || !secret) throw new Error('Set PLATFORM_KEY_ID and PLATFORM_API_SECRET on your server')
const encode = value => encodeURIComponent(value).replace(/[!'()*]/g, c => '%' + c.charCodeAt(0).toString(16).toUpperCase())
export async function request(path, body, idempotency = '') {
  const url = new URL('/v1' + path, base), method = body ? 'POST' : 'GET', raw = body ? JSON.stringify(body) : ''
  const timestamp = String(Math.floor(Date.now() / 1000)), nonce = randomUUID()
  const query = [...url.searchParams].map(([k, v]) => [encode(k), encode(v)]).sort(([ak, av], [bk, bv]) => ak < bk ? -1 : ak > bk ? 1 : av < bv ? -1 : av > bv ? 1 : 0).map(([k, v]) => `${k}=${v}`).join('&')
  const canonical = [method, url.pathname, query, timestamp, nonce, keyId, idempotency, createHash('sha256').update(raw).digest('hex')].join('\n')
  const response = await fetch(url, { method, body: body ? raw : undefined, redirect: 'error', signal: AbortSignal.timeout(30000), headers: { 'Content-Type': 'application/json', 'X-Key-Id': keyId, 'X-Timestamp': timestamp, 'X-Nonce': nonce, 'X-Signature': createHmac('sha256', secret).update(canonical).digest('hex'), ...(idempotency ? { 'Idempotency-Key': idempotency } : {}) } })
  const result = await response.json()
  if (!response.ok) throw new Error(result.error?.message || 'Request unconfirmed; query the original order')
  return result.data
}

// Running the example only reads your own balance; it never creates a charge.
if (import.meta.url === new URL(process.argv[1], 'file:///').href) console.log(await request('/balance'))
