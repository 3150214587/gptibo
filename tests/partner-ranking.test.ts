import { seedAdminEmailCode } from './admin-email-fixture.ts'
import test from 'node:test'
import assert from 'node:assert/strict'
import { DatabaseSync } from 'node:sqlite'
import { readFileSync, readdirSync } from 'node:fs'
import { partnerRanking } from '../worker/partner-ranking.ts'
import worker from '../worker/index.ts'
import { totpCode } from '../worker/totp.ts'
import type { Env, Statement, Value } from '../worker/platform.ts'

const NOW = Date.parse('2026-09-30T04:00:00Z')
const TODAY = Date.parse('2026-09-29T16:00:00Z')
const DAY = 86400000
const credential = 'GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ'
const url = (days = 1, page = 1) => new URL(`https://shop.example.invalid/api/admin/partners/ranking?days=${days}&page=${page}`)

function setup() {
  const db = new DatabaseSync(':memory:')
  const migrations = new URL('../migrations/', import.meta.url)
  for (const file of readdirSync(migrations).filter(file => file.endsWith('.sql')).sort()) db.exec(readFileSync(new URL(file, migrations), 'utf8'))
  const bound = new WeakMap<Statement, { sql: string; values: Value[] }>()
  const env: Env = {
    ASSETS: { fetch: async () => new Response('asset') }, ADMIN_EMAIL: 'admin@example.invalid', ADMIN_USERNAME: 'tester', ADMIN_TOTP_SECRET: credential,
    INTEREST_LIMITER: { limit: async () => ({ success: true }) },
    API_LIMITER: { limit: async () => ({ success: true }) },
    EXPENSIVE_LIMITER: { limit: async () => ({ success: true }) },
    INTEREST_DB: {
      prepare(sql: string): Statement {
        const state = { sql, values: [] as Value[] }
        const statement: Statement = {
          bind(...next) { state.values = next; return this },
          async first<T>() { return (db.prepare(sql).get(...state.values) ?? null) as T | null },
          async all<T>() { return { results: db.prepare(sql).all(...state.values) as T[] } },
          async run() { return db.prepare(sql).run(...state.values) },
        }
        bound.set(statement, state)
        return statement
      },
      async batch(statements) {
        db.exec('BEGIN')
        try {
          const results = statements.map(statement => { const state = bound.get(statement)!; return db.prepare(state.sql).run(...state.values) })
          db.exec('COMMIT'); return results
        } catch (error) { db.exec('ROLLBACK'); throw error }
      },
    },
  }
  const partner = (id: string, active = 1, created = 0) => db.prepare('INSERT INTO partners(id,slug,name,email,active,created_at,updated_at) VALUES (?,?,?,?,?,?,?)').run(id, id, `代理 ${id}`, `${id}@example.test`, active, created, created)
  const order = (partnerId: string | null, created = TODAY, patch: { status?: string; payment?: string; mode?: string; refund?: number; isTest?: number } = {}) => {
    const id = crypto.randomUUID()
    db.prepare(`INSERT INTO orders(id,owner_hash,idempotency_key,plan,plan_name,price_cents,mode,payment_status,status,created_at,updated_at,last_event_id,partner_id,refunded_cents,is_test)
      VALUES (?,?,?,'plus','Plus',13900,?,?,?,?,?,?,?,?,?)`).run(id, 'test-owner', id, patch.mode ?? 'live', patch.payment ?? 'paid', patch.status ?? 'success', created, created, id, partnerId, patch.refund ?? 0, patch.isTest ?? 0)
  }
  return { db, env, partner, order }
}

test('ranking counts only paid, fulfilled real partner sales and reads the login email and WeChat', async t => {
  t.mock.method(Date, 'now', () => NOW)
  const s = setup(); t.after(() => s.db.close())
  s.partner('a'); s.partner('b', 0); s.partner('c')
  s.db.prepare("INSERT INTO support_contacts(owner,partner_id,wechat,email) VALUES ('a','a','agent_wechat','support@example.test')").run()
  s.order('a'); s.order('a', TODAY, { refund: 1000 }); s.order('b')
  s.order(null)
  s.order('a', TODAY, { payment: 'unpaid' })
  s.order('a', TODAY, { status: 'processing' })
  s.order('a', TODAY, { status: 'failed' })
  s.order('a', TODAY, { mode: 'demo', payment: 'simulated' })
  s.order('a', TODAY, { isTest: 1 })
  s.order('a', TODAY, { refund: 13900 })
  s.order('a', NOW + 1)
  const result = await partnerRanking(s.env, url())
  assert.deepEqual(result.rows.map(row => [row.rank, row.id, row.orderCount]), [[1, 'a', 2], [2, 'b', 1], [3, 'c', 0]])
  assert.equal(result.rows[0].email, 'a@example.test')
  assert.equal(result.rows[0].wechat, 'agent_wechat')
  assert.equal(result.rows[1].wechat, '')
  assert.equal(result.rows[1].active, 0)
  assert.deepEqual([result.total, result.orderCount, result.sellingPartners], [3, 3, 2])
})

