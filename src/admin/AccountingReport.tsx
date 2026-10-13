import { useState } from 'react'
import { ListFilter, ReceiptText } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from '@/components/ui/table'
import { DateTimePicker, WorkspaceSelect, WorkspacePanel, WorkspaceMetrics } from '@/components/workspace-controls'
import { Feedback } from '@/components/workspace-loading'
import { useResource } from '@/lib/use-resource'
import { ORDER_LABELS, type OrderStatus } from '@/lib/demo-orders'
import type { AccountingReport as Report, AccountingReportTotals } from '../../shared/accounting.ts'

const money = (cents:number) => new Intl.NumberFormat('zh-CN',{style:'currency',currency:'CNY'}).format(cents/100)
const time = (ms:number) => new Date(ms).toLocaleString('zh-CN',{timeZone:'Asia/Shanghai',hour12:false})
const inputTime = (ms:number) => new Date(ms+8*3600000).toISOString().slice(0,19)
type Filters = {start:string;end:string;status:string}
function range(days=1):Filters {
  const now=Date.now();const midnight=Date.parse(inputTime(now).slice(0,10)+'T00:00:00+08:00')
  return {start:inputTime(midnight-(days-1)*86400000),end:inputTime(now),status:'success'}
}
function query(filters:Filters) {
  const params=new URLSearchParams({status:filters.status})
  for(const key of ['start','end'] as const) {
    if(!filters[key])continue
    const value=Date.parse(filters[key]+'+08:00')
    if(!Number.isFinite(value))throw new Error('请选择有效的起止时间。')
    params.set(key,String(value+(key==='end'?1000:0)))
  }
  if(params.has('start')&&params.has('end')&&Number(params.get('start'))>=Number(params.get('end')))throw new Error('结束时间不能早于开始时间。')
  return params.toString()
}
const statusOptions=[{value:'success',label:'已完成'},{value:'all',label:'全部状态'},...Object.entries(ORDER_LABELS).filter(([value])=>value!=='success').map(([value,label])=>({value,label})),{value:'refunded',label:'有退款'}]
const netProfit=(t:AccountingReportTotals)=>t.profit_cents===null?'待核对':money(t.profit_cents)

