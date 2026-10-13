import { useState } from 'react'
import { ArrowRight, Check, ChevronRight, Mail, Plus, ReceiptText, RefreshCw, ShieldCheck, Store, Users } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { WorkspaceTabs } from './workspace-ui'
import { api, errorMessage } from '@/lib/api'
import { CopyLink, Feedback, Pagination } from './shared'
import { date, money, toCents, usePartnerData, type Partner, type Offer } from './model'
import { partnerStorefrontPath } from '../../shared/partner-link'
import { ChildDefaultPrices } from './ChildDefaultPrices'
import './children.css'

type ChildOffer = Offer & { minimum_cost_cents: number; platform_enabled: boolean }
type ChildDetail = { partner: Partner; plans: ChildOffer[]; stats: { order_count: number; child_profit_cents: number; upstream_profit_cents: number } }
type ChildOrder = { id: string; plan_name: string; price_cents: number; cost_cents: number; status: string; payment_status: string; created_at: number; refunded_cents: number; refund_hold: number; child_profit_cents: number | null; upstream_profit_cents: number | null; mode: string; is_test: number; refund_status?: string; child_payout_status?: string; upstream_payout_status?: string }
const orderState = (s: string) => ({ pending: '待付款', session: '待提交', processing: '处理中', success: '已完成', failed: '失败', cancelled: '已取消' })[s] ?? s
const refundState = (s?: string) => s ? ({ pending: '售后待审核', approved: '退款处理中', refunded: '已退款', rejected: '售后已拒绝', cancelled: '售后已撤回' })[s] ?? '售后处理中' : ''
const settlement = (s?: string) => s === 'paid' ? '已结算' : s === 'pending' ? '结算中' : ''

function ChildPrice({ plan, id, onSaved }: { plan: ChildOffer; id: string; onSaved: () => void }) {
  const [cost, setCost] = useState((plan.cost_cents / 100).toFixed(2))
  const [enabled, setEnabled] = useState(Boolean(plan.enabled))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const amount = toCents(cost)
  const valid = Number.isFinite(amount) && amount >= plan.minimum_cost_cents && amount <= plan.price_cents
  const changed = amount !== plan.cost_cents || enabled !== Boolean(plan.enabled)
  async function save() {
    if (busy || !valid || !changed) return
    setBusy(true); setError('')
    try { await api(`/api/partner/children/${id}/prices/${plan.id}`, { costCents: amount, enabled, version: plan.version }); onSaved() }
    catch (e) { setError(errorMessage(e)) } finally { setBusy(false) }
  }
  return <form className="child-price" onSubmit={e => { e.preventDefault(); void save() }}>
    <div className="child-price-heading"><h3>{plan.name}</h3><Badge variant="secondary">{plan.platform_enabled ? '平台可供货' : '平台暂停供货'}</Badge></div>
    <div className="child-price-body">
      <Field data-invalid={!valid}><FieldLabel htmlFor={'child-cost-' + plan.id}>二级供货价（元）</FieldLabel><Input id={'child-cost-' + plan.id} value={cost} onChange={e => setCost(e.target.value)} inputMode="decimal" maxLength={9} disabled={busy} required aria-invalid={!valid} aria-describedby={'child-cost-rule-' + plan.id} /><FieldDescription id={'child-cost-rule-' + plan.id}>可设范围 {money(plan.minimum_cost_cents)} – {money(plan.price_cents)}</FieldDescription></Field>
      <dl className="child-price-equation"><div><dt>你的每单差价</dt><dd>{valid ? money(amount - plan.minimum_cost_cents) : '—'}</dd></div><div><dt>二级每单差价</dt><dd>{valid ? money(plan.price_cents - amount) : '—'}</dd></div></dl>
    </div>
    <div className="child-price-bottom"><Field orientation="horizontal"><Checkbox id={'child-enabled-' + plan.id} checked={enabled} disabled={busy || (!enabled && !plan.platform_enabled)} onCheckedChange={setEnabled} /><FieldLabel htmlFor={'child-enabled-' + plan.id}>开放此套餐</FieldLabel></Field><Button type="submit" variant="outline" disabled={busy || !valid || !changed}>{busy ? '保存中…' : '保存供货设置'}</Button></div>
    {!valid && <p className="child-field-error">供货价须在可设范围内。提价前，请先让二级调整客户售价。</p>}<Feedback error={error} />
  </form>
}

