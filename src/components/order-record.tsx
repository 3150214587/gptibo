import { ArrowRight, Check, Clock3, Info, Phone } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { CopyButton } from './copy-button'
import { PaymentConfirmed } from './payment-feedback'
import type { CheckoutProps } from './checkout'

export function OrderRecord({ order, onOrders, error }: CheckoutProps) {
  if (!order) return null
  if (order.redemptionBlocked && order.status !== 'success') return <div className="order-service-paused">本订单已由客服处理，无法再次兑换。<br />如有疑问，请联系支持。</div>
  const archived = order.mode !== 'live'
  const paused = order.refundHold || (order.refundedCents ?? 0) >= Math.round(order.price * 100)
  return <>
    {error && <p role="alert" className="copy-error">{error}</p>}
    {paused && !archived ? <div className="order-service-paused">{order.refundHold ? '售后申请正在处理中，服务暂时暂停。' : '本订单已全额退款。'}<br />可以在下方查看售后进度。</div> : <>
      {!archived && order.paymentStatus === 'paid' && <PaymentConfirmed>付款已确认，服务进度以订单记录为准。</PaymentConfirmed>}
      {!archived && order.plan === 'codex-sms-once' && order.smsPhone && <div className="sms-phone-panel"><div className="sms-phone-caption"><span><Phone size={14} />本次手机号</span></div><div className="sms-phone-value"><strong>{order.smsPhone}</strong><CopyButton iconOnly variant="ghost" value={order.smsPhone} label="复制手机号" /></div></div>}
      <div className="result-body">
        <div className="result-icon">{archived ? <Info size={30} strokeWidth={1.5} /> : order.status === 'success' && order.plan !== 'codex-sms-once' ? <Check size={30} /> : <Clock3 size={30} strokeWidth={1.5} />}</div>
        <h3>{archived ? '订单已归档' : order.status === 'cancelled' ? '订单已关闭' : order.status === 'success' && order.plan !== 'codex-sms-once' ? '服务已完成' : '服务进度待同步'}</h3>
        <p>{archived ? '此历史记录不支持继续付款或交付。' : order.status === 'cancelled' ? '可在订单中查看记录与售后进度。' : order.status === 'success' && order.plan !== 'codex-sms-once' ? '开通结果已记录在这笔订单中。' : '服务接入完成后，可在原订单继续操作。'}<br />订单信息与售后记录仍可查看。</p>
        <Button className="primary-action" onClick={onOrders}>查看我的订单 <ArrowRight size={16} /></Button>
      </div>
    </>}
  </>
}
