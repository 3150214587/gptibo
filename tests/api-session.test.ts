import test from 'node:test'
import assert from 'node:assert/strict'
import { api, RequestError } from '../src/lib/api.ts'

test('Cloudflare redirects are handled before cross-origin fetch can hide the login flow', async t => {
  const target = new EventTarget()
  const descriptor = Object.getOwnPropertyDescriptor(globalThis, 'window')
  Object.defineProperty(globalThis, 'window', { configurable: true, value: target })
  t.after(() => { if (descriptor) Object.defineProperty(globalThis, 'window', descriptor); else Reflect.deleteProperty(globalThis, 'window') })
  let events = 0
  target.addEventListener('ymx-admin-access-required', () => events++)
  let opaque = true
  t.mock.method(globalThis, 'fetch', async (_path: string, options: RequestInit) => {
    assert.equal(options.redirect, 'manual')
    const response = new Response(null, {status:302})
    if (opaque) Object.defineProperty(response, 'type', {value:'opaqueredirect'})
    return response
  })
  await assert.rejects(api('/api/admin/session'), (e: unknown) => e instanceof RequestError && e.status === 403)
  opaque = false
  await assert.rejects(api('/api/admin/orders'))
  assert.equal(events, 2)
})

test('admin Access expiry requests interactive verification without confusing business denials', async t => {
  const target = new EventTarget()
  const descriptor = Object.getOwnPropertyDescriptor(globalThis, 'window')
  Object.defineProperty(globalThis, 'window', { configurable: true, value: target })
  t.after(() => { if (descriptor) Object.defineProperty(globalThis, 'window', descriptor); else Reflect.deleteProperty(globalThis, 'window') })
  let events = 0
  target.addEventListener('ymx-admin-access-required', () => events++)
  let code: string | undefined = 'admin_access_required'
  t.mock.method(globalThis, 'fetch', async () => Response.json({ error: 'verification required', code }, { status: 403 }))
  await assert.rejects(api('/api/admin/session'))
  await assert.rejects(api('/api/admin/orders'))
  assert.equal(events, 2)
  await assert.rejects(api('/api/partner/dashboard'))
  code = undefined
  await assert.rejects(api('/api/admin/orders'))
  assert.equal(events, 2)
})

test('protected API failures expire only the matching workspace; login mistakes and business errors do not', async t => {
  const events: string[] = []
  const target = new EventTarget()
  const descriptor = Object.getOwnPropertyDescriptor(globalThis, 'window')
  Object.defineProperty(globalThis, 'window', { configurable: true, value: target })
  t.after(() => { if (descriptor) Object.defineProperty(globalThis, 'window', descriptor); else Reflect.deleteProperty(globalThis, 'window') })
  target.addEventListener('ymx-session-expired', event => events.push((event as CustomEvent<string>).detail))
  let status = 401
  t.mock.method(globalThis, 'fetch', async () => Response.json({ error: 'test error' }, { status }))
  for (const path of ['/api/admin/orders', '/api/partner/dashboard', '/api/admin/partners/example/preview']) {
    await assert.rejects(api(path), (e: unknown) => e instanceof RequestError && e.status === 401)
  }
  assert.deepEqual(events, ['admin', 'partner', 'admin'])
  for (const path of ['/api/admin/login', '/api/partner/auth/verify', '/api/orders']) await assert.rejects(api(path))
  status = 409; await assert.rejects(api('/api/partner/prices/plus'))
  status = 503; await assert.rejects(api('/api/admin/orders'))
  assert.equal(events.length, 3)
})

test('navigation can cancel an obsolete read without an authentication event', async t => {
  const controller = new AbortController()
  let requestSignal: AbortSignal | undefined
  t.mock.method(globalThis, 'fetch', async (_path: string, options: RequestInit) => {
    requestSignal = options.signal as AbortSignal
    return new Promise<Response>((_resolve, reject) => {
      requestSignal!.addEventListener('abort', () => reject(requestSignal!.reason), { once: true })
    })
  })
  const pending = api('/api/admin/orders', undefined, controller.signal)
  controller.abort()
  await assert.rejects(pending, (e: unknown) => e instanceof DOMException && e.name === 'AbortError')
  assert.equal(requestSignal?.aborted, true)
})
