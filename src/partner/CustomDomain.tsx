import { WorkspaceDisclosure } from './workspace-ui'
import { useEffect, useRef, useState } from 'react'
import { Globe, RefreshCw, ArrowUpRight, Check, ShieldCheck, LockKeyhole } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { CopyButton } from '@/components/copy-button'
import { api, errorMessage } from '@/lib/api'
import './custom-domain.css'

type DomainView = {
  enabled: boolean
  eligible: boolean
  completedOrders: number
  requiredOrders: number
  target: string
  domain: null | {
    hostname: string
    apex: boolean
    state: 'pending_dns' | 'pending_certificate' | 'active' | 'error' | 'deleting'
    message: string
    checkedAt: string | null
    records: { type: string; name: string; value: string }[]
  }
}
const labels = { pending_dns: '等待解析', pending_certificate: '证书签发中', active: '已启用', error: '需要检查', deleting: '正在解绑' }

export function CustomDomain({ prefix, readOnly = false, polling = true, onSummaryChange }: { prefix: string; readOnly?: boolean; polling?: boolean; onSummaryChange?: (summary: string) => void }) {
  const [view, setView] = useState<DomainView>()
  const [hostname, setHostname] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [confirm, setConfirm] = useState(false)
  const [revision, setRevision] = useState(0)
  const generation = useRef(0)
  const mutating = useRef(false)
  useEffect(() => {
    let cancelled = false
    const load = async () => {
      if (mutating.current) return
      const current = generation.current
      try { const next = await api<DomainView>(prefix + '/domain'); if (!cancelled && current === generation.current) { setView(next); setError('') } }
      catch (e) { if (!cancelled && current === generation.current) setError(errorMessage(e)) }
    }
    void load()
    const timer = polling ? window.setInterval(() => { if (!document.hidden) void load() }, 15000) : undefined
    return () => { cancelled = true; clearInterval(timer) }
  }, [prefix, revision, polling])
  const mutate = async (suffix: string, body: object) => {
    if (mutating.current || readOnly) return
    mutating.current = true; generation.current++
    setBusy(true); setError('')
    try { setView(await api<DomainView>(prefix + '/domain' + suffix, body)); setConfirm(false) }
    catch (e) { setError(errorMessage(e)) }
    finally { mutating.current = false; setBusy(false) }
  }
  useEffect(() => {
    onSummaryChange?.(error ? '读取失败，展开后重试' : !view ? '正在读取…' : view.domain ? `${view.domain.hostname} · ${labels[view.domain.state]}` : !view.enabled ? '暂未开放' : view.eligible ? '未绑定 · 使用自己的商店网址' : `完成 ${view.requiredOrders} 单后开放 · 当前 ${view.completedOrders} 单`)
  }, [view, error, onSummaryChange])
  const domain = view?.domain
  const active = domain?.state === 'active'
  const dnsReady = active || domain?.state === 'pending_certificate'
  return <section className="custom-domain-panel">
    <header className="domain-heading"><div><h2>自定义域名</h2><p>使用自己的网址访问商店，每个商店可绑定一个域名。</p></div><span className="domain-secure"><ShieldCheck size={14} />自动 HTTPS</span></header>
    {error && <div className="domain-error" role="alert"><span>{error}</span><Button variant="ghost" size="sm" onClick={() => setRevision(v => v + 1)}>重试</Button></div>}
    {!view && !error && <p className="domain-loading" role="status">正在读取域名设置…</p>}
    {view && !view.enabled && <p className="domain-loading" role="status">域名接入暂未开放，原专属链接仍可正常使用。</p>}
    {view?.enabled && !domain && <p className="domain-record-reminder">暂开放 100 个代理名额，按绑定顺序占用，每个商店限一个域名。</p>}
    {view?.enabled && !view.eligible && !domain && <div className="domain-eligibility"><LockKeyhole size={22} /><div><h3>完成 {view.requiredOrders} 单后，可申请开通</h3><p>当前已完成 {view.completedOrders} 单，还需 {Math.max(0, view.requiredOrders - view.completedOrders)} 单。</p><progress value={view.completedOrders} max={view.requiredOrders} aria-label="自定义域名开通进度" /><small>仅统计已付款、成功完成且无退款的正式订单。</small></div></div>}
    {view?.enabled && view.eligible && !domain && <div className="domain-start"><div className="domain-start-intro"><Globe size={26} strokeWidth={1.4} /><h3>绑定你的商店网址</h3><p>支持 example.com 根域名，也支持 shop.example.com 子域名。</p></div><form onSubmit={event => { event.preventDefault(); void mutate('', { hostname }) }}><label htmlFor="store-custom-domain">域名</label><div className="custom-domain-input"><Input id="store-custom-domain" value={hostname} onChange={event => setHostname(event.target.value)} placeholder="example.com" autoCapitalize="none" autoCorrect="off" spellCheck={false} maxLength={253} disabled={busy || readOnly} required /><Button type="submit" disabled={busy || readOnly || !hostname.trim()}>{busy ? '正在绑定…' : '绑定域名'}<ArrowUpRight size={15} /></Button></div><p>只填写域名，不含 https:// 或路径。绑定后会显示需要添加的解析记录。</p></form></div>}
    {domain && <>
      <div className="domain-overview"><div className="domain-address"><Globe size={22} strokeWidth={1.5} /><div><span>商店网址</span><strong>{domain.hostname}</strong></div></div><div className="domain-overview-actions"><span className="domain-state" data-state={domain.state}><i />{labels[domain.state]}</span>{active && <a className="domain-visit" href={'https://' + domain.hostname} target="_blank" rel="noreferrer">访问商店<ArrowUpRight size={15} /></a>}</div></div>
      {domain.state !== 'deleting' && <ol className="domain-steps" aria-label="域名开通步骤">{[{ label: '绑定域名', done: true }, { label: '添加解析', done: dnsReady }, { label: '启用 HTTPS', done: active }].map((step, i) => <li key={step.label} data-done={step.done} aria-current={!step.done && (i === 1 || dnsReady) ? 'step' : undefined}><span>{step.done ? <Check size={13} /> : i + 1}</span>{step.label}</li>)}</ol>}
      <div className="domain-status" role="status"><p>{domain.message}</p>{domain.state === 'pending_certificate' && <small>系统正在自动处理，你可以稍后回来查看。</small>}</div>
      {domain.state !== 'deleting' && <WorkspaceDisclosure className="domain-records" key={active ? 'active' : 'setup'} defaultOpen={!active} title={active ? '查看 DNS 解析记录' : '添加以下 DNS 记录'}><div className="domain-records-content"><div className="domain-dns-note"><strong>CNAME 选择「仅 DNS」</strong><span>关闭这条记录的橙云；商店仍经过平台 Cloudflare 防护。</span></div><div className="domain-record-list">{domain.records.map((record, index) => {
        const suffix = '.' + domain.hostname
        const name = domain.apex ? record.name === domain.hostname ? '@' : record.name.endsWith(suffix) ? record.name.slice(0, -suffix.length) : record.name : record.name
        return <div className="domain-record" key={record.type + record.name + index}><div className="domain-record-title"><span>{record.type}</span><strong>{index === 0 ? '连接商店' : index === 1 ? '验证域名归属' : '补充证书验证'}</strong>{record.type === 'CNAME' && <small>仅 DNS · 灰云</small>}</div><div className="domain-record-field"><span>名称</span><div><code>{name}</code><CopyButton value={name} label="复制记录名称" size="sm" variant="ghost" /></div></div><div className="domain-record-field"><span>{record.type === 'CNAME' ? '目标' : '内容'}</span><div><code>{record.value}</code><CopyButton value={record.value} label="复制记录值" size="sm" variant="ghost" /></div></div></div>
      })}</div><p className="domain-record-reminder">TXT 请完整复制并长期保留。重新绑定会生成新值，记得同步替换旧记录。</p>{domain.apex && <p className="domain-record-reminder">在当前根域名的 DNS 区域中填写。切换前确认原网站可以替换，移除冲突的 @ A/AAAA，保留邮件 MX/TXT。</p>}</div></WorkspaceDisclosure>}
      <div className="custom-domain-actions"><div>{view?.enabled && domain.state !== 'deleting' && <Button variant={active ? 'outline' : 'default'} disabled={busy || readOnly} onClick={() => void mutate('/check', {})}><RefreshCw size={14} className={busy ? 'domain-checking' : ''} />{busy ? '正在检查…' : active ? '重新检查' : '我已添加，检查解析'}</Button>}{domain.checkedAt && <small>最近检查 {new Date(domain.checkedAt).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })}</small>}</div>{!readOnly && domain.state !== 'deleting' && <Button variant="ghost" size="sm" disabled={busy} onClick={() => setConfirm(true)}>解除绑定</Button>}</div>
    </>}
    <WorkspaceDisclosure className="domain-help" title="设置帮助"><div><h4>根域名怎么配置？</h4><p>建议将 DNS 托管到 Cloudflare，免费套餐即可。添加 @ 的 CNAME 并选择「仅 DNS」，无需转移域名注册商。第三方 DNS 的 ALIAS 或扁平化即使解析出相同 IP，也可能无法通过接入验证；遇到此情况可迁移 DNS，或改绑 shop 等子域名并添加普通 CNAME。不要直接填写 Cloudflare IP。</p><h4>为什么没有立即生效？</h4><p>域名接入通过后才会进入证书验证。若提示等待解析，请按上方原因检查 CNAME 和 TXT；若提示证书验证记录，请完整添加。DNS 生效需要时间，不要反复解绑重绑。</p><h4>原链接和访客统计还在吗？</h4><p>原专属链接继续可用，两种入口的访问和订单都归属你的商店。登录状态在不同域名独立。</p></div></WorkspaceDisclosure>
    <Dialog open={confirm} onOpenChange={value => { if (!busy) setConfirm(value) }}><DialogContent><DialogHeader><DialogTitle>解除域名绑定？</DialogTitle><DialogDescription>{domain?.hostname} 将立即停止访问商店，原专属链接仍可使用。重新绑定需要更新 TXT 验证记录。</DialogDescription></DialogHeader><div className="domain-confirm-actions"><Button variant="outline" disabled={busy} onClick={() => setConfirm(false)}>取消</Button><Button variant="destructive" disabled={busy} onClick={() => domain && void mutate('/remove', { hostname: domain.hostname })}>{busy ? '正在解除…' : '确认解除'}</Button></div></DialogContent></Dialog>
  </section>
}
