import { ApiError, body, json, type Env } from './platform.ts'

// Audit the exact payout set in the same transaction as its exemption. A new
// payout after this transaction keeps the default cooldown, including on retry.
export async function resetPayoutCooldown(request: Request, env: Env, partnerId?: string) {
  if (request.method !== 'POST') throw new ApiError('请通过重置按钮操作。', 405)
  const data = await body(request)
  if (typeof data.key !== 'string' || !/^[a-f0-9-]{36}$/.test(data.key)) throw new ApiError('请求编号无效。')
  if (typeof data.note !== 'string' || !data.note.trim() || data.note.trim().length > 500) throw new ApiError('请填写有效的重置说明。')
  const db = env.INTEREST_DB
  if (partnerId && !await db.prepare('SELECT id FROM partners WHERE id=?').bind(partnerId).first()) throw new ApiError('代理不存在。', 404)
  const key = data.key, target = partnerId ?? 'all-partners'
  const action = partnerId ? 'payout-cooldown-reset-partner' : 'payout-cooldown-reset-all'
  const existing = await db.prepare('SELECT action,target_id FROM admin_audit WHERE id=?').bind(key).first<{ action: string; target_id: string }>()
  if (existing && (existing.action !== action || existing.target_id !== target)) throw new ApiError('请求编号已用于其他操作，请重新打开重置窗口。', 409)
  if (!existing) {
    const now = Date.now()
    // A per-call claim also serializes two concurrent calls using the same key.
    const note = `${env.ADMIN_USERNAME}：${data.note.trim()}；执行编号 ${crypto.randomUUID()}`
    const scope = partnerId ? ' AND p.partner_id=?' : ''
    await db.batch([
      db.prepare('INSERT INTO admin_audit(id,target_id,action,note,created_at) VALUES (?,?,?,?,?) ON CONFLICT(id) DO NOTHING').bind(key, target, action, note, now),
      db.prepare(`INSERT INTO admin_audit(id,target_id,action,note,created_at)
        SELECT ? || ':' || p.id,p.partner_id,'payout-cooldown-reset',? || '；提现单 ' || p.id,?
        FROM partner_payouts p WHERE p.cooldown_exempt=0${scope}
        AND EXISTS(SELECT 1 FROM admin_audit a WHERE a.id=? AND a.action=? AND a.target_id=? AND a.note=?)`)
        .bind(key, note, now, ...(partnerId ? [partnerId] : []), key, action, target, note),
      db.prepare(`UPDATE partner_payouts SET cooldown_exempt=1 WHERE cooldown_exempt=0
        AND EXISTS(SELECT 1 FROM admin_audit a WHERE a.id=? || ':' || partner_payouts.id AND a.action='payout-cooldown-reset')
        AND EXISTS(SELECT 1 FROM admin_audit a WHERE a.id=? AND a.action=? AND a.target_id=? AND a.note=?)`)
        .bind(key, key, action, target, note),
    ])
  }
  const claimed = await db.prepare('SELECT action,target_id FROM admin_audit WHERE id=?').bind(key).first<{ action: string; target_id: string }>()
  if (!claimed || claimed.action !== action || claimed.target_id !== target) throw new ApiError('请求编号已用于其他操作，请重新打开重置窗口。', 409)
  const counts = await db.prepare("SELECT COUNT(*) resetPayouts,COUNT(DISTINCT target_id) resetPartners FROM admin_audit WHERE id LIKE ? AND action='payout-cooldown-reset'").bind(key + ':%').first<{ resetPayouts: number; resetPartners: number }>()
  return json({ ok: true, resetPayouts: counts?.resetPayouts ?? 0, resetPartners: counts?.resetPartners ?? 0 })
}