function ChildAccount({ partner, onSaved }: { partner: Partner; onSaved: () => void }) {
  const [email, setEmail] = useState(partner.email)
  const [active, setActive] = useState(Boolean(partner.active))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [message, setMessage] = useState('')
  async function save(invite = false) {
    if (busy) return
    setBusy(true); setError(''); setMessage('')
    try {
      await api(`/api/partner/children/${partner.id}${invite ? '/invitation' : ''}`, invite ? {} : { email, active, version: partner.version })
      if (invite) setMessage('邀请邮件已提交发送')
      else onSaved()
    } catch (e) { setError(errorMessage(e)) } finally { setBusy(false) }
  }
  return <div className="child-account-layout">
    <form className="child-settings-group" onSubmit={e => { e.preventDefault(); void save() }}>
      <div className="child-section-title"><Mail aria-hidden="true" /><h3>登录与账号</h3></div>
      <Field><FieldLabel htmlFor="child-email">登录邮箱</FieldLabel><Input id="child-email" type="email" required maxLength={254} disabled={busy} value={email} onChange={e => setEmail(e.target.value)} aria-describedby="child-email-help" /><FieldDescription id="child-email-help">修改后旧登录与验证码失效，二级需使用新邮箱登录。</FieldDescription></Field>
      <Field orientation="horizontal" className="child-account-toggle"><Checkbox id="child-active" disabled={busy} checked={active} onCheckedChange={setActive} /><div><FieldLabel htmlFor="child-active">启用二级账号与商店</FieldLabel><FieldDescription>停用后暂停登录与新接单，历史记录保留。</FieldDescription></div></Field>
      <Feedback error={error} /><p className="child-status" role="status">{message}</p>
      <div className="child-actions"><Button type="submit" disabled={busy || (email === partner.email && active === Boolean(partner.active))}>{busy ? '处理中…' : '保存账号设置'}</Button><Button type="button" variant="outline" disabled={busy || !partner.active} onClick={() => void save(true)}>重发邀请邮件</Button></div>
    </form>
    <aside className="child-account-aside"><ShieldCheck aria-hidden="true" /><h3>独立经营，清晰分工。</h3><p>你管理账号与供货设置。二级自行管理店铺、客户售价和客户资料，并直接向平台提现。</p><div className="child-rule"><span>店铺状态</span><Badge variant={partner.active ? 'secondary' : 'outline'}>{partner.active ? '已启用' : '已停用'}</Badge></div></aside>
    <section className="child-store-link"><div className="child-section-title"><Store aria-hidden="true" /><h3>二级专属店铺</h3></div><CopyLink url={partner.storefront_url || (partner.referralOrigin || location.origin) + partnerStorefrontPath(partner.slug)} /></section>
  </div>
}

export function ChildOrders({ id, revision }: { id?: string; revision: number }) {
  const [page, setPage] = useState(1)
  const { data, error, loading } = usePartnerData<{ rows: ChildOrder[]; total: number }>(`${id ? `/api/partner/children/${id}/orders` : '/api/partner/upstream-orders'}?page=${page}`, revision)
  return <div className="child-orders"><Feedback error={error} loading={loading} data={data} />{data && <>
    <p className="child-help">仅正式已完成订单计入收益。售后与资金核对会影响可提现金额。</p>
    {data.total > 0 ? <Table className="child-order-table"><TableHeader><TableRow><TableHead>订单 / 时间</TableHead><TableHead>商品 / 状态</TableHead><TableHead>成交金额</TableHead><TableHead>二级收益</TableHead><TableHead>你的收益</TableHead></TableRow></TableHeader><TableBody>{data.rows.map(o => <TableRow key={o.id}>
      <TableCell className="child-order-identity"><span className="child-order-id">{o.id}</span><small>{date(o.created_at)}</small></TableCell>
      <TableCell className="child-order-product"><strong>{o.plan_name}</strong><div className="child-order-badges"><Badge variant="secondary">{orderState(o.status)}</Badge>{(o.mode !== 'live' || Boolean(o.is_test)) && <Badge variant="outline">测试订单</Badge>}</div>{o.refund_status && <small>{refundState(o.refund_status)}</small>}{o.refund_hold > 0 && <small>收益冻结待核对</small>}</TableCell>
      <TableCell data-label="成交金额"><span className="child-amount">{money(o.price_cents)}</span><small>{o.payment_status === 'paid' ? '已付款' : '待付款'}{o.refunded_cents > 0 ? ` · 已退 ${money(o.refunded_cents)}` : ''}</small></TableCell>
      <TableCell data-label="二级收益"><span className="child-amount">{o.child_profit_cents == null ? '待核算' : money(o.child_profit_cents)}</span><small>{settlement(o.child_payout_status)}</small></TableCell>
      <TableCell data-label="你的收益"><span className="child-amount partner-profit">{o.upstream_profit_cents == null ? '待核算' : money(o.upstream_profit_cents)}</span><small>{settlement(o.upstream_payout_status)}</small></TableCell>
    </TableRow>)}</TableBody></Table> : <div className="child-empty"><ReceiptText aria-hidden="true" /><h3>还没有订单</h3><p>二级店铺产生订单后，可在这里查看进度与双方收益。</p></div>}
    {data.total > 0 && <Pagination page={page} total={data.total} onPage={setPage} />}
  </>}</div>
}

