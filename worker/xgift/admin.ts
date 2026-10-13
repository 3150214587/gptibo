import { ApiError, json, type Env } from '../platform.ts'
import {
  configured,
  xgiftBalance,
  xgiftProducts,
  queryXGiftOrder,
  xgiftBackend,
} from './client.ts'
export async function xgiftAdmin(request: Request, env: Env, url: URL) {
  if (request.method !== 'GET')
    throw new ApiError('此管理接口只支持查询。', 405)
  if (url.pathname === '/api/admin/xgift/status') {
    if (!configured(env)) return json({ configured: false })
    const [balance, products] = await Promise.all([
      xgiftBalance(env),
      xgiftProducts(env),
    ])
    return json({
      configured: true,
      balance,
      products,
      consoleUrl: xgiftBackend(env) === 'platform' ? 'https://api.shop.example.invalid' : 'https://x-api.shop.example.invalid',
    })
  }
  if (url.pathname === '/api/admin/xgift/order') {
    const merchant = url.searchParams.get('merchant_order_no') ?? ''
    if (!/^[A-Za-z0-9_.:-]{1,128}$/.test(merchant))
      throw new ApiError('商户订单号无效。')
    const purchase = await env.INTEREST_DB.prepare('SELECT supplier_backend,supplier_merchant_order_no FROM xgift_purchases WHERE order_id=?').bind(merchant).first<{ supplier_backend: 'legacy' | 'platform'; supplier_merchant_order_no: string | null }>()
    return json(await queryXGiftOrder(env, purchase?.supplier_merchant_order_no ?? merchant, purchase?.supplier_backend ?? xgiftBackend(env)))
  }
  throw new ApiError('未找到 X 充值接口。', 404)
}
