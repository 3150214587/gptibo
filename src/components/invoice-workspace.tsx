import { PaymentMethodChoices, type PaymentMethodOption } from './payment-method-choices'
import { paymentChannel } from '@/lib/payment-channel'
import { useEffect, useRef, useState, type ComponentProps } from 'react'
import QRCode from 'qrcode'
import { Check, ChevronDown, Clock, FileText, LoaderCircle, Plus, RefreshCw, Search } from 'lucide-react'
import { Button } from './ui/button'
import { Badge } from './ui/badge'
import { Input } from './ui/input'
import { Textarea } from './ui/textarea'
import { Checkbox } from './ui/checkbox'
import { Field, FieldDescription, FieldGroup, FieldLabel } from './ui/field'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from './ui/dialog'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from './ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from './ui/table'
import { Tabs, TabsList, TabsTrigger } from './ui/tabs'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from './ui/collapsible'
import { CopyButton } from './copy-button'
import { Feedback } from './workspace-loading'
import { api, errorMessage, RequestError } from '@/lib/api'
import { useResource } from '@/lib/use-resource'
import { INVOICE_STATUSES, INVOICE_STATUS_LABELS, invoiceAmounts, parseInvoiceAmount, type Invoice, type InvoiceDetail, type InvoiceList, type InvoicePaymentView } from '../../shared/invoices.ts'
import './invoice-workspace.css'

const money = (cents: number) => new Intl.NumberFormat('zh-CN', { style: 'currency', currency: 'CNY' }).format(cents / 100)
const date = (ms: number) => new Date(ms).toLocaleString('zh-CN', { timeZone: 'Asia/Shanghai', year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false })
const typeLabel = (type: Invoice['invoice_type']) => type === 'special' ? '增值税专用发票' : '增值税普通发票'
function Status({ invoice }: { invoice: Invoice }) {
  return <Badge variant={invoice.status === 'awaiting_submission' ? 'secondary' : 'outline'} className="invoice-status" data-state={invoice.status}>{invoice.status === 'completed' && <Check size={12} />}{INVOICE_STATUS_LABELS[invoice.status]}</Badge>
}
function TextField({ label, id, ...props }: ComponentProps<typeof Input> & { label: string; id: string }) {
  return <Field><FieldLabel htmlFor={id}>{label}</FieldLabel><Input id={id} {...props} /></Field>
}
function Amounts({ base, fee, total, includeFee }: { base: number | null; fee: number | null; total: number | null; includeFee: boolean }) {
  return <dl className="invoice-amounts"><div><dt>原始金额</dt><dd>{base === null ? '—' : money(base)}</dd></div><div><dt>手续费 <span>5%</span></dt><dd>{fee === null ? '—' : money(fee)}</dd></div><div className="invoice-total"><dt>最终开票金额</dt><dd>{total === null ? '—' : money(total)}</dd></div><p>{includeFee ? '包含手续费，按此金额提交平台。' : '手续费单独支付，不计入票面金额。'}</p></dl>
}

