import type { RefObject } from 'react'
import { ArrowRight, Info } from 'lucide-react'
import { Button } from './ui/button'
import { DialogDescription, DialogHeader, DialogTitle } from './ui/dialog'
import { ORDER_PLANS, type PlanId } from '@/lib/demo-orders'
import { productForPlan } from '@/lib/products'
import '@/styles/purchase-notice.css'

function fulfillmentNotice(plan: PlanId) {
  switch (productForPlan(plan).id) {
    case 'gemini': return {
      emphasis: '需自行领取，付款后不会自动开通会员。',
      steps: ['发货后，在订单或邮箱中查看领取链接。', '点击「立即领取」，核对 Google 账号并按指引操作。', '领取完成后，登录对应账号确认会员状态。'],
    }
    case 'duolingo': return {
      emphasis: '需自行兑换 CDK，付款后会员不会自动到账。',
      steps: ['发货后，在订单或邮箱中复制兑换 CDK。', '按领取说明进入兑换页面，核对多邻国账号后兑换。', '兑换完成后，登录对应账号确认会员状态。'],
    }
    case 'gmail': return {
      emphasis: '账号资料邮件交付，请确保邮箱能正常收件。',
      steps: ['付款后查收账号资料与说明，同时检查垃圾邮件。', '购买当天完成首次登录，留意登录要求与质保范围。'],
    }
    case 'x-premium': return {
      emphasis: '付款前请核对 X 用户名与订阅周期。',
      steps: ['填写并验证用户名，无需提供密码或 Cookie。', '付款后在订单查看进度，完成后前往 X 确认会员。'],
    }
    case 'codex-sms': return {
      emphasis: '单次接码服务，请先确认适用范围。',
      steps: ['付款后在订单获取手机号，再按指引查询验证码。', '接收结果在原订单查看，无需提交 ChatGPT Session。'],
    }
    default: return {
      emphasis: ORDER_PLANS.find(item => item.id === plan)?.renewalOnly ? '仅支持已有订阅续费，请确认账号符合要求。' : '付款后需连接账号并提交充值。',
      steps: ['核对套餐和金额，付款后按指引连接 ChatGPT 账号。', '在原订单查看结果；等待或超时时请勿重复下单。'],
    }
  }
}

// Render inside PurchaseFlow's existing popup so continuing never swaps focus traps.
export function PurchaseNotice({ plan, purchaseNote, supportsWarranty = true, titleRef, onCancel, onContinue }: { plan: PlanId; purchaseNote?: string; supportsWarranty?: boolean; titleRef: RefObject<HTMLHeadingElement | null>; onCancel: () => void; onContinue: () => void }) {
  const product = productForPlan(plan)
  const notice = fulfillmentNotice(plan)
  return <>
      <DialogHeader>
        <DialogTitle ref={titleRef} tabIndex={-1}>下单须知</DialogTitle>
        <DialogDescription>{product.name} · {ORDER_PLANS.find(item => item.id === plan)?.name}</DialogDescription>
      </DialogHeader>
      <div className="purchase-notice-body">
        <div className="purchase-notice-emphasis"><Info size={16} aria-hidden="true" /><strong>{notice.emphasis}</strong></div>
        <ol className="purchase-notice-steps">{notice.steps.map(step => <li key={step}>{step}</li>)}</ol>
        {!supportsWarranty && <section className="purchase-notice-product" role="note"><h3>不支持售后质保</h3><p>该商品不支持售后质保，购买后无法提交售后申请。请确认接受此说明后再继续下单；如有疑问，请先联系商店客服。</p></section>}
        {purchaseNote?.trim() && <section className="purchase-notice-product"><h3>购买提醒</h3><p>{purchaseNote}</p></section>}
        <p className="purchase-notice-help">领取有疑问？请先联系支持，确认后再购买。</p>
      </div>
      <footer className="purchase-notice-footer">
        <div className="purchase-notice-actions"><Button variant="outline" onClick={onCancel}>暂不购买</Button><Button onClick={onContinue}>继续下单 <ArrowRight size={16} /></Button></div>
      </footer>
  </>
}
