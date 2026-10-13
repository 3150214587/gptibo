export const referralCodeLength = 6
export const referralCodeAlphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789'
// Keep existing six- and eight-letter links valid.
export const referralCodePattern = /^(?:[A-Z0-9]{6}|[A-Z]{8})$/

export function generateReferralCode() {
  let code = ''
  const limit = 256 - 256 % referralCodeAlphabet.length
  while (code.length < referralCodeLength) {
    for (const byte of crypto.getRandomValues(new Uint8Array(16))) {
      if (byte < limit) code += referralCodeAlphabet[byte % referralCodeAlphabet.length]
      if (code.length === referralCodeLength) return code
    }
  }
  return code
}

export function partnerStorefrontPath(slug: string) {
  return (referralCodePattern.test(slug) ? '/r/' : '/s/') + slug
}
