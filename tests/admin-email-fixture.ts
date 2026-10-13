import type { DatabaseSync } from 'node:sqlite'
import { hash, type Env } from '../worker/platform.ts'
import { adminLoginEmail } from '../worker/admin-auth.ts'

// Seed a delivered email challenge in isolated test databases. Dedicated auth
// tests exercise sending and delivery; other business tests still use the login API.
export async function seedAdminEmailCode(db: DatabaseSync, env: Env, emailCode = '001234') {
  const challenge = crypto.randomUUID()
  const version = await hash('admin-email-totp-v1:' + adminLoginEmail(env) + ':' + env.ADMIN_USERNAME + ':' + env.ADMIN_TOTP_SECRET)
  const digest = await hash('admin-email:' + env.ADMIN_TOTP_SECRET + ':' + version + ':' + challenge + ':' + emailCode)
  db.prepare('INSERT INTO admin_email_login_codes(id,code_hash,credential_version,expires_at,ready) VALUES (?,?,?,?,1)').run(challenge, digest, version, Date.now() + 600000)
  return { challenge, emailCode }
}