test('today, seven and thirty days use inclusive Beijing calendar boundaries', async t => {
  t.mock.method(Date, 'now', () => NOW)
  const s = setup(); t.after(() => s.db.close()); s.partner('a')
  for (const created of [TODAY, TODAY - 1, TODAY - 6 * DAY, TODAY - 6 * DAY - 1, TODAY - 29 * DAY, TODAY - 29 * DAY - 1, NOW]) s.order('a', created)
  const today = await partnerRanking(s.env, url(1))
  const week = await partnerRanking(s.env, url(7))
  const month = await partnerRanking(s.env, url(30))
  assert.deepEqual([today.orderCount, week.orderCount, month.orderCount], [2, 4, 6])
  assert.deepEqual([today.since, week.since, month.since], [TODAY, TODAY - 6 * DAY, TODAY - 29 * DAY])
})

test('ranking pagination has stable order and global totals, handles empty data and validates input', async t => {
  t.mock.method(Date, 'now', () => NOW)
  const s = setup(); t.after(() => s.db.close())
  assert.deepEqual((await partnerRanking(s.env, url())).rows, [])
  for (let i = 24; i >= 0; i--) s.partner(`p${String(i).padStart(2, '0')}`)
  s.order('p24'); s.order('p24')
  const first = await partnerRanking(s.env, url(7))
  const second = await partnerRanking(s.env, url(7, 2))
  assert.equal(first.rows.length, 20); assert.equal(second.rows.length, 5)
  assert.equal(first.rows[0].id, 'p24'); assert.equal(first.rows[1].id, 'p00')
  assert.equal(second.rows[0].rank, 21); assert.equal(second.rows[0].id, 'p19')
  assert.equal(second.orderCount, 2); assert.equal(second.total, 25)
  assert.equal(new Set([...first.rows, ...second.rows].map(row => row.id)).size, 25)
  assert.equal((await partnerRanking(s.env, url(30, 3))).rows.length, 0)
  for (const days of [0, 2, 90]) await assert.rejects(partnerRanking(s.env, url(days)), { status: 400 })
  for (const page of [0, -1, 1.5, 10001, NaN]) await assert.rejects(partnerRanking(s.env, url(1, page)), { status: 400 })
})

test('ranking API requires an admin session and returns private contacts only to admins', async t => {
  const s = setup(); t.after(() => s.db.close()); s.partner('a')
  const request = (path: string, cookie = '') => worker.fetch(new Request('https://shop.example.invalid' + path, { headers: { Cookie: cookie } }), s.env)
  assert.equal((await request(url().pathname)).status, 401)
  const buyer = await worker.fetch(new Request('https://shop.example.invalid/api/orders/client', { method: 'POST', headers: { Origin: 'https://shop.example.invalid', 'Content-Type': 'application/json' }, body: '{}' }), s.env)
  assert.equal((await request(url().pathname, buyer.headers.get('Set-Cookie')!.split(';')[0])).status, 401)
  const login = await worker.fetch(new Request('https://shop.example.invalid/api/admin/login', { method: 'POST', headers: { Origin: 'https://shop.example.invalid', 'Content-Type': 'application/json' }, body: JSON.stringify({ username: 'tester', code: await totpCode(credential), ...await seedAdminEmailCode(s.db, s.env) }) }), s.env)
  assert.equal(login.status, 200)
  const cookie = login.headers.get('Set-Cookie')!.split(';')[0]
  const response = await request(url().pathname, cookie)
  assert.equal(response.status, 200)
  assert.equal(response.headers.get('Cache-Control'), 'no-store')
  assert.equal((await response.json()).rows[0].email, 'a@example.test')
  assert.equal((await request(url().pathname + '?days=90', cookie)).status, 400)
})