export default function AccountingReport({revision}:{revision:number}) {
  const [filters,setFilters]=useState<Filters>(()=>range())
  const [applied,setApplied]=useState(()=>query(filters));const [page,setPage]=useState(1);const [refresh,setRefresh]=useState(0);const [validation,setValidation]=useState('');const [details,setDetails]=useState(false)
  const {data,error,loading}=useResource<Report>(`/api/admin/accounting/report?${applied}&page=${page}`,revision+refresh)
  function apply(next=filters) {try{setApplied(query(next));setPage(1);setRefresh(v=>v+1);setValidation('')}catch(e){setValidation(e instanceof Error?e.message:'筛选条件无效。')}}
  function preset(kind:'today'|'yesterday'|'week'|'all') {
    let next={...range(kind==='week'?7:1),status:filters.status}
    if(kind==='yesterday'){const today=Date.parse(next.start+'+08:00');next={...next,start:inputTime(today-86400000),end:inputTime(today-1000)}}
    if(kind==='all')next={...next,start:'',end:''}
    setFilters(next);apply(next)
  }
  return <WorkspacePanel title="订单利润分析" description="按下单时间与状态查询，净利润扣除采购成本和代理分成。" action={<Badge variant="secondary">北京时间</Badge>}>
    <form onSubmit={e=>{e.preventDefault();apply()}}>
      <div className="workspace-filters">
        <DateTimePicker id="accounting-report-start" label="开始时间" value={filters.start} onChange={start=>setFilters(v=>({...v,start}))}/>
        <DateTimePicker id="accounting-report-end" label="结束时间" value={filters.end} onChange={end=>setFilters(v=>({...v,end}))}/>
        <WorkspaceSelect label="订单状态" value={filters.status} onChange={status=>setFilters(v=>({...v,status}))} options={statusOptions}/>
        <Button type="submit" disabled={loading}><ListFilter size={15}/>{loading?'查询中…':'查询统计'}</Button>
      </div>
      <div className="workspace-quick-actions">{(['today','yesterday','week','all'] as const).map((kind,i)=><Button key={kind} variant="outline" type="button" size="sm" onClick={()=>preset(kind)}>{['今天','昨天','近 7 天','全部时间'][i]}</Button>)}</div>
    </form>
    <Feedback error={validation||error} loading={loading} data={data}/>
    {data&&<><p className="workspace-caption">{data.start===null?'最早订单':time(data.start)} 至 {data.end===null?'当前':time(data.end-1000)} · {statusOptions.find(s=>s.value===data.status)?.label}</p>
      <WorkspaceMetrics items={[
        {label:'已完成净利润',value:netProfit(data.totals),accent:true},
        {label:'筛选订单 / 已付款',value:`${data.totals.order_count} / ${data.totals.paid_count} 单`},
        {label:'付款金额',value:money(data.totals.paid_cents)},
        {label:'退款金额',value:money(data.totals.refund_cents)},
        {label:'已付款且完成',value:`${data.totals.completed_count} 单`},
        {label:'完成订单付款',value:money(data.totals.completed_paid_cents)},
        {label:'完成订单采购成本',value:data.totals.missing_cost_count?'待补成本':money(data.totals.cost_cents)},
        {label:'代理分成（含未提现）',value:data.totals.missing_commission_count?'待核对':money(data.totals.commission_cents)},
      ]}/>
      <Table className="workspace-table"><TableHeader><TableRow>{['套餐','单数 / 付款 / 完成','付款金额','退款','完成采购成本','代理分成','已完成净利润'].map(s=><TableHead key={s}>{s}</TableHead>)}</TableRow></TableHeader><TableBody>{data.groups.map(g=><TableRow key={g.plan}><TableCell><strong>{g.plan_name}</strong></TableCell><TableCell>{g.order_count} / {g.paid_count} / {g.completed_count}</TableCell><TableCell>{money(g.paid_cents)}</TableCell><TableCell>{money(g.refund_cents)}</TableCell><TableCell>{g.missing_cost_count?'待补成本':money(g.cost_cents)}</TableCell><TableCell>{g.missing_commission_count?'待核对':money(g.commission_cents)}</TableCell><TableCell><strong>{netProfit(g)}</strong></TableCell></TableRow>)}</TableBody></Table>
      {!data.groups.length&&<div className="workspace-empty"><h3>此范围没有订单</h3><p>调整时间或状态后重新查询。</p></div>}
      <p className="workspace-caption">净利润 = 已完成订单付款 − 已完成订单退款 {money(data.totals.completed_refund_cents)} − 采购成本 − 代理分成。已付未完成订单仅计付款金额，未付款订单仅计单数。已清账订单沿用历史采购成本，其余使用当前成本。<br/>{data.totals.held_count>0&&`${data.totals.held_count} 笔完成订单售后或金额待核对；`}{data.totals.missing_cost_count>0&&`${data.totals.missing_cost_count} 笔缺采购成本；`}{data.totals.missing_commission_count>0&&`${data.totals.missing_commission_count} 笔代理分成待核对；`}{data.totals.profit_cents===null&&'核对完成后显示合计净利润。'}统计包含已清和未清订单，更新于 {time(data.asOf)}。</p>
      <Button variant="outline" disabled={!data.totals.order_count} onClick={()=>setDetails(true)}><ReceiptText size={15}/>查看筛选订单明细（{data.totals.order_count}）</Button>
      <Dialog open={details} onOpenChange={setDetails}><DialogContent className="workspace-dialog motion-dialog"><DialogHeader><DialogTitle>筛选订单明细</DialogTitle><DialogDescription>共 {data.totals.order_count} 单，金额汇总覆盖全部筛选结果。</DialogDescription></DialogHeader>{details&&<><Feedback error={error} loading={loading} data={data}/><Table className="workspace-table"><TableHeader><TableRow>{['订单 / 下单时间','套餐 / 来源','状态','付款','退款','采购成本','代理分成','净利润'].map(s=><TableHead key={s}>{s}</TableHead>)}</TableRow></TableHeader><TableBody>{data.orders.map(o=>{const completed=o.status==='success'&&o.payment_status==='paid';return <TableRow key={o.id}><TableCell><strong>{o.id}</strong><small>{time(o.created_at)}</small></TableCell><TableCell>{o.plan_name}<small>{o.partner_name||'直营'}</small></TableCell><TableCell><Badge variant="secondary">{ORDER_LABELS[o.status as OrderStatus]}</Badge><small>{o.payment_status==='paid'?'已付款':'未付款'}{o.held?' · 待核对':''}</small></TableCell><TableCell>{money(o.paid_cents)}</TableCell><TableCell>{money(o.refund_cents)}</TableCell><TableCell>{!completed?'—':o.cost_cents===null?'待补成本':money(o.cost_cents)}</TableCell><TableCell>{!completed?'—':o.commission_cents===null?'待核对':money(o.commission_cents)}</TableCell><TableCell>{!completed?'—':o.profit_cents===null?'待核对':money(o.profit_cents)}</TableCell></TableRow>})}</TableBody></Table><div className="workspace-page-bar"><span>第 {page} / {Math.max(1,Math.ceil(data.totals.order_count/20))} 页</span><div><Button variant="outline" disabled={page===1||loading} onClick={()=>setPage(p=>p-1)}>上一页</Button><Button variant="outline" disabled={page*20>=data.totals.order_count||loading} onClick={()=>setPage(p=>p+1)}>下一页</Button></div></div></>}</DialogContent></Dialog>
    </>}
  </WorkspacePanel>
}
