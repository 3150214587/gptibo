/** Recognize a whole HTTPS claim link, including legacy deliveries stored as CDKs. */
export function membershipClaimUrl(value: string | undefined): string | null {
  if (!value || value.length > 2048) return null
  const link = value.trim()
  if (!/^https:\/\//i.test(link) || /[\s<>"']/.test(link) || Array.from(link).some(char => char.charCodeAt(0) < 32 || char.charCodeAt(0) === 127)) return null
  try {
    const url = new URL(link)
    return url.protocol === 'https:' && !url.username && !url.password && url.hostname.includes('.') ? link : null
  } catch { return null }
}
