import orderEmailDomains from '../../services/storefront/internal/orders/email-domains.json' with { type: 'json' }

// Match the server's address requirements without browser-language-dependent bubbles.
export function emailProblem(value: string): string {
  const email = value.trim()
  if (!email) return '请填写用于找回订单的邮箱。'
  if (email.length > 254 || !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) return '请填写完整的邮箱地址，例如 you@example.com。'
  return ''
}

export function orderEmailProblem(value: string): string {
  const problem = emailProblem(value)
  if (problem) return problem
  const domain = value.trim().toLowerCase().split('@')[1]
  return orderEmailDomains.includes(domain) ? '' : '下单仅支持常用邮箱，请使用 QQ、Gmail、网易、Outlook 或 iCloud 等邮箱。'
}

const commonEmailDomains = ['gmail.com', 'qq.com', 'outlook.com', '163.com', 'icloud.com']

export function emailSuggestions(value: string): string[] {
  const email = value.trim()
  if (!email || /\s/.test(email)) return []
  const [local, domain = '', extra] = email.split('@')
  if (!local || extra !== undefined) return []
  return commonEmailDomains
    .filter(candidate => candidate.startsWith(domain.toLowerCase()) && candidate !== domain.toLowerCase())
    .map(candidate => `${local}@${candidate}`)
    .filter(candidate => candidate.length <= 254)
}
