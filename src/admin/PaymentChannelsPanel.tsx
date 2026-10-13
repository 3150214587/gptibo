import { useState } from 'react'
import { Check, ChevronDown, CreditCard, RotateCcw } from 'lucide-react'
import { PaymentLogo } from '@/components/payment-logo'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Feedback } from '@/components/workspace-loading'
import { api, errorMessage } from '@/lib/api'
import { useResource } from '@/lib/use-resource'
import { parseFeePercent, paymentFeeDraft } from './payment-settings-draft'

type Settings = { feeRatesBps: Record<string, number>; provider: string; enabledProviders: string[]; version: number; checkout: boolean; channels: { id: string; name: string; configured: boolean }[] }

export function PaymentChannelsPanel({ revision }: { revision: number }) {
  const [refresh, setRefresh] = useState(0)
  const { data, error, loading } = useResource<Settings>('/api/admin/payments/settings', revision + refresh)
  const [selected, setSelected] = useState<string[] | null>(null)
  const [rates, setRates] = useState<Record<string, string> | null>(null)
  const rateValues = rates ?? Object.fromEntries((data?.channels ?? []).map(c => [c.id, String((data?.feeRatesBps?.[c.id] ?? 0) / 100)]))
  const [busy, setBusy] = useState(false)
  const [saveError, setSaveError] = useState('')
  const [message, setMessage] = useState('')
  const choices = selected ?? data?.enabledProviders ?? []
  const { feeRatesBps: rateBps, invalidChannels } = paymentFeeDraft((data?.channels ?? []).map(c => c.id), choices, data?.feeRatesBps ?? {}, rateValues)
  const ratesValid = invalidChannels.length === 0
  const alipay = choices.find(id => !id.endsWith('-wxpay')) ?? ''
  const wechat = choices.find(id => id.endsWith('-wxpay')) ?? ''
  const changed = Boolean(data && (JSON.stringify([...choices].sort()) !== JSON.stringify([...data.enabledProviders].sort()) || data.channels.some(c => (rateBps[c.id] ?? 0) !== (data.feeRatesBps?.[c.id] ?? 0))))
  function choose(method: 'alipay' | 'wxpay', value: string) {
    const other = choices.filter(id => method === 'alipay' ? id.endsWith('-wxpay') : !id.endsWith('-wxpay'))
    setSelected([...other, ...(value ? [value] : [])]); setMessage(''); setSaveError('')
  }
  async function save() {
    if (!data || busy || loading || !changed || !ratesValid || choices.length === 0 || choices.some(id => !data.channels.find(c => c.id === id)?.configured)) return
    setBusy(true); setSaveError(''); setMessage('')
    try {
      await api('/api/admin/payments/settings', { providers: choices, version: data.version, feeRatesBps: rateBps })
      setSelected(null); setRates(null); setRefresh(v => v + 1); setMessage('收款设置已保存')
    } catch (e) { setSaveError(errorMessage(e)); setRefresh(v => v + 1) }
    finally { setBusy(false) }
  }
  function feeInput(id: string, name: string) {
    const valid = parseFeePercent(rateValues[id] ?? '0') !== null
    return <div className="payment-rate-field"><label htmlFor={`fee-${id}`}>{name}手续费</label><div className="payment-rate-control"><Input id={`fee-${id}`} type="text" inputMode="decimal" autoComplete="off" value={rateValues[id] ?? '0'} disabled={busy || loading} aria-invalid={!valid} aria-describedby={!valid ? 'payment-rate-error' : undefined} onChange={e => { setRates({ ...rateValues, [id]: e.target.value }); setMessage(''); setSaveError('') }} /><span aria-hidden="true">%</span></div></div>
  }
  const unused = data?.channels.filter(c => !choices.includes(c.id)) ?? []
  return <section className="admin-panel payment-settings-panel">
    <div className="payment-settings-heading"><div className="payment-section-icon"><CreditCard size={19} /></div><div><h2>收款设置</h2><p>选择支付渠道，设置向客户加收的手续费。</p></div><span className={`connection-state ${data?.checkout ? 'is-ready' : ''}`}>{data ? data.checkout ? '收款已开放' : '收款已关闭' : '读取中'}</span></div>
    <Feedback error={error || saveError} loading={loading} data={data} shape="compact" />
    {data && <form className="payment-channel-settings" onSubmit={e => { e.preventDefault(); void save() }}>
      <div className="payment-method-rows">{([{ method: 'alipay', name: '支付宝', value: alipay, selectId: 'payment-alipay' }, { method: 'wxpay', name: '微信支付', value: wechat, selectId: 'payment-wechat' }] as const).map(({ method, name, value, selectId }) => <div className="payment-method-row" key={method}>
        <div className="payment-method-name"><div className="payment-brand-icon"><PaymentLogo method={method} /></div><div><h3>{name}</h3><span>{value ? '客户可选' : '未启用'}</span></div></div>
        <div className="payment-provider-field"><label htmlFor={selectId}>{name}收款渠道</label><div className="payment-select-wrap"><select id={selectId} value={value} disabled={busy || loading} onChange={e => choose(method, e.target.value)}><option value="">不启用{name}</option>{data.channels.filter(c => c.id.endsWith('-wxpay') === (method === 'wxpay')).map(c => <option key={c.id} value={c.id} disabled={!c.configured}>{c.name.replace(/ · (支付宝|微信支付)$/, '')}{c.configured ? '' : '（待配置）'}</option>)}</select><ChevronDown size={14} aria-hidden="true" /></div></div>
        {value ? feeInput(value, name) : <div className="payment-disabled-rate">启用后可设置手续费</div>}
        <div className="payment-price-example"><span>商品 ¥100.00 · 客户实付</span><strong>{value && !invalidChannels.includes(value) ? `¥${((10000 + Math.floor((10000 * (rateBps[value] ?? 0) + 5000) / 10000)) / 100).toFixed(2)}` : '—'}</strong></div>
      </div>)}</div>
      <div className="payment-details"><details><summary>其他渠道与费率 <span>{unused.length} 个备用渠道</span><ChevronDown size={14} /></summary><div className="payment-reserve-list">{unused.map(c => <div key={c.id}><div><strong>{c.name}</strong><span className={`connection-state ${c.configured ? 'is-ready' : ''}`}>{c.configured ? '已配置' : '待配置'}</span></div><span>已保存费率 {(data.feeRatesBps?.[c.id] ?? 0) / 100}%</span></div>)}<p className="payment-rules">启用渠道后可修改费率；未启用渠道保留原设置。</p></div></details><details><summary>计费与退款规则<ChevronDown size={14} /></summary><div className="payment-rules"><p>手续费只加收在本站商品订单上，按分四舍五入；退款时随商品款同比例退还。</p><p>修改仅影响新付款单，已有付款单继续使用原渠道与费率。ChatGPT 仍由 Recharge 平台代收，开票手续费不再叠加此费率。</p></div></details></div>
      {!ratesValid && <p className="payment-validation" id="payment-rate-error" role="alert">{invalidChannels.map(id => data.channels.find(c => c.id === id)?.name ?? id).join('、')}：请输入 0–100 的百分比，最多两位小数。</p>}
      {choices.length === 0 && <p className="payment-validation" role="alert">请至少启用一种支付方式。</p>}
      <div className="payment-save-bar"><div aria-live="polite">{message ? <span className="payment-saved"><Check size={15} />{message}</span> : changed ? <span>有未保存的更改</span> : <span>更改保存后生效</span>}</div><div>{changed && <Button type="button" variant="ghost" disabled={busy || loading} onClick={() => { setSelected(null); setRates(null); setMessage(''); setSaveError('') }}><RotateCcw size={14} />撤销更改</Button>}<Button type="submit" disabled={busy || loading || choices.length === 0 || !changed || !ratesValid || choices.some(id => !data.channels.find(channel => channel.id === id)?.configured)}>{busy ? '保存中…' : '保存设置'}</Button></div></div>
    </form>}
  </section>
}
