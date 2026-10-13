import { Button } from '@/components/ui/button'
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell, TableCaption } from '@/components/ui/table'
import { useState } from 'react'
import { Package } from 'lucide-react'
import { ProductMark } from '@/components/brand'
import { Feedback } from '@/components/workspace-loading'
import { productNameForId } from '../../shared/email-products'
import type { PartnerOrderPeriod, PartnerOrderSummary } from '../../shared/partner-order-summary'
import { money, usePartnerData } from './model'
import './order-summary.css'

const periods: { value: PartnerOrderPeriod; label: string }[] = [
  { value: 'all', label: '全部时间' }, { value: 'today', label: '今天' },
  { value: '7', label: '近 7 天' }, { value: '30', label: '近 30 天' },
]
const count = (value: number) => value.toLocaleString('zh-CN')

export function OrderSummary({ endpoint, revision }: { endpoint: string; revision: number }) {
  const [period, setPeriod] = useState<PartnerOrderPeriod>('all')
  const { data, error, loading } = usePartnerData<PartnerOrderSummary>(`${endpoint}?period=${period}`, revision)
  return <section className="partner-product-summary" aria-labelledby="partner-product-summary-title" aria-busy={loading}>
    <div className="partner-summary-heading">
      <div><h2 id="partner-product-summary-title">商品收益统计</h2><p>按商品汇总正式订单与已确认收益</p></div>
      <div className="partner-summary-periods" role="group" aria-label="统计时间范围">
        {periods.map(item => <Button variant="ghost" key={item.value} type="button" aria-pressed={period === item.value} onClick={() => setPeriod(item.value)}>{item.label}</Button>)}
      </div>
    </div>
    <Feedback error={error} loading={loading} data={data} shape="compact" />
    {data && <>
      <dl className="partner-summary-totals">
        <div><dt>已确认收益</dt><dd className="partner-profit">{money(data.totals.profitCents)}</dd></div>
        <div><dt>订单总数</dt><dd>{count(data.totals.orderCount)}<span>单</span></dd></div>
        <div><dt>已付款完成</dt><dd>{count(data.totals.completedCount)}<span>单</span></dd></div>
      </dl>
      {data.products.length ? <Table className="partner-summary-table">
        <TableCaption className="sr-only">{periods.find(item => item.value === period)?.label}各商品订单数量和已确认收益</TableCaption>
        <TableHeader><TableRow><TableHead scope="col">商品</TableHead><TableHead scope="col">订单数</TableHead><TableHead scope="col">已完成</TableHead><TableHead scope="col">收益</TableHead></TableRow></TableHeader>
        <TableBody>{data.products.map(product => <TableRow key={product.productId}>
          <TableHead scope="row"><div className="partner-summary-product">
            {product.productId === 'other' ? <Package size={20} aria-hidden="true" /> : <ProductMark small productId={product.productId} />}
            <span>{product.productId === 'other' ? '其他商品' : productNameForId(product.productId)}</span>
          </div></TableHead>
          <TableCell>{count(product.orderCount)}</TableCell><TableCell>{count(product.completedCount)}</TableCell>
          <TableCell><strong>{money(product.profitCents)}</strong>{product.heldCount > 0 && <small>{product.heldCount} 单待核对</small>}</TableCell>
        </TableRow>)}</TableBody>
      </Table> : <div className="partner-summary-empty"><Package size={24} aria-hidden="true" /><h3>此时间范围暂无订单</h3><p>客户下单后，各商品的订单数和收益会显示在这里。</p></div>}
      <p className="partner-summary-note">按北京时间下单时间统计；收益含已结算和未结算的已完成订单，已扣退款影响，不含待核对金额。{data.totals.heldCount > 0 && `当前 ${data.totals.heldCount} 单待核对。`}</p>
    </>}
  </section>
}
