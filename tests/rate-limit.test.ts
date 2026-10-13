import assert from 'node:assert/strict'
import test from 'node:test'
import worker from '../worker/index.ts'
import { limitApi } from '../worker/rate-limit.ts'
import type { Env } from '../worker/platform.ts'

test('API limit rejects before catalog can reach D1 or supplier', async () => {
  const env = {
    API_LIMITER: { limit: async () => ({ success: false }) },
    EXPENSIVE_LIMITER: { limit: async () => { throw new Error('unexpected second limiter') } },
    INTEREST_DB: { prepare: () => { throw new Error('unexpected D1 access') } },
  } as unknown as Env
  const response = await worker.fetch(new Request('https://shop.example.invalid/api/catalog', {
    headers: { 'CF-Connecting-IP': '203.0.113.10' },
  }), env)
  assert.equal(response.status, 429)
  assert.equal(response.headers.get('Retry-After'), '60')
  assert.equal(response.headers.get('Cache-Control'), 'no-store')
})

test('costly reads have a separate budget, while signed callbacks use their own source bucket', async () => {
  const keys: string[] = []
  const env = {
    API_LIMITER: { limit: async ({ key }: { key: string }) => { keys.push(key); return { success: true } } },
    EXPENSIVE_LIMITER: { limit: async () => ({ success: false }) },
  } as unknown as Env
  const headers = { 'CF-Connecting-IP': '203.0.113.10' }
  const catalog = await limitApi(new Request('https://shop.example.invalid/api/catalog', { headers }), env, '/api/catalog')
  const callback = await limitApi(new Request('https://shop.example.invalid/api/recharge/webhook', { method: 'POST', headers }), env, '/api/recharge/webhook')
  assert.equal(catalog?.status, 429)
  const queryPath = '/api/admin/orders/YMX-TEST/platform-status'
  assert.equal((await limitApi(new Request('https://shop.example.invalid' + queryPath, { method: 'POST', headers }), env, queryPath))?.status, 429)
  assert.equal(callback, null)
  assert.equal(keys.length, 3)
  assert.notEqual(keys[0], keys[1])
})
