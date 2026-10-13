import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { api, errorMessage } from '@/lib/api'
import { Feedback } from './shared'
import { money, offerName, toCents, usePartnerData } from './model'

type Plan = { id: string; product_id: string; name: string; cost_cents: number; minimum_cost_cents: number; version: number }
type Settings = { plans: Plan[] }
const endpoint = '/api/partner/child-default-prices'

function DefaultForm({ settings, disabled, total, onEditing, onSaved }: { settings: Settings; disabled: boolean; total: number; onEditing: () => void; onSaved: (message: string) => void }) {
  const [values, setValues] = useState(() => Object.fromEntries(settings.plans.map(p => [p.id, (p.cost_cents / 100).toFixed(2)])))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [confirming, setConfirming] = useState(false)
  const changed = settings.plans.some(p => toCents(values[p.id]) !== p.cost_cents)
  const invalid = (p: Plan) => !Number.isSafeInteger(toCents(values[p.id])) || toCents(values[p.id]) < p.minimum_cost_cents || toCents(values[p.id]) < 1 || toCents(values[p.id]) > 10000000
  const blocked = busy || disabled || settings.plans.length === 0 || settings.plans.some(invalid)
  async function save(apply: boolean) {
    if (blocked || (apply && changed)) return
    setBusy(true); setError(''); onEditing()
    try {
      const result = await api<{ children: number }>(endpoint + (apply ? '/apply' : ''), { prices: settings.plans.map(p => ({ plan: p.id, costCents: toCents(values[p.id]), version: p.version })) })
      setConfirming(false)
      onSaved(apply ? `默认拿货价已应用到 ${result.children} 位直属二级代理` : '默认拿货价已保存，新开通二级将自动使用')
    } catch (e) { setError(errorMessage(e)) } finally { setBusy(false) }
  }
  return <>
    <form className="child-default-form" onSubmit={e => { e.preventDefault(); void save(false) }}>
      <div className="child-default-grid">{settings.plans.map(p => <Field key={p.id} data-invalid={invalid(p)}>
        <FieldLabel htmlFor={'child-default-' + p.id}>{offerName(p)}</FieldLabel>
        <div className="child-default-own-cost"><span>我的成本价</span><strong>{money(p.minimum_cost_cents)}</strong></div>
        <Input id={'child-default-' + p.id} value={values[p.id]} inputMode="decimal" autoComplete="off" maxLength={9} required disabled={busy || disabled} aria-invalid={invalid(p)} aria-describedby={'child-default-help-' + p.id} onChange={e => { setValues(v => ({ ...v, [p.id]: e.target.value })); setError(''); onEditing() }} />
        <FieldDescription id={'child-default-help-' + p.id}>二级默认拿货价（元），不得低于我的成本价{invalid(p) && '；请输入有效金额，最多两位小数'}</FieldDescription>
      </Field>)}</div>
      {!confirming && <Feedback error={error} />}
      <div className="child-actions"><Button type="submit" disabled={blocked || !changed}>{busy ? '处理中…' : '保存默认拿货价'}</Button><Button type="button" variant="outline" disabled={blocked || changed || total === 0} onClick={() => { setError(''); onEditing(); setConfirming(true) }}>一键应用到全部二级</Button></div>
      {changed && <p className="child-help">请先保存默认拿货价，再应用到已有二级代理。</p>}
    </form>
    <Dialog open={confirming} onOpenChange={open => { if (!busy) setConfirming(open) }}><DialogContent showCloseButton={!busy}>
      <DialogHeader><DialogTitle>应用到全部直属二级</DialogTitle><DialogDescription>将按已保存的默认配置更新全部 {total} 位直属二级代理（含停用账号）的拿货价。二级商店售价、套餐开关及历史订单保持原样。若任一商店售价或优惠后价格低于新拿货价，整次操作取消。</DialogDescription></DialogHeader>
      <Feedback error={error} />
      <DialogFooter><Button variant="outline" disabled={busy} onClick={() => setConfirming(false)}>取消</Button><Button disabled={blocked || changed} onClick={() => void save(true)}>{busy ? '应用中…' : '确认应用'}</Button></DialogFooter>
    </DialogContent></Dialog>
  </>
}

export function ChildDefaultPrices({ revision, total, onSaved }: { revision: number; total: number; onSaved: () => void }) {
  const [localRevision, setLocalRevision] = useState(0)
  const [message, setMessage] = useState('')
  const { data, error, loading } = usePartnerData<Settings>(endpoint, revision + localRevision)
  return <section className="child-list-panel child-default-panel">
    <div className="child-section-heading"><h3>二级代理默认拿货价</h3><p>按套餐设置新二级的初始拿货价与售价；保存后可一键应用到全部直属二级。</p></div>
    <Feedback error={error} loading={loading} data={data} />
    <p className="child-status" role="status">{message}</p>
    {data && <DefaultForm key={data.plans.map(p => p.id + ':' + p.version).join(',')} settings={data} disabled={loading || Boolean(error)} total={total} onEditing={() => setMessage('')} onSaved={value => { setMessage(value); setLocalRevision(v => v + 1); onSaved() }} />}
  </section>
}
