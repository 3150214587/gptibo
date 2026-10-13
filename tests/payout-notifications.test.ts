import test from 'node:test'
import assert from 'node:assert/strict'
import { DatabaseSync } from 'node:sqlite'
import { readFileSync } from 'node:fs'
import { notifyPayout, payoutMessage, retryPayoutNotifications } from '../worker/payout-notifications.ts'
import type { Env, Statement, Value } from '../worker/platform.ts'

function setup() {
  const db = new DatabaseSync(':memory:')
  for (const file of ['0001_launch_interest.sql','0002_commerce.sql','0003_admin_totp.sql','0004_partners.sql','0005_payout_items.sql','0006_after_sales.sql','0016_payout_notifications.sql'])
    db.exec(readFileSync(new URL('../migrations/' + file, import.meta.url), 'utf8'))
  db.prepare('INSERT INTO partners(id,slug,name,email,active,created_at,updated_at) VALUES (?,?,?,?,1,?,?)').run('p1','ABCDEF','代理 A','a@example.com',1,1)
  db.prepare("INSERT INTO partner_payouts(id,partner_id,amount_cents,gross_cents,deduction_cents,recipient,created_at,request_key) VALUES ('pay1','p1',5850,7800,1950,'支付宝 / 张三',?, 'k1')").run(Date.now())
  db.prepare('INSERT INTO payout_notifications(payout_id,created_at) VALUES (?,?)').run('pay1', Date.now())
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
  const env = { INTEREST_DB: database, FEISHU_APP_ID: 'cli_test', FEISHU_APP_SECRET: 'secret', FEISHU_PAYOUT_RECIPIENT: 'ou_abc123' } as Env
  return { db, env }
}

function stubFeishu(sent: unknown[]) {
  const original = globalThis.fetch
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const body = JSON.parse(String(init?.body ?? '{}'))
    if (String(input).includes('/auth/v3/tenant_access_token/internal')) return Response.json({ code: 0, tenant_access_token: 't-1' })
    sent.push(body)
    return Response.json({ code: 0, data: { message_id: 'om_1' } })
  }) as typeof fetch
  return () => { globalThis.fetch = original }
}

test('payout notification sends amount, deduction and recipient to the shared bot', async t => {
  const { db, env } = setup(); t.after(() => db.close())
  const sent: any[] = []; const restore = stubFeishu(sent); t.after(restore)
  await retryPayoutNotifications(env)
  assert.equal(sent.length, 1)
  assert.equal(sent[0].receive_id, 'ou_abc123')
  const text = JSON.parse(sent[0].content).text
  assert.match(text, /【提现申请】/)
  assert.match(text, /代理 A（ABCDEF）/)
  assert.match(text, /¥58\.50/)
  assert.match(text, /抵扣售后退款 ¥19\.50/)
  assert.match(text, /支付宝 \/ 张三/)
  assert.equal(db.prepare('SELECT delivered_at,attempt_count FROM payout_notifications WHERE payout_id=?').get('pay1')!.attempt_count, 1)
  assert.ok(db.prepare('SELECT delivered_at FROM payout_notifications WHERE payout_id=?').get('pay1')!.delivered_at)
})

test('a delivered payout is never notified twice', async t => {
  const { db, env } = setup(); t.after(() => db.close())
  const sent: any[] = []; const restore = stubFeishu(sent); t.after(restore)
  await notifyPayout(env, 'pay1')
  await retryPayoutNotifications(env)
  await retryPayoutNotifications(env)
  assert.equal(sent.length, 1)
})

test('a failed send is retried later instead of being dropped', async t => {
  const { db, env } = setup(); t.after(() => db.close())
  const original = globalThis.fetch
  let calls = 0
  globalThis.fetch = (async () => { calls += 1; return new Response('nope', { status: 500 }) }) as typeof fetch
  t.after(() => { globalThis.fetch = original })
  await notifyPayout(env, 'pay1')
  const row = db.prepare('SELECT delivered_at,next_attempt_at,attempt_count FROM payout_notifications WHERE payout_id=?').get('pay1')!
  assert.equal(row.delivered_at, null)
  assert.ok(row.next_attempt_at > Date.now())
  assert.equal(row.attempt_count, 1)
  assert.ok(calls >= 1)
})

test('without bot credentials the queue stays untouched', async t => {
  const { db, env } = setup(); t.after(() => db.close())
  delete (env as Record<string, unknown>).FEISHU_APP_ID
  const result = await retryPayoutNotifications(env as Env)
  assert.deepEqual(result, { configured: false, sent: 0, failed: 0 })
  assert.equal(db.prepare('SELECT delivered_at FROM payout_notifications WHERE payout_id=?').get('pay1')!.delivered_at, null)
})

test('message text is plain and free of markup', () => {
  const text = payoutMessage({ payout_id: 'x', attempt_count: 0, amount_cents: 3000, gross_cents: 3000, deduction_cents: 0, recipient: '支付宝 / 李四', partner_name: '代理 B', partner_slug: 'GHIJKL', created_at: 1759161600000 })
  assert.equal(text.includes('<'), false)
  assert.match(text, /应结|金额/)
})