function ChildEditor({ id, onClose, onSaved }: { id: string; onClose: () => void; onSaved: () => void }) {
  const [revision, setRevision] = useState(0)
  const [message, setMessage] = useState('')
  const { data, error, loading } = usePartnerData<ChildDetail>(`/api/partner/children/${id}`, revision)
  const refresh = () => { setRevision(v => v + 1); setMessage('设置已保存'); onSaved() }
  return <Dialog open onOpenChange={v => { if (!v) onClose() }}><DialogContent className="motion-dialog partner-management-dialog child-dialog">
    <DialogHeader><span className="child-eyebrow">直属二级代理</span><DialogTitle>{data?.partner.name ?? '管理二级代理'}</DialogTitle><DialogDescription>{data?.partner.email ?? '正在读取账号信息'}</DialogDescription></DialogHeader>
    <Feedback error={error} loading={loading} data={data} shape="compact" /><p className="child-status" role="status">{message}</p>
    {data && <WorkspaceTabs label="二级代理管理" defaultValue="account" onValueChange={() => setMessage('')} items={[
      {value: 'account', label: '账号', content: <ChildAccount key={data.partner.version} partner={data.partner} onSaved={refresh} />},
      {value: 'pricing', label: '供货设置', content: <><div className="child-section-heading"><h3>为每个套餐设定供货价</h3><p>客户售价由二级设置；供货改价仅影响新订单。</p></div><div className="child-price-list">{data.plans.map(p => <ChildPrice key={p.id + ':' + p.version} plan={p} id={id} onSaved={refresh} />)}</div></>},
      {value: 'orders', label: '订单与收益', content: <><dl className="child-earnings-summary"><div><dt>二级订单</dt><dd>{data.stats.order_count.toLocaleString('zh-CN')}<small>笔</small></dd></div><div><dt>二级已完成收益</dt><dd>{money(data.stats.child_profit_cents)}</dd></div><div><dt>你的已完成差价</dt><dd className="partner-profit">{money(data.stats.upstream_profit_cents)}</dd></div></dl><ChildOrders id={id} revision={revision} /></>},
    ]} />}
  </DialogContent></Dialog>
}

function ChildCreator({ onClose, onSaved }: { onClose: () => void; onSaved: (message: string) => void }) {
  const [email, setEmail] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  async function create() {
    if (busy) return
    setBusy(true); setError('')
    try { const result = await api<{ message: string }>('/api/partner/children', { email }); onSaved(result.message) }
    catch (e) { setError(errorMessage(e)) } finally { setBusy(false) }
  }
  return <Dialog open onOpenChange={v => { if (!v && !busy) onClose() }}><DialogContent className="motion-dialog child-create-dialog" showCloseButton={!busy}>
    <DialogHeader><DialogTitle>开通二级代理</DialogTitle><DialogDescription>填写登录邮箱，为二级创建独立店铺。</DialogDescription></DialogHeader>
    <form id="child-create-form" className="child-create-form" onSubmit={e => { e.preventDefault(); void create() }}><Field><FieldLabel htmlFor="new-child-email">二级登录邮箱</FieldLabel><Input id="new-child-email" type="email" required maxLength={254} disabled={busy} value={email} onChange={e => setEmail(e.target.value)} placeholder="name@example.com" autoFocus /><FieldDescription>邀请邮件将发送至此邮箱。</FieldDescription></Field><div className="child-create-note"><Check aria-hidden="true" /><p>初始拿货价与售价采用你保存的二级默认拿货价，开通后可分别调整。</p></div><Feedback error={error} /></form>
    <DialogFooter><Button variant="outline" disabled={busy} onClick={onClose}>取消</Button><Button type="submit" form="child-create-form" disabled={busy || !email.trim()}>{busy ? '开通中…' : '确认开通'}<ArrowRight aria-hidden="true" /></Button></DialogFooter>
  </DialogContent></Dialog>
}