function CreateInvoice({ onClose, onCreated }: { onClose: () => void; onCreated: (id: string) => void }) {
  const [key] = useState(() => crypto.randomUUID())
  const [amount, setAmount] = useState(''); const [includeFee, setIncludeFee] = useState(false)
  const [busy, setBusy] = useState(false); const [error, setError] = useState(''); const running = useRef(false)
  const base = parseInvoiceAmount(amount); const amounts = base === null ? null : invoiceAmounts(base, includeFee)
  async function submit(form: HTMLFormElement) {
    if (running.current) return
    if (base === null) { setError('请输入 0.10–100000 元的原始金额，最多两位小数。'); return }
    const data = Object.fromEntries(new FormData(form))
    running.current = true; setBusy(true); setError('')
    try {
      const result = await api<{ invoice: Invoice }>('/api/partner/invoices', { ...data, key, baseCents: base, includeFee, invoiceType: 'ordinary' })
      onCreated(result.invoice.id)
    } catch (e) { setError(errorMessage(e)) } finally { running.current = false; setBusy(false) }
  }
  return <Dialog open onOpenChange={open => { if (!open && !busy) onClose() }}><DialogContent className="invoice-dialog invoice-create motion-dialog" showCloseButton={!busy}>
    <DialogHeader><DialogTitle>申请开票</DialogTitle><DialogDescription>填写开票信息，在线支付 5% 手续费。</DialogDescription></DialogHeader>
    <form onSubmit={e => { e.preventDefault(); void submit(e.currentTarget) }}>
      <div className="invoice-create-layout"><FieldGroup className="invoice-fields">
        <TextField id="invoice-title" name="title" label="发票抬头" placeholder="请输入个人或企业名称" required maxLength={200} disabled={busy} />
        <div className="invoice-field-pair"><Field><FieldLabel htmlFor="invoice-type">发票类型</FieldLabel><Select value="ordinary" disabled={busy} items={{ ordinary: '增值税普通发票' }}><SelectTrigger id="invoice-type"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="ordinary">增值税普通发票</SelectItem></SelectContent></Select></Field><TextField id="invoice-tax" name="taxNumber" label="税号" placeholder="纳税人识别号" required maxLength={100} disabled={busy} /></div>
        <TextField id="invoice-email" name="email" label="接收邮箱" type="email" autoComplete="email" placeholder="用于接收电子发票" required maxLength={254} disabled={busy} />
        <Field><FieldLabel htmlFor="invoice-base">原始金额（元）</FieldLabel><Input id="invoice-base" inputMode="decimal" value={amount} onChange={e => setAmount(e.target.value)} placeholder="例如 140.00" maxLength={9} required disabled={busy} aria-describedby="invoice-fee-rule" /><FieldDescription id="invoice-fee-rule">手续费按原始金额的 5% 计算，四舍五入到分。</FieldDescription></Field>
        <Field orientation="horizontal" className="invoice-fee-choice"><Checkbox id="invoice-include" checked={includeFee} onCheckedChange={v => setIncludeFee(Boolean(v))} disabled={busy} /><div><FieldLabel htmlFor="invoice-include">手续费计入开票金额</FieldLabel><FieldDescription>勾选后票面金额增加手续费，支付金额不变。</FieldDescription></div></Field>
        <Collapsible className="invoice-extra"><CollapsibleTrigger type="button" className="invoice-extra-trigger">补充开票信息（选填）<ChevronDown size={15} /></CollapsibleTrigger><CollapsibleContent keepMounted><FieldGroup className="invoice-extra-fields"><TextField id="invoice-address" name="address" label="注册地址" maxLength={300} disabled={busy} /><TextField id="invoice-phone" name="phone" label="联系电话" maxLength={50} disabled={busy} /><div className="invoice-field-pair"><TextField id="invoice-bank" name="bankName" label="开户银行" maxLength={200} disabled={busy} /><TextField id="invoice-account" name="bankAccount" label="银行账号" maxLength={100} disabled={busy} /></div><Field><FieldLabel htmlFor="invoice-note">备注</FieldLabel><Textarea id="invoice-note" name="note" placeholder="需要随申请提交的说明" maxLength={1000} disabled={busy} /></Field></FieldGroup></CollapsibleContent></Collapsible>
      </FieldGroup><aside className="invoice-summary"><span className="invoice-section-label">金额明细</span><Amounts base={base} fee={amounts?.feeCents ?? null} total={amounts?.invoiceCents ?? null} includeFee={includeFee} /><div className="invoice-payable" aria-live="polite"><span>本次需支付手续费</span><strong>{amounts ? money(amounts.feeCents) : '—'}</strong><p>付款成功后，由平台统一处理开票。</p></div></aside></div>
      <Feedback error={error} /><DialogFooter><Button type="button" variant="outline" onClick={onClose} disabled={busy}>取消</Button><Button type="submit" disabled={busy || base === null}>{busy && <LoaderCircle className="processing-spinner" />}{busy ? '正在提交…' : '提交并支付手续费'}</Button></DialogFooter>
    </form>
  </DialogContent></Dialog>
}

