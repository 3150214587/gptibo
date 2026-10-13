// RFC 4226 / RFC 6238: SHA-1, six digits, 30-second time steps.
const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'
export function generateTotpSecret() {
  return Array.from(crypto.getRandomValues(new Uint8Array(32)), byte => alphabet[byte & 31]).join('')
}
export function validTotpSecret(secret: string | undefined): secret is string {
  return typeof secret === 'string' && /^[A-Z2-7]{32}$/.test(secret)
}
export async function totpCode(secret: string, step = Math.floor(Date.now() / 30000)) {
  if (!validTotpSecret(secret) || !Number.isSafeInteger(step) || step < 0) throw new Error('Invalid TOTP parameters')
  let bits = 0; let value = 0; const bytes: number[] = []
  for (const char of secret) {
    value = (value << 5) | alphabet.indexOf(char); bits += 5
    if (bits >= 8) { bits -= 8; bytes.push((value >>> bits) & 255) }
  }
  const counter = new ArrayBuffer(8)
  new DataView(counter).setBigUint64(0, BigInt(step))
  const key = await crypto.subtle.importKey('raw', new Uint8Array(bytes), { name: 'HMAC', hash: 'SHA-1' }, false, ['sign'])
  const digest = new Uint8Array(await crypto.subtle.sign('HMAC', key, counter))
  const offset = digest[19] & 15
  const number = new DataView(digest.buffer).getUint32(offset) & 0x7fffffff
  return String(number % 1000000).padStart(6, '0')
}
export async function verifyTotp(secret: string, code: unknown, now = Date.now()) {
  if (typeof code !== 'string' || !/^\d{6}$/.test(code)) return null
  const current = Math.floor(now / 30000)
  let matched: number | null = null
  for (const step of [current - 1, current, current + 1]) {
    if (step < 0) continue
    const expected = await totpCode(secret, step)
    let difference = 0
    for (let i = 0; i < 6; i++) difference |= expected.charCodeAt(i) ^ code.charCodeAt(i)
    if (difference === 0) matched = step
  }
  return matched
}
