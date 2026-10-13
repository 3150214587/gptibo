import { emailProduct } from '../../shared/email-products'
import { Feedback } from '@/components/workspace-loading'
export { Feedback } from '@/components/workspace-loading'
import { useEffect, useState } from 'react'
import { Check, Copy } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { api, errorMessage } from '@/lib/api'
import { money, offerName, toCents, type Offer } from './model'

export function CopyLink({ url, label = '专属推广链接' }: { url: string; label?: string }) {
  const [copied, setCopied] = useState(false); const [error, setError] = useState('')
  useEffect(() => { if (!copied) return; const t = setTimeout(() => setCopied(false), 2000); return () => clearTimeout(t) }, [copied])
  async function copy() { try { await navigator.clipboard.writeText(url); setCopied(true); setError('') } catch { setError('复制失败，请手动选择链接复制。') } }
  return <div><div className="partner-copy"><Input value={url} readOnly aria-label={label} onFocus={e => e.target.select()} /><Button onClick={() => void copy()}>{copied ? <Check size={15} /> : <Copy size={15} />}{copied ? '已复制' : '复制链接'}</Button></div><span className="sr-only" role="status">{copied ? label + '已复制' : ''}</span><Feedback error={error} /></div>
}
export function PriceEditor({ offer, cost, endpoint, onClose, onSaved }: { offer: Offer; cost?: boolean; endpoint: string; onClose: () => void; onSaved: () => void }) {
  const [value, setValue] = useState(((cost ? offer.cost_cents : offer.price_cents) / 100).toFixed(2)); const [busy, setBusy] = useState(false); const [error, setError] = useState('')
  const amount = toCents(value); const supply = cost ? amount : offer.cost_cents; const retail = cost ? offer.price_cents : amount
  const valid = Number.isFinite(amount) && amount >= 1 && amount <= 10000000 && retail >= supply
  async function save() { if (busy || !valid) return; setBusy(true); setError(''); try { await api(endpoint, { [cost ? 'costCents' : 'priceCents']: amount, version: offer.version }); onSaved(); onClose() } catch (e) { setError(errorMessage(e)) } finally { setBusy(false) } }
  return <Dialog open onOpenChange={v => { if (!v && !busy) onClose() }}><DialogContent className="motion-dialog partner-dialog"><DialogHeader><DialogTitle>{cost ? '调整供货价' : '调整售价'}</DialogTitle><DialogDescription>{offerName(offer)}{offer.renewal_only ? ' · 仅续费' : ''}</DialogDescription></DialogHeader><form className="partner-form" onSubmit={e => { e.preventDefault(); void save() }}><label htmlFor="partner-price">{cost ? '供货价' : '客户支付价格'}（元）</label><Input id="partner-price" inputMode="decimal" maxLength={9} value={value} onChange={e => setValue(e.target.value)} disabled={busy} autoFocus aria-describedby="price-rule" /><div className="partner-equation"><div><span>客户售价</span><strong>{Number.isFinite(retail) ? money(retail) : '—'}</strong></div><span>−</span><div><span>供货价</span><strong>{Number.isFinite(supply) ? money(supply) : '—'}</strong></div><span>=</span><div><span>每单收益</span><strong className={valid ? 'partner-profit' : ''}>{valid ? money(retail - supply) : '—'}</strong></div></div><p id="price-rule" className="admin-subtle">售价不能低于供货价。修改仅影响新订单。</p><Feedback error={error} /><Button type="submit" disabled={busy || !valid}>{busy ? '正在保存…' : '保存价格'}</Button></form></DialogContent></Dialog>
}
export function Pricing({ plans, cost, readOnly, endpoint, onSaved }: { plans: Offer[]; cost?: boolean; readOnly?: boolean; endpoint: (p: Offer) => string; onSaved: () => void }) {
  const [edit, setEdit] = useState<Offer>()
  return <><div className="partner-pricing"><div className="partner-price-head"><span>商品套餐</span><span>供货价</span><span>客户售价</span><span>每单收益</span><span /></div>{plans.map(p => <div className="partner-price-row" key={p.id}><div className="partner-price-name"><strong>{offerName(p)}</strong><small>{p.renewal_only ? '仅续费' : emailProduct(p.id) ? emailProduct(p.id)!.duration + ' · 邮件交付' : p.product_id === 'chatgpt' ? '按月订阅' : '按次服务'}{!p.enabled && ' · 平台暂停销售'}</small></div><div><span className="partner-mobile-label">供货价</span><strong>{money(p.cost_cents)}</strong></div><div><span className="partner-mobile-label">客户售价</span><strong>{money(p.price_cents)}</strong></div><div><span className="partner-mobile-label">每单收益</span><strong className="partner-profit">{money(p.price_cents - p.cost_cents)}</strong></div><Button variant="outline" disabled={readOnly} onClick={() => setEdit(p)}>{cost ? '改供货价' : '调整售价'}</Button></div>)}</div>{edit && <PriceEditor key={edit.id} offer={edit} cost={cost} endpoint={endpoint(edit)} onClose={() => setEdit(undefined)} onSaved={onSaved} />}</>
}
export function Pagination({ page, total, onPage }: { page: number; total: number; onPage: (p: number) => void }) { return <div className="admin-pagination"><span>共 {total} 条 · 第 {page} 页</span><div><Button variant="outline" disabled={page === 1} onClick={() => onPage(page - 1)}>上一页</Button><Button variant="outline" disabled={page * 20 >= total} onClick={() => onPage(page + 1)}>下一页</Button></div></div> }
