import test from 'node:test'
import assert from 'node:assert/strict'
import { canonicalStorefrontUrl, readStorefrontPage, storefrontHref, storefrontShopSlug, storefrontScopeHeaders } from '../src/lib/storefront-routing.ts'
import { partnerStorefrontPath } from '../shared/partner-link.ts'

const current = (pathname: string, hash = '', search = '') => ({ pathname, hash, search })

test('direct product and order paths work on reload, including product section anchors', () => {
  assert.equal(readStorefrontPage(current('/')), 'store')
  assert.equal(readStorefrontPage(current('/product/chatgpt')), 'product/chatgpt')
  assert.equal(readStorefrontPage(current('/product/codex-sms/', '#help')), 'product/codex-sms')
  assert.equal(readStorefrontPage(current('/orders')), 'orders')
  assert.equal(readStorefrontPage(current('/product/missing')), 'product/missing')
  assert.equal(canonicalStorefrontUrl(current('/product/chatgpt', '#help')), null)
})

test('legacy bookmarks map to clean routes without losing query parameters', () => {
  assert.equal(canonicalStorefrontUrl(current('/', '#product/chatgpt', '?source=referral')), '/product/chatgpt?source=referral')
  assert.equal(canonicalStorefrontUrl(current('/', '#orders')), '/orders')
  assert.equal(canonicalStorefrontUrl(current('/product/chatgpt', '#products')), '/')
  assert.equal(canonicalStorefrontUrl(current('/', '#product')), '/')
  assert.equal(canonicalStorefrontUrl(current('/', '#help')), '/product/chatgpt#help')
  assert.equal(canonicalStorefrontUrl(current('/', '#catalog-products')), null)
})

test('partner deep links preserve the shop and never treat a product path as the partner slug', () => {
  const location = current('/s/my-shop/product/chatgpt', '', '?source=friend')
  assert.equal(storefrontShopSlug(location.pathname), 'my-shop')
  assert.equal(storefrontShopSlug('/s/my-shop/orders'), 'my-shop')
  assert.equal(storefrontShopSlug('/product/chatgpt'), undefined)
  assert.equal(storefrontShopSlug('/s/'), '!')
  assert.equal(readStorefrontPage(location), 'product/chatgpt')
  assert.equal(readStorefrontPage(current('/s/my-shop/orders')), 'orders')
  assert.equal(storefrontHref('store', location), '/s/my-shop?source=friend')
  assert.equal(storefrontHref('product/codex-sms', location), '/s/my-shop/product/codex-sms?source=friend')
  assert.equal(storefrontHref('orders', location), '/s/my-shop/orders?source=friend')
  assert.equal(canonicalStorefrontUrl(current('/s/my-shop/', '#product/chatgpt', '?source=friend')), '/s/my-shop/product/chatgpt?source=friend')
})

test('six-character alphanumeric and existing letter referral links preserve attribution through products, orders and reloads', () => {
  for (const code of ['ABCDEF', 'ABCDEFGH', 'A3B7C9', '012345']) {
    const base = '/r/' + code
    const location = current(base + '/product/chatgpt', '', '?source=friend')
    assert.equal(partnerStorefrontPath(code), base)
    assert.equal(partnerStorefrontPath('my-shop'), '/s/my-shop')
    assert.equal(storefrontShopSlug(location.pathname), code)
    assert.equal(readStorefrontPage(location), 'product/chatgpt')
    assert.equal(readStorefrontPage(current(base + '/orders')), 'orders')
    assert.equal(storefrontHref('store', location), base + '?source=friend')
    assert.equal(storefrontHref('product/codex-sms', location), base + '/product/codex-sms?source=friend')
    assert.equal(storefrontHref('orders', location), base + '/orders?source=friend')
    assert.equal(canonicalStorefrontUrl(current(base + '/', '#product/chatgpt')), base + '/product/chatgpt')
  }
  for (const path of ['/r/', '/r/abcdef', '/r/abcdefgh', '/r/ABC12345', '/r/ABCDE', '/r/ABCDEFG', '/r/ABCDEFGHI']) assert.equal(storefrontShopSlug(path), '!')
})

test('buyer API selection stays with the referral shop through product and order navigation', () => {
 for(const path of ['/r/ABC123','/r/ABC123/orders','/r/ABC123/product/chatgpt']) assert.deepEqual(storefrontScopeHeaders(current(path)), {'X-Storefront-Shop':'ABC123'})
 assert.deepEqual(storefrontScopeHeaders(current('/s/legacy-shop/orders')), {'X-Storefront-Shop':'legacy-shop'})
 for(const path of ['/','/orders','/join']) assert.deepEqual(storefrontScopeHeaders(current(path)), {})
 assert.deepEqual(storefrontScopeHeaders(current('/r/bad')), {'X-Storefront-Shop':'!'})
})
