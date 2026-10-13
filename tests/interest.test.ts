import test from 'node:test'
import assert from 'node:assert/strict'
import { DatabaseSync } from 'node:sqlite'
import { readFileSync } from 'node:fs'
import worker from '../worker/index.ts'

function setup() {
  const database = new DatabaseSync(':memory:')
  database.exec(readFileSync(new URL('../migrations/0001_launch_interest.sql', import.meta.url), 'utf8'))
  const migrateGmail = () => database.exec(readFileSync(new URL('../migrations/0027_gmail_launch_interest.sql', import.meta.url), 'utf8'))
  const env = {
    ASSETS: { fetch: async () => new Response('asset') },
    INTEREST_LIMITER: { limit: async () => ({ success: true }) },
    API_LIMITER: { limit: async () => ({ success: true }) },
    EXPENSIVE_LIMITER: { limit: async () => ({ success: true }) },
    INTEREST_DB: { prepare(sql: string) {
      let values: string[] = []
      return { bind(...args: string[]) { values = args; return this }, async first<T>() { return database.prepare(sql).get(...values) as T ?? null }, async run() { return database.prepare(sql).run(...values) } }
    } },
  }
  const request = (body: unknown, path = '/api/interest', headers = {}) => worker.fetch(new Request(`https://shop.example.invalid${path}`, { method: 'POST', headers: { Origin: 'https://shop.example.invalid', 'Content-Type': 'application/json', ...headers }, body: JSON.stringify(body) }), env)
  const count = () => Number(database.prepare('SELECT COUNT(*) AS n FROM launch_interest').get()!.n)
  const token = 'a'.repeat(64)
  const valid = { email: 'test@example.com', product: 'claude', token, consent: true }
  return { database, env, request, count, token, valid, migrateGmail }
}

test('registration persists consent, normalizes email and retries idempotently without rotating ownership', async t => {
  const s = setup(); t.after(() => s.database.close())
  const response = await s.request({ ...s.valid, email: ' TEST@EXAMPLE.COM ' })
  assert.equal(response.status, 200)
  assert.deepEqual(await response.json(), { ok: true, manageable: true, deliveryEnabled: false })
  assert.equal(response.headers.get('cache-control'), 'no-store')
  assert.equal(s.database.prepare('SELECT email FROM launch_interest').get()!.email, 'test@example.com')
  assert.equal(s.database.prepare('SELECT consent_version FROM launch_interest').get()!.consent_version, 'launch-interest-v1')
  assert.notEqual(s.database.prepare('SELECT token_hash FROM launch_interest').get()!.token_hash, s.token)
  assert.equal((await (await s.request(s.valid)).json()).manageable, true)
  assert.equal((await (await s.request({ ...s.valid, token: 'b'.repeat(64) })).json()).manageable, false)
  assert.equal(s.count(), 1)
  await s.request({ token: 'b'.repeat(64) }, '/api/interest/cancel')
  assert.equal(s.count(), 1, 'a duplicate request cannot cancel someone else’s registration')
  await s.request({ token: s.token }, '/api/interest/cancel')
  assert.equal(s.count(), 0)
  assert.equal((await s.request({ token: s.token }, '/api/interest/cancel')).status, 200)
})

test('registrations are independent across products and reject invalid data before writing', async t => {
  const s = setup(); t.after(() => s.database.close())
  for (const change of [{ consent: false }, { email: 'bad' }, { product: 'chatgpt' }, { token: 'short' }]) assert.equal((await s.request({ ...s.valid, ...change })).status, 400)
  assert.equal((await s.request(null)).status, 400)
  assert.equal((await s.request({ ...s.valid, padding: 'x'.repeat(2050) })).status, 413)
  assert.equal(s.count(), 0)
  await s.request(s.valid)
  await s.request({ ...s.valid, product: 'grok', token: 'b'.repeat(64) })
  assert.equal(s.count(), 2)
})

test('Gmail migration preserves old registrations; Gmail supports consent, deduplication and independent cancellation', async t => {
  const s = setup(); t.after(() => s.database.close())
  await s.request(s.valid)
  const existing = s.database.prepare('SELECT * FROM launch_interest').get()
  s.migrateGmail()
  assert.deepEqual(s.database.prepare('SELECT * FROM launch_interest').get(), existing)
  const gmail = { ...s.valid, product: 'gmail', token: 'c'.repeat(64) }
  assert.equal((await s.request({ ...gmail, consent: false })).status, 400)
  assert.equal((await (await s.request(gmail)).json()).manageable, true)
  assert.equal((await (await s.request(gmail)).json()).manageable, true)
  assert.equal((await (await s.request({ ...gmail, token: 'd'.repeat(64) })).json()).manageable, false)
  assert.equal(s.count(), 2)
  await s.request({ token: gmail.token }, '/api/interest/cancel')
  assert.equal(s.count(), 1)
  assert.deepEqual(s.database.prepare('SELECT * FROM launch_interest').get(), existing)
  await s.request({ token: s.token }, '/api/interest/cancel')
  assert.equal(s.count(), 0)
})

test('API rejects cross-site requests, unsupported formats and rate limiting without exposing records', async t => {
  const s = setup(); t.after(() => s.database.close())
  assert.equal((await s.request(s.valid, '/api/interest', { Origin: 'https://another.example' })).status, 403)
  assert.equal((await s.request(s.valid, '/api/interest', { 'Content-Type': 'text/plain' })).status, 415)
  assert.equal((await s.env.ASSETS.fetch()).status, 200)
  assert.equal((await worker.fetch(new Request('https://shop.example.invalid/api/interest'), s.env)).status, 405)
  assert.equal((await s.request(s.valid, '/api/unknown')).status, 404)
  s.env.INTEREST_LIMITER.limit = async () => ({ success: false })
  const response = await s.request(s.valid)
  assert.equal(response.status, 429)
  assert.equal(response.headers.get('retry-after'), '60')
  assert.equal(s.count(), 0)
})

test('database failures never produce a successful registration or disclose the error', async t => {
  const s = setup(); t.after(() => s.database.close())
  s.env.INTEREST_DB.prepare = () => { throw new Error('private database details') }
  const response = await s.request(s.valid)
  assert.equal(response.status, 503)
  assert.equal((await response.text()).includes('private database'), false)
})
