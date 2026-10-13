import test from 'node:test'
import assert from 'node:assert/strict'
import { restartAdminAccess } from '../src/lib/admin-access.ts'

test('reauthentication clears Access before navigating to an uncached admin entry', async t => {
  const calls: string[] = []
  const descriptor = Object.getOwnPropertyDescriptor(globalThis, 'window')
  Object.defineProperty(globalThis, 'window', { configurable: true, value: { location: { replace(url: string) { calls.push(url) } } } })
  t.after(() => { if (descriptor) Object.defineProperty(globalThis, 'window', descriptor); else Reflect.deleteProperty(globalThis, 'window') })
  let release!: (value: Response) => void
  t.mock.method(globalThis, 'fetch', async (url: string, options: RequestInit) => {
    assert.equal(url, '/cdn-cgi/access/logout')
    assert.equal(options.credentials, 'same-origin')
    assert.equal(options.cache, 'no-store')
    assert.equal(options.redirect, 'manual')
    assert.ok(options.signal)
    calls.push('logout')
    return new Promise<Response>(resolve => { release = resolve })
  })
  const pending = restartAdminAccess()
  assert.deepEqual(calls, ['logout'])
  release(new Response('logged out'))
  await pending
  assert.match(calls[1], /^\/admin\?access_refresh=\d+$/)
})

test('logout failures do not silently reload the same broken session', async t => {
  let navigations = 0
  const descriptor = Object.getOwnPropertyDescriptor(globalThis, 'window')
  Object.defineProperty(globalThis, 'window', { configurable: true, value: { location: { replace() { navigations++ } } } })
  t.after(() => { if (descriptor) Object.defineProperty(globalThis, 'window', descriptor); else Reflect.deleteProperty(globalThis, 'window') })
  for (const failure of [new Response('unavailable', {status:503}), new Error('network failure')]) {
    t.mock.method(globalThis, 'fetch', async () => {
      if (failure instanceof Error) throw failure
      return failure
    })
    await assert.rejects(restartAdminAccess(), /Access logout failed|network failure/)
    assert.equal(navigations, 0)
    t.mock.restoreAll()
  }
})
