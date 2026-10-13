import { Clock, ExternalLink, LoaderCircle, TriangleAlert } from 'lucide-react'
import { Button } from './ui/button'
import '@/styles/live-checkout.css'

type Props = { qr: string; image: string | null; imageError?: boolean; remaining: number; price: number; quantity: string }

// Shared by live checkout and the isolated recharge walkthrough.
export function PaymentCard({ qr, image, imageError, remaining, price, quantity }: Props) {
  return (<div className="live-pay-card">
          <div className="live-pay-header">
            <div className="live-pay-brand">
              <span className="alipay-mark-sm">支</span>
              <div className="live-pay-brand-info">
                <strong>支付宝支付</strong>
              </div>
            </div>
              <div className={`live-pay-timer ${remaining <= 300 ? 'danger' : remaining <= 600 ? 'warning' : 'normal'}`} title="订单付款有效期">
                <Clock size={12} />
                <span>{`${Math.floor(remaining / 60)}:${String(remaining % 60).padStart(2, '0')}`}</span>
              </div>
          </div>
          <div className="live-pay-body">
            <div className="live-qr-frame" aria-busy={!image && !imageError}>
              {image ? <img src={image} width={156} height={156} alt="支付宝付款二维码" /> : imageError ? <div className="live-qr-error"><TriangleAlert size={26} aria-hidden="true" /><span>二维码加载失败</span></div> : <LoaderCircle className="processing-spinner" size={26} aria-label="正在显示二维码" />}
            </div>
            <div className="live-pay-details">
              <div className="live-pay-amount">
                <span className="live-pay-amount-label">应付金额</span>
                <strong className="live-pay-amount-value"><small>¥</small>{price.toFixed(2)}</strong>
              </div>
              <div className="live-pay-quantity">
                <span className="live-pay-quantity-label">购买数量</span>
                <strong className="live-pay-quantity-value">{quantity}</strong>
              </div>
              <p className="live-pay-tip">请使用手机支付宝<strong>「扫一扫」</strong>完成付款</p>
              <Button nativeButton={false} className="live-mobile-action" render={<a href={qr} target="_blank" rel="noopener noreferrer" />}>在手机支付宝中打开 <ExternalLink size={13} /></Button>
            </div>
          </div>
          <div className="live-pay-footer">
            <span className="live-watch"><span aria-hidden="true" />付款后自动更新进度 · 支付后无需重复操作</span>
          </div>
        </div>)
}