function InvoicePayment({ invoice, initial, onChanged }: { invoice: Invoice; initial: InvoicePaymentView | null; onChanged: () => void }) {
  const [result, setResult] = useState<{ invoice: Invoice; payment: InvoicePaymentView | null }>({ invoice, payment: initial })
  const [image, setImage] = useState<{ qr: string; url: string }>(); const [imageError, setImageError] = useState('')
  const [error, setError] = useState(''); const [busy, setBusy] = useState(false); const [now, setNow] = useState(Date.now())
  const [methods, setMethods] = useState<PaymentMethodOption[]>([])
  const run = useRef<((action: 'create' | 'query' | 'options', method?: string) => Promise<void>) | null>(null)
  useEffect(() => {
    let active = true; let working = false; let timer: ReturnType<typeof setTimeout> | undefined
    async function act(action: 'create' | 'query' | 'options', method?: string) {
      if (working || !active) return
      working = true; setBusy(true)
      if (timer) clearTimeout(timer)
      let delay = 5000; let poll = true
      try {
        const value = await api<{ invoice: Invoice; payment: InvoicePaymentView | null; methods?: PaymentMethodOption[] }>(`/api/partner/invoices/${invoice.id}/payment`, { action, method })
        if (!active) return
        setResult(value); if (value.methods) setMethods(value.methods); setError('')
        if (value.invoice.status !== 'awaiting_payment') { poll = false; onChanged() }
        else if (value.payment?.status === 'closed' || !value.payment) poll = false
      } catch (e) {
        if (active) setError(errorMessage(e))
        delay = 15000
        if (e instanceof RequestError && [401, 403, 404].includes(e.status)) poll = false
      } finally {
        working = false
        if (active) { setBusy(false); if (poll) timer = setTimeout(() => void act('query'), delay) }
      }
    }
    run.current = act
    void act('options')
    const clock = setInterval(() => setNow(Date.now()), 1000)
    return () => { active = false; run.current = null; clearInterval(clock); if (timer) clearTimeout(timer) }
    // The payment session belongs to this invoice, not to detail refreshes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [invoice.id])
  const p = result.payment; const qr = p?.qr ?? null
  useEffect(() => {
    let active = true; setImageError('')
    if (qr) void QRCode.toDataURL(qr, { width: 224, margin: 2, errorCorrectionLevel: 'M' }).then(url => { if (active) setImage({ qr, url }) }).catch(() => { if (active) setImageError('二维码显示失败，请重新打开申请。') })
    return () => { active = false }
  }, [qr])
  const remaining = p ? Math.min(900, Math.max(0, Math.ceil((p.expiresAt - now) / 1000))) : 0
  const paid = result.invoice.status !== 'awaiting_payment'; const expired = Boolean(p && remaining === 0)
  return <section className="invoice-payment" aria-label="手续费支付">
    <div className="invoice-payment-heading"><strong>{p ? paymentChannel(p.provider).name : '支付手续费'}</strong>{p && !paid && !expired && <span><Clock size={13} />{Math.floor(remaining / 60)}:{String(remaining % 60).padStart(2, '0')}</span>}</div>
    {paid ? <div className="invoice-payment-success" role="status"><Check size={24} /><strong>手续费已支付</strong><p>申请已进入待审核列表。</p></div> : !p || p.status === 'closed' ? <><PaymentMethodChoices methods={methods} busy={busy} onChoose={method => void run.current?.('create', method)} /><Feedback error={error} />{!busy && !methods.length && <Button variant="outline" onClick={() => void run.current?.('options')}>刷新支付方式</Button>}</> : <><div className="invoice-payment-body"><div className="invoice-qr">{qr && !expired && image?.qr === qr ? <img src={image.url} alt="手续费付款二维码" width={200} height={200} /> : expired ? <><Clock size={24} /><span>二维码已过期</span></> : imageError ? <span>{imageError}</span> : error ? <span>二维码暂不可用</span> : <LoaderCircle className="processing-spinner" aria-label="正在准备付款二维码" />}</div><div className="invoice-payment-amount"><span>本次支付手续费</span><strong>{money(invoice.fee_cents)}</strong><p>扫码进入收银台完成付款。<br />付款后会自动确认，无需重复支付。</p>{qr && !expired && <a href={qr} target="_blank" rel="noopener noreferrer">{paymentChannel(p?.provider).action}</a>}{expired && <p>确认旧交易关闭后可重新生成二维码。</p>}</div></div><Feedback error={error || imageError} /><div className="invoice-payment-actions"><span>最终开票金额 {money(invoice.invoice_cents)}</span><Button variant="outline" disabled={busy} onClick={() => void run.current?.(expired || !p.qr ? 'create' : 'query')}><RefreshCw className={busy ? 'processing-spinner' : ''} size={14} />{busy ? '正在确认…' : expired ? '重新获取付款码' : !p.qr ? '重新获取付款码' : '我已付款，确认状态'}</Button></div></>}
  </section>
}

function submissionText(i: Invoice) {
  return [`申请编号：${i.id}`, `发票抬头：${i.title}`, `税号：${i.tax_number || '未填写'}`, `发票类型：${typeLabel(i.invoice_type)}`, `开票金额：${money(i.invoice_cents)}`, `接收邮箱：${i.email}`, `注册地址：${i.address || '未填写'}`, `联系电话：${i.phone || '未填写'}`, `开户银行：${i.bank_name || '未填写'}`, `银行账号：${i.bank_account || '未填写'}`, `备注：${i.note || '无'}`].join('\n')
}
function InvoiceDetails({ id, admin, onClose, onUpdated }: { id: string; admin: boolean; onClose: () => void; onUpdated: () => void }) {
  const root = admin ? '/api/admin/invoices' : '/api/partner/invoices'
  const [revision, setRevision] = useState(0); const { data, error, loading } = useResource<InvoiceDetail>(`${root}/${id}`, revision)
  const [busy, setBusy] = useState(false); const [saveError, setSaveError] = useState(''); const [notice, setNotice] = useState('')
  const running = useRef(false)
  function refresh() { setRevision(v => v + 1); onUpdated() }
  async function action(kind: 'submit' | 'complete', form: HTMLFormElement) {
    if (!data || running.current) return
    running.current = true; setBusy(true); setSaveError(''); setNotice('')
    try {
      await api(`${root}/${id}/${kind}`, { ...Object.fromEntries(new FormData(form)), version: data.invoice.version })
      setNotice(kind === 'submit' ? '已登记提交平台，等待开票。' : '已登记开票完成。'); refresh()
    } catch (e) { setSaveError(errorMessage(e)) } finally { running.current = false; setBusy(false) }
  }
  async function queryPayment() {
    if (running.current) return
    running.current = true; setBusy(true); setSaveError('')
    try { await api(`${root}/${id}/payment`, { action: 'query' }); refresh() } catch (e) { setSaveError(errorMessage(e)) } finally { running.current = false; setBusy(false) }
  }
  const i = data?.invoice
  return <Dialog open onOpenChange={open => { if (!open && !busy) onClose() }}><DialogContent className="invoice-dialog invoice-detail motion-dialog" showCloseButton={!busy}>
    <DialogHeader><DialogTitle>开票申请详情</DialogTitle><DialogDescription>查看开票资料、手续费与处理进度。</DialogDescription></DialogHeader><Feedback error={error} loading={loading} data={data} shape="detail" />
    {i && <><div className="invoice-detail-title"><div><strong>{i.title}</strong><span>{typeLabel(i.invoice_type)}</span></div><Status invoice={i} /></div>
      {!admin && i.status === 'awaiting_payment' && <InvoicePayment invoice={i} initial={data.payment} onChanged={refresh} />}
      <div className="invoice-detail-layout"><section><span className="invoice-section-label">开票信息</span><dl className="invoice-info">{(admin ? [['代理', `${i.partner_name || '代理'} · ${i.partner_email}`]] : []).concat([['发票抬头', i.title], ['税号', i.tax_number || '未填写'], ['接收邮箱', i.email], ...(i.address ? [['注册地址', i.address]] : []), ...(i.phone ? [['联系电话', i.phone]] : []), ...(i.bank_name ? [['开户银行', i.bank_name]] : []), ...(i.bank_account ? [['银行账号', i.bank_account]] : []), ...(i.note ? [['备注', i.note]] : [])]).map(([label, value]) => <div key={label}><dt>{label}</dt><dd>{value}</dd></div>)}</dl>{admin && <CopyButton variant="outline" value={submissionText(i)} label="复制平台开票资料" />}</section><aside className="invoice-summary"><span className="invoice-section-label">金额明细</span><Amounts base={i.base_cents} fee={i.fee_cents} total={i.invoice_cents} includeFee={Boolean(i.include_fee)} /><div className="invoice-receipt"><span>手续费收款</span><strong>{i.paid_at ? '已支付 ' + money(i.fee_cents) : '待支付 ' + money(i.fee_cents)}</strong>{i.paid_at && <small>{date(i.paid_at)}</small>}</div></aside></div>
      {admin && ['awaiting_submission', 'submitted'].includes(i.status) && <form className="invoice-admin-action" key={i.status} onSubmit={e => { e.preventDefault(); void action(i.status === 'awaiting_submission' ? 'submit' : 'complete', e.currentTarget) }}><div><h3>{i.status === 'awaiting_submission' ? '审核与平台提交' : '登记开票完成'}</h3><p>{i.status === 'awaiting_submission' ? '核对资料并人工提交平台后，标记为待开票。' : '平台完成开票后，更新申请状态。'}</p></div><div className="invoice-field-pair"><TextField id="invoice-reference" name="reference" label="平台单号（选填）" defaultValue={i.platform_reference} maxLength={200} disabled={busy} /><TextField id="invoice-admin-note" name="note" label="处理备注（选填）" maxLength={500} disabled={busy} /></div><Button type="submit" disabled={busy || loading}>{busy && <LoaderCircle className="processing-spinner" />}{i.status === 'awaiting_submission' ? '标记待开票' : '标记已开票'}</Button></form>}
      {i.platform_reference && <p className="invoice-reference">平台单号：{i.platform_reference}</p>}
      <section className="invoice-history"><span className="invoice-section-label">处理记录</span><ol>{data.events.map(e => <li key={e.id}><span className="invoice-history-dot" /><div><strong>{e.note}</strong><time>{date(e.created_at)}</time></div></li>)}</ol></section>
      <p className="invoice-request-id">申请编号 <span>{i.id}</span></p></>}
    <Feedback error={saveError} />{notice && <p className="invoice-notice" role="status">{notice}</p>}
    <DialogFooter>{admin && i?.status === 'awaiting_payment' && <Button variant="outline" onClick={() => void queryPayment()} disabled={busy}>核对手续费付款</Button>}<Button variant="outline" disabled={busy || loading} onClick={refresh}><RefreshCw size={14} />刷新详情</Button><Button variant="outline" onClick={onClose} disabled={busy}>关闭</Button></DialogFooter>
  </DialogContent></Dialog>
}

export default function InvoiceWorkspace({ admin = false, revision = 0 }: { admin?: boolean; revision?: number }) {
  const [localRevision, setLocalRevision] = useState(0); const [status, setStatus] = useState(''); const [search, setSearch] = useState(''); const [q, setQ] = useState(''); const [page, setPage] = useState(1)
  const [creating, setCreating] = useState(false); const [detail, setDetail] = useState<string>()
  const root = admin ? '/api/admin/invoices' : '/api/partner/invoices'
  const { data, error, loading } = useResource<InvoiceList>(`${root}?${new URLSearchParams({ page: String(page), status, q })}`, revision + localRevision)
  const refresh = () => setLocalRevision(v => v + 1)
  return <div className="invoice-workspace">{!admin && <div className="invoice-workspace-heading"><div><h2>我的开票申请</h2><p>提交开票信息并支付 5% 手续费，随时查看处理进度。</p></div><Button onClick={() => setCreating(true)}><Plus size={16} />申请开票</Button></div>}
    <section className="invoice-list"><div className="invoice-list-toolbar"><Tabs value={status || 'all'} onValueChange={v => { setStatus(v === 'all' ? '' : String(v)); setPage(1) }}><TabsList variant="line" aria-label="开票申请状态"><TabsTrigger value="all">全部</TabsTrigger>{INVOICE_STATUSES.map(s => <TabsTrigger key={s} value={s}>{INVOICE_STATUS_LABELS[s]}</TabsTrigger>)}</TabsList></Tabs><form className="invoice-search" onSubmit={e => { e.preventDefault(); setQ(search.trim()); setPage(1) }}><Input aria-label="搜索开票申请" placeholder={admin ? '搜索抬头、邮箱或申请编号' : '搜索抬头或申请编号'} value={search} onChange={e => setSearch(e.target.value)} maxLength={100} /><Button type="submit" variant="outline" size="icon" aria-label="搜索"><Search size={16} /></Button></form></div>
      <Feedback error={error} loading={loading} data={data} />{error && <Button variant="outline" onClick={refresh}>重新加载</Button>}
      {data && <><Table className="invoice-table"><TableHeader><TableRow><TableHead>开票抬头{admin && ' / 代理'}</TableHead><TableHead className="invoice-number">原始金额</TableHead><TableHead className="invoice-number">手续费 · 5%</TableHead><TableHead className="invoice-number">开票金额</TableHead><TableHead>状态</TableHead><TableHead>申请时间</TableHead><TableHead><span className="sr-only">操作</span></TableHead></TableRow></TableHeader><TableBody>{data.rows.map(i => <TableRow key={i.id}><TableCell><div className="invoice-row-title"><strong>{i.title}</strong><span>{admin ? i.partner_name || i.partner_email : typeLabel(i.invoice_type)}</span></div></TableCell><TableCell className="invoice-number">{money(i.base_cents)}</TableCell><TableCell className="invoice-number">{money(i.fee_cents)}</TableCell><TableCell className="invoice-number"><strong>{money(i.invoice_cents)}</strong>{Boolean(i.include_fee) && <small>含手续费</small>}</TableCell><TableCell><Status invoice={i} /></TableCell><TableCell className="invoice-row-date">{date(i.created_at)}</TableCell><TableCell><Button variant="ghost" size="sm" onClick={() => setDetail(i.id)}>{!admin && i.status === 'awaiting_payment' ? '去支付' : admin && i.status === 'awaiting_submission' ? '处理' : '详情'}</Button></TableCell></TableRow>)}</TableBody></Table>
      {!data.rows.length && <div className="invoice-empty"><FileText size={28} strokeWidth={1.4} /><h3>{q || status ? '没有符合条件的申请' : '暂无开票申请'}</h3><p>{q || status ? '调整搜索或状态筛选后重试。' : admin ? '代理支付手续费后，可在此处理开票申请。' : '填写开票抬头和金额，支付手续费后提交平台处理。'}</p>{!admin && !q && !status && <Button variant="outline" onClick={() => setCreating(true)}><Plus size={15} />申请开票</Button>}</div>}
      <div className="invoice-pagination"><span>共 {data.total} 条 · 第 {page} 页</span><div><Button variant="outline" size="sm" disabled={page === 1 || loading} onClick={() => setPage(p => p - 1)}>上一页</Button><Button variant="outline" size="sm" disabled={page * 20 >= data.total || loading} onClick={() => setPage(p => p + 1)}>下一页</Button></div></div></>}
    </section>{creating && <CreateInvoice onClose={() => setCreating(false)} onCreated={id => { setCreating(false); setDetail(id); refresh() }} />}{detail && <InvoiceDetails key={detail} id={detail} admin={admin} onClose={() => setDetail(undefined)} onUpdated={refresh} />}
  </div>
}