export function Children({ canRecruit, revision, onSaved }: { canRecruit: boolean; revision: number; onSaved: () => void }) {
  const [page, setPage] = useState(1)
  const [localRevision, setLocalRevision] = useState(0)
  const [selected, setSelected] = useState<string>()
  const [creating, setCreating] = useState(false)
  const [message, setMessage] = useState('')
  const list = usePartnerData<{ rows: Partner[]; total: number }>(`/api/partner/children?page=${page}`, revision + localRevision)
  const refresh = () => { setLocalRevision(v => v + 1); onSaved() }
  return <section className="child-workspace">
    <header className="child-workspace-heading"><div><span className="child-eyebrow">合作与经营</span><h2>二级代理</h2><p>管理直属二级代理，设置供货价，查看每一笔差价收益。</p></div><div className="child-heading-actions"><Button variant="outline" size="icon" aria-label="刷新二级代理列表" disabled={list.loading} onClick={refresh}><RefreshCw aria-hidden="true" /></Button>{canRecruit && <Button onClick={() => setCreating(true)}><Plus aria-hidden="true" />开通二级代理</Button>}</div></header>
    <div className="child-overview"><div className="child-overview-total"><span>直属二级代理</span><strong>{list.data ? list.data.total.toLocaleString('zh-CN') : '—'}<small>位</small></strong></div><div className="child-overview-rule"><ShieldCheck aria-hidden="true" /><div><strong>{canRecruit ? '新增权限已开放' : '新增权限已关闭'}</strong><p>{canRecruit ? '可为新的合作伙伴开通独立账号' : '已有二级仍可管理和经营'}</p></div></div><div className="child-overview-rule"><ReceiptText aria-hidden="true" /><div><strong>差价收益，独立结算</strong><p>二级向平台提现，你的差价计入工作台余额</p></div></div></div>
    <ChildDefaultPrices revision={revision + localRevision} total={list.data?.total ?? 0} onSaved={refresh} />
    <p className="child-status" role="status">{message}</p><Feedback error={list.error} loading={list.loading} data={list.data} />
    {list.data && <section className="child-list-panel"><div className="child-list-heading"><h3>代理列表</h3><span>仅显示你的直属二级</span></div>{list.data.total > 0 ? <>
      <Table className="child-partner-table"><TableHeader><TableRow><TableHead>店铺</TableHead><TableHead>登录邮箱</TableHead><TableHead>状态</TableHead><TableHead><span className="sr-only">管理操作</span></TableHead></TableRow></TableHeader><TableBody>{list.data.rows.map(p => <TableRow key={p.id}><TableCell><div className="child-store-identity"><span className="child-store-icon"><Store aria-hidden="true" /></span><div><strong>{p.name}</strong><small>二级代理</small></div></div></TableCell><TableCell className="child-partner-email">{p.email}</TableCell><TableCell className="child-partner-status"><Badge variant={p.active ? 'secondary' : 'outline'}>{p.active ? '已启用' : '已停用'}</Badge></TableCell><TableCell className="child-partner-action"><Button variant="ghost" aria-label={'管理 ' + p.email} onClick={() => setSelected(p.id)}>管理<ChevronRight aria-hidden="true" /></Button></TableCell></TableRow>)}</TableBody></Table>
      <Pagination page={page} total={list.data.total} onPage={setPage} />
    </> : <div className="child-empty"><Users aria-hidden="true" /><h3>从第一位合作伙伴开始</h3><p>{canRecruit ? '点击“开通二级代理”，填写邮箱即可创建独立店铺。' : '当前暂无二级代理。总后台开放权限后，可添加合作伙伴。'}</p></div>}</section>}
    {creating && canRecruit && <ChildCreator onClose={() => setCreating(false)} onSaved={result => { setMessage(result); setCreating(false); setPage(1); refresh() }} />}
    {selected && <ChildEditor id={selected} onClose={() => setSelected(undefined)} onSaved={refresh} />}
  </section>
}
