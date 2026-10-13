import './payment-method-choices.css'
import { Button } from '@/components/ui/button'
import { PaymentLogo } from './payment-logo'

export type PaymentMethodOption = { method: string; name: string; feeBps?: number }

export function PaymentMethodSelector({ methods, value, busy, onChange }: { methods: PaymentMethodOption[]; value?: string; busy: boolean; onChange: (method: string) => void }) {
  return <fieldset className="purchase-payment-methods" disabled={busy}>
    <legend>支付方式</legend>
    <div className="purchase-payment-options">{methods.map(option => <button type="button" key={option.method} aria-pressed={value === option.method} onClick={() => onChange(option.method)}>
      <PaymentLogo method={option.method} />
      <span>{option.name}</span>
    </button>)}</div>
    {!methods.length && <p role="status">暂未读取到可用支付方式，请关闭窗口并刷新后重试。</p>}
  </fieldset>
}

export function PaymentMethodChoices({ methods, busy, onChoose, baseCents }: { baseCents?: number; methods: PaymentMethodOption[]; busy: boolean; onChoose: (method: string) => void }) {
  return <section aria-label="选择支付方式" className="payment-method-choices">
    <h3>选择支付方式</h3>
    <div className="payment-method-buttons">{methods.map(option => <Button key={option.method} variant="outline" disabled={busy} onClick={() => onChoose(option.method)}><PaymentLogo method={option.method} />{option.method === 'wxpay' ? '微信' : option.name}付款{baseCents !== undefined && <span> ¥{((baseCents + Math.floor((baseCents * (option.feeBps ?? 0) + 5000) / 10000)) / 100).toFixed(2)}{option.feeBps ? `（含 ${option.feeBps / 100}% 手续费）` : ''}</span>}</Button>)}</div>
    {!methods.length && <p>{busy ? '正在读取支付方式…' : '暂无可用支付方式，请刷新后重试。'}</p>}
    {methods.length > 0 && <p>选择后生成付款码，支付结果会自动更新。</p>}
  </section>
}
