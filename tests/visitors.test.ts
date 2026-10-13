import test from 'node:test'
import assert from 'node:assert/strict'
import { DatabaseSync } from 'node:sqlite'
import { readFileSync } from 'node:fs'
import { adminVisitorSummary, recordVisitor, visitorSummary } from '../worker/visitors.ts'
import type { Env, Statement, Value } from '../worker/platform.ts'

function setup() {
  const db = new DatabaseSync(':memory:')
  db.exec('CREATE TABLE partners(id TEXT PRIMARY KEY,slug TEXT,name TEXT,active INTEGER,created_at INTEGER)')
  db.exec(readFileSync(new URL('../migrations/0015_visitor_analytics.sql', import.meta.url), 'utf8'))
  db.prepare('INSERT INTO partners VALUES (?,?,?,?,?)').run('partner-a', 'ABCDEF', '代理 A', 1, 1)
  db.prepare('INSERT INTO partners VALUES (?,?,?,?,?)').run('partner-b', 'GHIJKL', '代理 B', 0, 2)
  const database = {
    prepare(sql: string): Statement {
      let values: Value[] = []
      return {
        bind(...next) { values = next; return this },
        async first<T>() { return (db.prepare(sql).get(...values) ?? null) as T | null },
        async all<T>() { return { results: db.prepare(sql).all(...values) as T[] } },
        async run() { return db.prepare(sql).run(...values) },
      }
    },
    async batch() { return [] },
  }
  return { db, env: { INTEREST_DB: database } as Env }
}

test('storefront visits are scoped by partner and deduplicated across pages and shops', async t => {
  const { db, env } = setup(); t.after(() => db.close())
  const visitorId = crypto.randomUUID()
  const send = (path: string, id = visitorId, origin = 'https://shop.example.invalid') => recordVisitor(new Request('https://shop.example.invalid/api/visitors', {
    method: 'POST', headers: { Origin: origin, 'Content-Type': 'application/json' }, body: JSON.stringify({ path, visitorId: id }),
  }), env)
  await send('/')
  await send('/product/chatgpt')
  await send('/r/ABCDEF')
  await send('/r/ABCDEF/product/chatgpt')
  await send('/r/ABCDEF/orders', crypto.randomUUID())
  const platform = await visitorSummary(env, new URL('https://shop.example.invalid/?days=7'), 'platform')
  const partner = await visitorSummary(env, new URL('https://shop.example.invalid/?days=7'), 'partner-a')
  const overall = await adminVisitorSummary(env, new URL('https://shop.example.invalid/?days=7'))
  assert.deepEqual([platform.visitors, platform.pageViews], [1, 2])
  assert.deepEqual([partner.visitors, partner.pageViews], [2, 3])
  assert.deepEqual([overall.visitors, overall.pageViews], [2, 5])
  assert.deepEqual([overall.partners[0].id, overall.partners[0].visitors], ['partner-a', 2])
  assert.deepEqual([overall.partners[1].id, overall.partners[1].visitors], ['partner-b', 0])
  assert.equal(overall.trend.length, 7)
  await assert.rejects(send('/r/GHIJKL'), { status: 404 })
  await assert.rejects(send('/admin'), { status: 400 })
  await assert.rejects(send('/', visitorId, 'https://evil.test'), { status: 403 })
  assert.equal(db.prepare('SELECT COUNT(*) n FROM visitor_days').get()!.n, 3)
})

test('90-day range includes older visits and keeps 30-day results separate', async t => {
  const { db, env } = setup(); t.after(() => db.close())
  const oldDay = new Date(Date.now() - 75 * 86400000 + 8 * 3600000).toISOString().slice(0, 10)
  db.prepare('INSERT INTO visitor_days(day,shop_id,visitor_hash,page_views) VALUES (?,?,?,?)').run(oldDay, 'partner-a', 'old-visitor', 3)
  const url = (days: number) => new URL(`https://shop.example.invalid/api/admin/visitors?days=${days}`)
  assert.deepEqual([ (await visitorSummary(env, url(30), 'partner-a')).pageViews, (await visitorSummary(env, url(90), 'partner-a')).pageViews ], [0, 3])
  const admin = await adminVisitorSummary(env, url(90))
  assert.equal(admin.trend.length, 90)
  assert.equal(admin.partners[0].pageViews, 3)
  await assert.rejects(visitorSummary(env, url(91)), { status: 400 })
})
