import { StoresWorkspace } from '@/partner/StoresWorkspace'
import { useEffect, useState } from 'react'
import { AlertCircle, AlertTriangle, ArrowUpRight, Check, CheckCircle2, CircleAlert, Clock, Plus, ReceiptText, RefreshCw, Search, Users, Wallet } from 'lucide-react'
import { Checkbox } from '@base-ui/react/checkbox'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { CopyButton } from '@/components/copy-button'
import { api, errorMessage } from '@/lib/api'
import { CopyLink, Feedback, Pagination, Pricing } from '@/partner/shared'
import { date, money, payoutLabel, usePartnerData, type Dashboard, type Partner, type Payout } from '@/partner/model'
import { PartnerOrders } from '@/partner/Partner'
import PartnerDefaultPrices from './PartnerDefaultPrices'
import { RecruitPermission, ParentRelationship } from './RecruitPermission'
import { partnerStorefrontPath } from '../../shared/partner-link.ts'
import '@/partner/partner.css'

function PartnerCreator({ onClose, onSaved }: { onClose: () => void; onSaved: (id?: string, message?: string, invitationSent?: boolean) => void }) {
  const [email, setEmail] = useState(''); const [busy, setBusy] = useState(false); const [error, setError] = useState('')
  async function save() {
    if (busy || !email.trim()) return
    setBusy(true); setError('')
    try {
      const result = await api<{ id: string; message: string; invitationSent: boolean }>('/api/admin/partners', { email })
      onSaved(result.id, result.message, result.invitationSent); onClose()
    } catch (e) { setError(errorMessage(e)) } finally { setBusy(false) }
  }
  return <Dialog open onOpenChange={open => { if (!open && !busy) onClose() }}><DialogContent className="motion-dialog partner-dialog"><DialogHeader><DialogTitle>开通代理</DialogTitle><DialogDescription>自动生成专属链接，并发送工作台邀请邮件。代理登录时自行获取验证码。</DialogDescription></DialogHeader><form className="partner-form" onSubmit={e => { e.preventDefault(); void save() }}><Field><FieldLabel htmlFor="agent-email">登录邮箱</FieldLabel><Input id="agent-email" type="email" autoComplete="email" value={email} onChange={e => setEmail(e.target.value)} maxLength={254} required disabled={busy} placeholder="输入代理邮箱" /></Field><Feedback error={error} /><Button type="submit" disabled={busy || !email.trim()}>{busy ? '开通并发送邮件中…' : '开通并发送邀请邮件'}</Button></form></DialogContent></Dialog>
}
function PayoutHandler({ payout, onClose, onSaved }: { payout: Payout; onClose: () => void; onSaved: () => void }) {
  const [page, setPage] = useState(1)
  const [revision, setRevision] = useState(0)
  const details = usePartnerData<{ payout: Payout; items: { order_id: string; profit_cents: number; plan_name: string }[]; deductions: { order_id: string | null; domain_hostname?: string | null; amount_cents: number }[]; total: number }>('/api/admin/partner-payouts/' + payout.id + '?page=' + page, revision)
  const [reject, setReject] = useState(false)
  const [note, setNote] = useState('')
  const [reference, setReference] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const current = details.data?.payout
  const action = reject ? 'rejected' : current?.reviewed_at ? 'paid' : 'approve'

  async function save() {
    if (busy || !current || details.loading) return
    setBusy(true)
    setError('')
    try {
      await api('/api/admin/partner-payouts/' + payout.id, { action, note, reference, version: current.version })
      onSaved()
      setRevision(v => v + 1)
      setNote('')
      setReference('')
      setReject(false)
    } catch (e) {
      setError(errorMessage(e))
      setRevision(v => v + 1)
    } finally {
      setBusy(false)
    }
  }

  const isPending = current?.status === 'pending'
  const isPaid = current?.status === 'paid'
  const isRejected = current?.status === 'rejected'
  const isReviewed = Boolean(current?.reviewed_at)

  return (
    <Dialog open onOpenChange={v => { if (!v && !busy) onClose() }}>
      <DialogContent className="motion-dialog partner-dialog partner-order-dialog partner-payout-dialog" showCloseButton={!busy}>
        <DialogHeader>
          <DialogTitle>提现审核与转账</DialogTitle>
          <DialogDescription>
            {payout.partner_name || current?.partner_name || '代理商店'} · 审核后手动转账，最后登记流水。
          </DialogDescription>
        </DialogHeader>

        <Feedback error={details.error} loading={details.loading} data={details.data} shape="compact" />

        {current && (
          <>
            <div className="partner-order-ticket">
              <div className="partner-order-ticket-id">
                <div className="partner-order-ticket-meta">
                  <span className="partner-order-ticket-label">申请单号</span>
                  <Badge
                    variant={isRejected ? 'destructive' : isPaid ? 'secondary' : 'outline'}
                    className="partner-order-mode-badge"
                    data-state={isPaid ? 'success' : undefined}
                  >
                    {payoutLabel(current.status, current.reviewed_at)}
                  </Badge>
                  <span className="partner-order-ticket-time">
                    <Clock size={12} />
                    {date(current.created_at)}
                  </span>
                </div>
                <strong className="partner-order-id-value" title={current.id}>
                  {current.id}
                </strong>
              </div>
              <div className="partner-order-ticket-actions">
                <CopyButton value={current.id} label="复制单号" size="sm" variant="ghost" />
              </div>
            </div>

            <div className="partner-order-finances">
              <div className="partner-order-finances-head">
                <span>转账金额核算</span>
                <span className="text-xs text-muted-foreground">{payout.partner_name || '代理商店'}</span>
              </div>
              <div className="partner-order-finances-grid">
                <div className="partner-finance-item">
                  <span className="partner-finance-label">订单累计收益</span>
                  <strong className="partner-finance-value">{money(current.gross_cents)}</strong>
                  <small className="partner-finance-hint">未结算佣金</small>
                </div>
                <span className="partner-finance-sign" aria-hidden="true">−</span>
                <div className="partner-finance-item">
                  <span className="partner-finance-label">退款及域名费用抵扣</span>
                  <strong className="partner-finance-value">{money(current.deduction_cents)}</strong>
                  <small className="partner-finance-hint">售后冲抵</small>
                </div>
                <span className="partner-finance-sign" aria-hidden="true">=</span>
                <div className="partner-finance-item highlight">
                  <span className="partner-finance-label">实际打款金额</span>
                  <strong className="partner-finance-value partner-profit">
                    {money(current.amount_cents)}
                  </strong>
                  <small className="partner-finance-hint">需向代理付款</small>
                </div>
              </div>
              {current.deduction_cents > 0 && (
                <p className="partner-finance-notice">
                  已抵扣历史退款 {money(current.deduction_cents)} · 实际打款金额已核减
                </p>
              )}
            </div>

            <div className="partner-payout-payee-card">
              <div className="partner-payout-payee-left">
                <div className="partner-payout-payee-icon" aria-hidden="true">
                  <Wallet size={18} />
                </div>
                <div className="partner-payout-payee-info">
                  <span className="partner-payout-payee-label">支付宝收款账户</span>
                  <strong className="partner-payout-payee-account">{current.recipient}</strong>
                </div>
              </div>
              <div className="partner-payout-payee-actions">
                <CopyButton value={current.recipient} label="复制收款账号" size="sm" variant="outline" />
              </div>
            </div>

            <div className="partner-payout-details-card">
              <div className="partner-payout-details-header">
                <div className="partner-payout-details-title">
                  <ReceiptText size={15} />
                  <span>本次收益明细</span>
                </div>
                <Badge variant="secondary" className="partner-payout-count-badge">
                  共 {details.data!.total} 笔订单
                </Badge>
              </div>

              <div className="partner-payout-details-list">
                {details.data!.items.map(item => (
                  <div key={item.order_id} className="partner-payout-item-row">
                    <div className="partner-payout-item-left">
                      <Badge variant="outline" className="partner-payout-item-plan">
                        {item.plan_name}
                      </Badge>
                      <span className="partner-payout-item-order" title={item.order_id}>
                        {item.order_id}
                      </span>
                      <CopyButton value={item.order_id} label="复制单号" iconOnly size="icon-sm" variant="ghost" />
                    </div>
                    <strong className="partner-payout-item-amount">
                      +{money(item.profit_cents)}
                    </strong>
                  </div>
                ))}
              </div>

              <Pagination page={page} total={details.data!.total} onPage={setPage} />

              {details.data!.deductions.length > 0 && (
                <div className="partner-payout-deductions-box">
                  <div className="partner-payout-details-header partner-deduction-header">
                    <div className="partner-payout-details-title">
                      <AlertTriangle size={15} className="text-destructive" />
                      <span>退款及域名费用抵扣</span>
                    </div>
                    <Badge variant="destructive" className="partner-payout-count-badge">
                      共 {details.data!.deductions.length} 笔
                    </Badge>
                  </div>
                  <div className="partner-payout-details-list">
                    {details.data!.deductions.map((d, i) => (
                      <div key={i} className="partner-payout-item-row">
                        <div className="partner-payout-item-left">
                          <span className="partner-payout-item-order" title={d.domain_hostname ? '域名续费 · ' + d.domain_hostname : (d.order_id ?? '')}>
                            {d.domain_hostname ? '域名续费 · ' + d.domain_hostname : (d.order_id ?? '')}
                          </span>
                          <CopyButton value={d.domain_hostname ? '域名续费 · ' + d.domain_hostname : (d.order_id ?? '')} label="复制单号" iconOnly size="icon-sm" variant="ghost" />
                        </div>
                        <strong className="partner-payout-deduction-amount">
                          −{money(d.amount_cents)}
                        </strong>
                      </div>
                    ))}
                  </div>
                </div>
              )}
            </div>

            {!isPending ? (
              <div className={`partner-payout-result-card ${current.status}`}>
                <div className="partner-payout-result-head">
                  <div className="partner-payout-result-title">
                    {isPaid ? (
                      <>
                        <CheckCircle2 size={16} className="partner-profit" />
                        <strong>已打款流水记录</strong>
                      </>
                    ) : (
                      <>
                        <AlertCircle size={16} className="text-destructive" />
                        <strong>申请退回记录</strong>
                      </>
                    )}
                  </div>
                  <Badge
                    variant={isPaid ? 'secondary' : 'destructive'}
                    data-state={isPaid ? 'success' : undefined}
                  >
                    {payoutLabel(current.status, current.reviewed_at)}
                  </Badge>
                </div>
                <div className="partner-payout-result-body">
                  {current.reference && (
                    <div className="partner-payout-result-row">
                      <span className="partner-payout-field-label">支付宝转账流水号</span>
                      <div className="partner-payout-ref-box">
                        <strong className="partner-payout-ref-val">{current.reference}</strong>
                        <CopyButton value={current.reference} label="复制流水号" size="sm" variant="ghost" />
                      </div>
                    </div>
                  )}
                  {current.note && (
                    <div className="partner-payout-result-row">
                      <span className="partner-payout-field-label">
                        {isRejected ? '退回原因' : '处理说明'}
                      </span>
                      <p className="partner-payout-note-val">{current.note}</p>
                    </div>
                  )}
                </div>
              </div>
            ) : (
              <form className="partner-payout-form" onSubmit={e => { e.preventDefault(); void save() }}>
                <div className="partner-action-picker">
                  <span className="partner-action-picker-label">处理方式</span>
                  <div className="partner-action-options">
                    <button
                      type="button"
                      className={`partner-action-option ${!reject ? 'active' : ''}`}
                      aria-pressed={!reject}
                      onClick={() => setReject(false)}
                      disabled={busy}
                    >
                      <div className="partner-action-option-header">
                        <div className="partner-action-radio">
                          {!reject && <div className="partner-action-radio-dot" />}
                        </div>
                        <strong>{isReviewed ? '登记转账完成' : '审核通过'}</strong>
                      </div>
                      <p>{isReviewed ? '已在支付宝完成打款，登记流水号' : '核验金额与收款信息，等待手动转账'}</p>
                    </button>
                    <button
                      type="button"
                      className={`partner-action-option ${reject ? 'active destructive' : ''}`}
                      aria-pressed={reject}
                      onClick={() => setReject(true)}
                      disabled={busy}
                    >
                      <div className="partner-action-option-header">
                        <div className="partner-action-radio">
                          {reject && <div className="partner-action-radio-dot" />}
                        </div>
                        <strong>退回申请</strong>
                      </div>
                      <p>释放未冻结佣金，代理可修正后重新申请</p>
                    </button>
                  </div>
                </div>

                <Alert variant={reject ? 'destructive' : 'default'} className="partner-payout-action-alert">
                  {reject ? <AlertCircle className="size-4" /> : <CircleAlert className="size-4" />}
                  <AlertDescription>
                    {action === 'approve'
                      ? '确认收益和收款信息后审核，资金继续保留，等待你手动在支付宝转账。'
                      : action === 'paid'
                      ? '仅在支付宝转账成功后登记。本操作不会自动打款，需登记真实流水号。'
                      : isReviewed
                      ? '请先确认没有实际转账。若已转账成功，必须登记流水，不能退回。'
                      : '退回后释放未冻结收益，代理可修正信息后重新申请。'}
                  </AlertDescription>
                </Alert>

                <FieldGroup className="partner-payout-form-fields">
                  {action === 'paid' && (
                    <Field>
                      <FieldLabel htmlFor="payout-reference">支付宝转账流水号</FieldLabel>
                      <Input
                        id="payout-reference"
                        value={reference}
                        onChange={e => setReference(e.target.value)}
                        required
                        maxLength={100}
                        disabled={busy}
                        placeholder="例如：DEMO_TRANSFER_REFERENCE"
                        className="font-mono"
                      />
                      <FieldDescription>请从支付宝账单详情复制真实交易流水号或订单号。</FieldDescription>
                    </Field>
                  )}
                  <Field>
                    <FieldLabel htmlFor="payout-note">{reject ? '退回原因' : '处理说明'}</FieldLabel>
                    <Input
                      id="payout-note"
                      value={note}
                      onChange={e => setNote(e.target.value)}
                      required
                      maxLength={300}
                      disabled={busy}
                      placeholder={reject ? '说明退回原因，便于代理查看并修正' : '例如：已核对无误或转账备注'}
                    />
                  </Field>
                </FieldGroup>

                <Feedback error={error} />

                <Button
                  type="submit"
                  variant={reject ? 'destructive' : 'default'}
                  disabled={busy || details.loading || !note.trim() || (action === 'paid' && !reference.trim())}
                  className="partner-payout-submit"
                >
                  {busy
                    ? '保存中…'
                    : action === 'approve'
                    ? '审核通过，等待转账'
                    : action === 'paid'
                    ? '确认已转账并登记流水'
                    : '确认退回提现申请'}
                </Button>
              </form>
            )}

            <DialogFooter className="partner-order-dialog-footer partner-payout-dialog-footer">
              <Button
                variant="ghost"
                size="sm"
                disabled={busy || details.loading}
                onClick={() => setRevision(v => v + 1)}
                className="partner-payout-refresh-btn"
              >
                <RefreshCw size={14} className={details.loading ? 'animate-spin' : undefined} />
                刷新详情
              </Button>
              <Button variant="outline" size="sm" onClick={onClose} disabled={busy}>
                关闭
              </Button>
            </DialogFooter>
          </>
        )}
      </DialogContent>
    </Dialog>
  )
}
function Payouts({ revision, onSaved }: { revision: number; onSaved: () => void }) {
  const [page, setPage] = useState(1); const [edit, setEdit] = useState<Payout>()
  const { data, error, loading } = usePartnerData<{ rows: Payout[]; total: number }>('/api/admin/partner-payouts?page=' + page, revision)
  return <><section className="admin-panel"><div className="admin-panel-title"><h2>代理结算</h2><span>待处理申请优先显示</span></div><Feedback error={error} loading={loading} data={data} />{data && <>{data.rows.length ? <div className="admin-table-scroll"><table className="admin-table"><thead><tr><th>代理 / 时间</th><th>金额</th><th>状态</th><th>说明 / 流水</th><th>操作</th></tr></thead><tbody>{data.rows.map(p => <tr key={p.id}><td>{p.partner_name}<small>{date(p.created_at)}</small></td><td>{money(p.amount_cents)}</td><td>{payoutLabel(p.status, p.reviewed_at)}</td><td>{p.note || '—'}<small>{p.reference}</small></td><td>{p.status === 'pending' ? <Button variant="outline" onClick={() => setEdit(p)}>处理申请</Button> : <Button variant="ghost" onClick={() => setEdit(p)}>查看明细</Button>}</td></tr>)}</tbody></table></div> : <div className="admin-empty"><h3>暂无结算申请</h3><p>代理提交的正式订单收益结算会显示在这里。</p></div>}<Pagination page={page} total={data.total} onPage={setPage} /></>}</section>{edit && <PayoutHandler payout={edit} onClose={() => setEdit(undefined)} onSaved={onSaved} />}</>
}
function PartnerInvitation({ id, active, mailReady, notice }: { id: string; active: boolean; mailReady: boolean; notice: string }) {
  const [busy, setBusy] = useState(false); const [message, setMessage] = useState(''); const [error, setError] = useState(''); const [cooldown, setCooldown] = useState(0)
  useEffect(() => { if (cooldown <= 0) return; const timer = setTimeout(() => setCooldown(value => value - 1), 1000); return () => clearTimeout(timer) }, [cooldown])
  async function resend() {
    if (busy || cooldown) return
    setBusy(true); setError(''); setMessage('')
    try { await api('/api/admin/partners/' + id + '/invitation', {}); setMessage('邀请邮件已发送。代理可通过工作台入口自行获取登录验证码。'); setCooldown(60) } catch (e) { setError(errorMessage(e)) } finally { setBusy(false) }
  }
  return <div className="partner-invitation"><p role="status" className="partner-notice">{message || notice || '代理从 /join 使用邮箱和邮件验证码登录，客户售价由代理自行修改。'}</p><Feedback error={error} /><Button variant="outline" disabled={busy || !active || !mailReady || cooldown > 0} onClick={() => void resend()}>{busy ? '发送中…' : cooldown ? `${cooldown} 秒后可重发` : '重发邀请邮件'}</Button></div>
}
function PartnerSettings({ partner, loading, onSaved, onBusy }: { partner: Partner; loading: boolean; onSaved: () => void; onBusy: (busy: boolean) => void }) {
  const [name, setName] = useState(partner.name); const [email, setEmail] = useState(partner.email); const [active, setActive] = useState(Boolean(partner.active)); const [busy, setBusy] = useState(false); const [error, setError] = useState(''); const [message, setMessage] = useState('')
  async function save() {
    if (busy || loading || !name.trim() || !email.trim()) return
    setBusy(true); onBusy(true); setError(''); setMessage('')
    try { await api('/api/admin/partners/' + partner.id, { name, email, active, version: partner.version }); setMessage('设置已保存'); onSaved() } catch (e) { setError(errorMessage(e)) } finally { setBusy(false); onBusy(false) }
  }
  return <form onSubmit={e => { e.preventDefault(); void save() }}><FieldGroup className="partner-settings-fields"><Field><FieldLabel htmlFor="edit-partner-name">商店名称</FieldLabel><Input id="edit-partner-name" value={name} onChange={e => setName(e.target.value)} required maxLength={40} disabled={busy} /></Field><Field><FieldLabel htmlFor="edit-partner-email">登录邮箱</FieldLabel><Input id="edit-partner-email" type="email" value={email} onChange={e => setEmail(e.target.value)} required maxLength={254} disabled={busy} /></Field><label className="partner-check"><Checkbox.Root checked={active} onCheckedChange={setActive} disabled={busy}><Checkbox.Indicator>✓</Checkbox.Indicator></Checkbox.Root>启用代理与专属商店</label></FieldGroup><Feedback error={error} /><div className="partner-settings-actions"><Button type="submit" disabled={busy || loading || !name.trim() || !email.trim()}>{busy ? '保存中…' : '保存设置'}</Button><span role="status">{message}</span></div></form>
}
type TelegramApplication = { user_id: number; group_id: number; wechat: string; qq: string; email: string; created_at: number; mail_status: string }
function TelegramApplicationInfo({ application }: { application: TelegramApplication | null }) {
  if (!application) return null
  return <section className="partner-management-link"><h3>Telegram 申请资料</h3><p>微信：{application.wechat} · QQ：{application.qq}</p><p>申请邮箱：{application.email}</p><p>Telegram 用户 ID：{application.user_id} · 群 ID：{application.group_id}</p><p>申请时间：{date(application.created_at)} · 邀请邮件：{({ pending: '排队发送', sent: '已发送', failed: '未确认发送，请核实后重发' })[application.mail_status] ?? application.mail_status}</p><p className="partner-notice">微信和 QQ 自动设为初始商店联系方式，代理可在工作台修改；这里保留申请时提交的资料。</p></section>
}
function PartnerManagement({ id, revision, onClose, onSaved }: { id: string; revision: number; onClose: () => void; onSaved: () => void }) {
  const { data, error, loading } = usePartnerData<Dashboard & { telegramApplication: TelegramApplication | null; audit: { id: string; note: string; created_at: number }[] }>('/api/admin/partners/' + id, revision)
  const [tab, setTab] = useState('settings'); const [busy, setBusy] = useState(false)
  return <Dialog open onOpenChange={open => { if (!open && !busy) onClose() }}><DialogContent className="motion-dialog partner-management-dialog hierarchy-admin-dialog" showCloseButton={!busy}><DialogHeader><DialogTitle>编辑代理</DialogTitle><DialogDescription>{data ? data.partner.email : '正在读取代理信息'}</DialogDescription></DialogHeader><Feedback error={error} loading={loading} data={data} shape="compact" />{data && <Tabs value={tab} onValueChange={value => setTab(String(value))} className="partner-tabs"><TabsList variant="line"><TabsTrigger value="settings">基本信息</TabsTrigger><TabsTrigger value="prices">供货价</TabsTrigger><TabsTrigger value="stores">商店与域名</TabsTrigger><TabsTrigger value="records">订单与记录</TabsTrigger><TabsTrigger value="permissions">层级与权限</TabsTrigger></TabsList><TabsContent value="settings"><TelegramApplicationInfo application={data.telegramApplication} /><PartnerSettings partner={data.partner} loading={loading} onSaved={onSaved} onBusy={setBusy} /><section className="partner-management-link"><h3>专属推广链接</h3><CopyLink url={data.partner.storefront_url || (data.partner.referralOrigin || location.origin) + partnerStorefrontPath(data.partner.slug)} /><PartnerInvitation id={id} active={Boolean(data.partner.active)} mailReady={data.mailReady} notice="邀请邮件提供工作台入口，代理登录时自行获取验证码。" /></section></TabsContent><TabsContent value="permissions"><div className="hierarchy-settings"><RecruitPermission partner={data.partner} onSaved={onSaved} disabled={loading || busy} /><ParentRelationship key={data.partner.version} partner={data.partner} onSaved={onSaved} disabled={loading || busy} /></div></TabsContent><TabsContent value="stores"><StoresWorkspace prefix={'/api/admin/partners/'+id} readOnly adminManagement onSaved={onSaved}/></TabsContent><TabsContent value="prices"><p className="partner-notice">客户售价由代理自己修改，供货价调整仅影响新订单。</p><Pricing plans={data.plans} cost endpoint={p => '/api/admin/partners/' + id + '/prices/' + p.id} onSaved={onSaved} /></TabsContent><TabsContent value="records"><PartnerOrders path={'/api/admin/partners/' + id + '/orders'} revision={revision} /><div className="admin-audit-list">{data.audit.map(a => <div key={a.id}><span>{a.note}</span><time>{date(a.created_at)}</time></div>)}</div></TabsContent></Tabs>}<a className="partner-management-preview" href={'/join?preview=' + id} target="_blank" rel="noreferrer">只读预览代理工作台 <ArrowUpRight size={14} /></a></DialogContent></Dialog>
}
function PayoutCooldownReset({ partner, onClose, onSaved }: { partner?: Partner; onClose: () => void; onSaved: (message: string) => void }) {
  const [key] = useState(() => crypto.randomUUID())
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  async function reset() {
    if (busy) return
    setBusy(true); setError('')
    try {
      const path = partner ? `/api/admin/partners/${partner.id}/payout-cooldown/reset` : '/api/admin/partners/payout-cooldown/reset'
      const result = await api<{ resetPartners: number; resetPayouts: number }>(path, { key, note: '后台手动重置' + (partner ? '该代理' : '所有代理') + '的提现冷却' })
      onSaved(result.resetPayouts ? (partner ? `${partner.email} 的提现冷却已重置。` : `已重置 ${result.resetPartners} 位代理的提现冷却。`) : '没有需要重置的提现冷却。')
      onClose()
    } catch (e) { setError(errorMessage(e)) } finally { setBusy(false) }
  }
  return <Dialog open onOpenChange={open => { if (!open && !busy) onClose() }}>
    <DialogContent className="motion-dialog partner-dialog" showCloseButton={!busy}>
      <DialogHeader>
        <DialogTitle>{partner ? '重置提现冷却' : '重置所有代理的提现冷却'}</DialogTitle>
        <DialogDescription>{partner ? partner.email : '作用于全部代理，包括未在当前列表显示的代理。'}</DialogDescription>
      </DialogHeader>
      <p className="partner-notice">重置后，符合提现条件的代理可重新申请。新的申请仍需间隔 24 小时；提现记录和已处理金额保留，待处理申请仍需先处理。</p>
      <Feedback error={error} />
      <DialogFooter>
        <Button variant="outline" disabled={busy} onClick={onClose}>取消</Button>
        <Button disabled={busy} onClick={() => void reset()}><RefreshCw size={15} />{busy ? '重置中…' : '确认重置'}</Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
}
export default function PartnersPage({ revision: externalRevision }: { revision: number }) {
  const [notice, setNotice] = useState<{ message: string; failed: boolean }>(); const [revision, setRevision] = useState(0); const [tab, setTab] = useState('partners'); const [payoutsVisited, setPayoutsVisited] = useState(false); const [create, setCreate] = useState(false); const [selected, setSelected] = useState<string>(); const [page, setPage] = useState(1); const [search, setSearch] = useState(''); const [q, setQ] = useState('')
  const reload = () => setRevision(v => v + 1); const version = externalRevision + revision
  const [cooldownReset, setCooldownReset] = useState<{ partner?: Partner }>()
  const { data, error, loading } = usePartnerData<{ rows: (Partner & { wechat: string; qq: string })[]; total: number; mailReady: boolean }>(`/api/admin/partners?page=${page}&q=${encodeURIComponent(q)}`, version)
  return <>
    <Tabs value={tab} onValueChange={v => { setTab(String(v)); if (v === 'payouts') setPayoutsVisited(true) }} className="partner-tabs">
      <TabsList variant="line">
        <TabsTrigger value="partners">代理列表</TabsTrigger>
        <TabsTrigger value="payouts">结算申请</TabsTrigger>
        <TabsTrigger value="defaults">默认拿货价</TabsTrigger>
      </TabsList>
      <TabsContent value="partners" className="workspace-enter">
        {notice && <Alert role={notice.failed ? 'alert' : 'status'} variant={notice.failed ? 'destructive' : 'default'} className="partner-list-result">
          {notice.failed ? <CircleAlert /> : <Check className="text-primary" />}
          <AlertDescription>{notice.message}</AlertDescription>
        </Alert>}
        {data && !data.mailReady && <p className="partner-notice">邮件服务待启用 · 配置完成后可开通代理并发送邀请邮件。</p>}
        <section className="admin-panel">
          <div className="admin-panel-title">
            <form className="partner-list-search" onSubmit={e => { e.preventDefault(); setQ(search); setPage(1) }}>
              <Input aria-label="搜索代理" placeholder="名称或邮箱" value={search} onChange={e => setSearch(e.target.value)} />
              <Button variant="ghost" type="submit" aria-label="查询代理"><Search size={16} /></Button>
            </form>
            <div className="partner-list-actions">
              <Button variant="outline" disabled={!data || loading} onClick={() => setCooldownReset({})}><RefreshCw size={15} />重置全部提现冷却</Button>
              <Button disabled={!data?.mailReady} onClick={() => setCreate(true)}><Plus size={15} />开通代理</Button>
            </div>
          </div>
          <Feedback error={error} loading={loading} data={data} />
          {data && <>
            {data.rows.length ? <div className="admin-table-scroll">
              <table className="admin-table">
                <thead><tr><th>代理</th><th>登录邮箱</th><th>微信</th><th>QQ</th><th>状态</th><th>操作</th></tr></thead>
                <tbody>{data.rows.map(p => <tr key={p.id}>
                  <td><strong>{p.name}</strong><small>{p.parent_id ? '二级代理' : '一级代理'}{p.can_recruit ? ' · 可发展二级' : ''}</small></td><td>{p.email}</td>
                  <td className="partner-list-contact">{p.wechat || '—'}</td><td className="partner-list-contact">{p.qq || '—'}</td>
                  <td><span className="admin-mode">{p.active ? '已启用' : '已停用'}</span></td>
                  <td><div className="partner-row-actions">
                    <Button variant="ghost" onClick={() => setSelected(p.id)}>编辑</Button>
                    <Button variant="outline" disabled={loading} aria-label={`重置 ${p.email} 的提现冷却`} onClick={() => setCooldownReset({ partner: p })}>重置提现冷却</Button>
                  </div></td>
                </tr>)}</tbody>
              </table>
            </div> : <div className="admin-empty">
              <Users size={28} /><h3>{q ? '没有找到代理' : '开通你的第一位代理'}</h3>
              <p>{q ? '试试其他名称或邮箱。' : '填写邮箱，系统生成专属链接并发送邀请邮件。'}</p>
              {!q && <Button disabled={!data?.mailReady} onClick={() => setCreate(true)}>开通代理</Button>}
            </div>}
            <Pagination page={page} total={data.total} onPage={setPage} />
          </>}
        </section>
      </TabsContent>
      <TabsContent value="defaults" className="workspace-enter"><PartnerDefaultPrices revision={version} /></TabsContent>
      <TabsContent value="payouts" keepMounted={payoutsVisited} className="workspace-enter"><Payouts revision={version} onSaved={reload} /></TabsContent>
    </Tabs>
    {create && <PartnerCreator onClose={() => setCreate(false)} onSaved={(_id, message, invitationSent) => { reload(); setNotice({ message: message ?? '代理已开通', failed: invitationSent === false }); setPage(1); setSearch(''); setQ('') }} />}
    {selected && <PartnerManagement key={selected} id={selected} revision={version} onClose={() => setSelected(undefined)} onSaved={reload} />}
    {cooldownReset && <PayoutCooldownReset partner={cooldownReset.partner} onClose={() => setCooldownReset(undefined)} onSaved={message => { reload(); setNotice({ message, failed: false }) }} />}
  </>
}
