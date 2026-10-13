import { useRef, useState } from 'react'
import { Settings2, Check } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Alert,AlertDescription } from '@/components/ui/alert'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from '@/components/ui/table'
import { Tabs,TabsList,TabsTrigger,TabsContent } from '@/components/ui/tabs'
import { WorkspacePanel,WorkspaceMetrics } from '@/components/workspace-controls'
import { Feedback } from '@/components/workspace-loading'
import { api, errorMessage } from '@/lib/api'
import { useResource } from '@/lib/use-resource'
import type { AccountingCost, AccountingDetail, AccountingGroup, AccountingSummary, AccountingTotals } from '../../shared/accounting.ts'
import AccountingReport from './AccountingReport'

const money=(cents:number)=>new Intl.NumberFormat('zh-CN',{style:'currency',currency:'CNY'}).format(cents/100)
const time=(value:number)=>new Intl.DateTimeFormat('zh-CN',{timeZone:'Asia/Shanghai',year:'numeric',month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit',second:'2-digit',hour12:false}).format(value)
const profit=(totals:AccountingTotals)=>totals.missing_cost_count?'待补成本':money(totals.profit_cents)
function Groups({groups}:{groups:AccountingGroup[]}) {
  return <><Table className="workspace-table"><TableHeader><TableRow>{['套餐','完成订单','付款金额','采购成本','退款扣减','利润'].map(s=><TableHead key={s}>{s}</TableHead>)}</TableRow></TableHeader><TableBody>{groups.map(g=><TableRow key={g.plan}><TableCell><strong>{g.plan_name}</strong>{g.adjustment_count>0&&<small>含 {g.adjustment_count} 笔退款调整</small>}</TableCell><TableCell>{g.order_count}</TableCell><TableCell>{money(g.sale_cents)}</TableCell><TableCell>{g.missing_cost_count?'待补成本':money(g.cost_cents)}</TableCell><TableCell>{money(g.refund_cents)}</TableCell><TableCell><strong>{profit(g)}</strong></TableCell></TableRow>)}</TableBody></Table>{!groups.length&&<div className="workspace-empty"><h3>暂无待清账订单</h3><p>真实付款且完成的订单会汇总在这里。</p></div>}</>
}
function Costs({costs,version,onUpdated,onClose}:{costs:AccountingCost[];version:number;onUpdated:()=>void;onClose:()=>void}) {
  const [values,setValues]=useState(()=>Object.fromEntries(costs.map(c=>[c.plan,c.cost_cents===null?'':String(c.cost_cents/100)])))
  const [busy,setBusy]=useState(false);const [error,setError]=useState('')
  async function save() {
    if(busy)return
    if(costs.some(c=>values[c.plan]!==''&&(!/^\d+(\.\d{1,2})?$/.test(values[c.plan])||Number(values[c.plan])>100000))){setError('请输入 0–100000 元的采购成本，最多两位小数。');return}
    setBusy(true);setError('')
    try{await api('/api/admin/accounting/costs',{version,costs:costs.map(c=>({plan:c.plan,cost_cents:values[c.plan]===''?null:Math.round(Number(values[c.plan])*100)}))});onUpdated();onClose()}catch(e){setError(errorMessage(e))}finally{setBusy(false)}
  }
  return <Dialog open onOpenChange={open=>{if(!open&&!busy)onClose()}}><DialogContent className="workspace-dialog workspace-edit-dialog motion-dialog"><DialogHeader><DialogTitle>采购成本设置</DialogTitle><DialogDescription>单位：元 / 单。修改仅用于尚未清账的订单，历史清账保留原成本。</DialogDescription></DialogHeader><form className="workspace-form" onSubmit={e=>{e.preventDefault();void save()}}><div className="workspace-form-grid">{costs.map(c=><div className="workspace-control" key={c.plan}><Label htmlFor={'cost-'+c.plan}>{c.name}{c.plan==='pro-20x'?'（原 20×）':''}</Label><Input id={'cost-'+c.plan} value={values[c.plan]} onChange={e=>setValues(v=>({...v,[c.plan]:e.target.value}))} inputMode="decimal" placeholder="待填写" disabled={busy}/></div>)}</div><p className="workspace-caption">未知成本请留空；需补齐相关订单成本后才能清账。</p><Feedback error={error}/><div className="workspace-actions"><Button variant="outline" type="button" disabled={busy} onClick={onClose}>取消</Button><Button type="submit" disabled={busy}>{busy?'保存中…':'保存采购成本'}</Button></div></form></DialogContent></Dialog>
}
function Detail({id,onClose}:{id:string;onClose:()=>void}) {
  const [page,setPage]=useState(1)
  const {data,error,loading}=useResource<AccountingDetail>(`/api/admin/accounting/${id}?page=${page}`)
  return <Dialog open onOpenChange={open=>{if(!open)onClose()}}><DialogContent className="workspace-dialog motion-dialog"><DialogHeader><DialogTitle>清账明细</DialogTitle><DialogDescription>{data?`${time(data.settlement.from_at)} 至 ${time(data.settlement.closed_at)}（北京时间）`:'查看已保存的订单和利润。'}</DialogDescription></DialogHeader><Feedback error={error} loading={loading} data={data}/>{data&&<><WorkspaceMetrics items={[{label:'本次利润',value:money(data.settlement.profit_cents),accent:true},{label:'完成订单',value:data.settlement.order_count},{label:'付款金额',value:money(data.settlement.sale_cents)},{label:'采购成本',value:money(data.settlement.cost_cents)}]}/><Groups groups={data.groups}/><Table className="workspace-table"><TableHeader><TableRow>{['订单','类型','付款','成本','退款','利润'].map(s=><TableHead key={s}>{s}</TableHead>)}</TableRow></TableHeader><TableBody>{data.items.map(i=><TableRow key={i.order_id}><TableCell><strong>{i.plan_name}</strong><small>{i.order_id}</small></TableCell><TableCell>{i.kind==='refund'?'退款调整':'完成订单'}</TableCell><TableCell>{money(i.sale_cents)}</TableCell><TableCell>{money(i.cost_cents)}</TableCell><TableCell>{money(i.refund_cents)}</TableCell><TableCell>{money(i.sale_cents-i.cost_cents-i.refund_cents)}</TableCell></TableRow>)}</TableBody></Table>{data.total>50&&<div className="workspace-page-bar"><span>共 {data.total} 条 · 第 {page} 页</span><div><Button variant="outline" disabled={page===1||loading} onClick={()=>setPage(p=>p-1)}>上一页</Button><Button variant="outline" disabled={page*50>=data.total||loading} onClick={()=>setPage(p=>p+1)}>下一页</Button></div></div>}</>}</DialogContent></Dialog>
}
export default function AccountingPage({revision}:{revision:number}) {
  const [localRevision,setLocalRevision]=useState(0);const [page,setPage]=useState(1)
  const {data,error,loading}=useResource<AccountingSummary>(`/api/admin/accounting?page=${page}`,revision+localRevision)
  const [busy,setBusy]=useState(false);const [saveError,setSaveError]=useState('');const [message,setMessage]=useState('');const [detail,setDetail]=useState<string>();const [costsOpen,setCostsOpen]=useState(false)
  const requestKey=useRef<{version:number;key:string}|null>(null)
  const updated=()=>{setSaveError('');setLocalRevision(v=>v+1)}
  async function close() {
    if(!data||busy||loading)return
    if(requestKey.current?.version!==data.version)requestKey.current={version:data.version,key:crypto.randomUUID()}
    setBusy(true);setSaveError('');setMessage('')
    try{const result=await api<{settlement:AccountingDetail['settlement']}>('/api/admin/accounting/close',requestKey.current);setMessage(`清账已记录，本次利润 ${money(result.settlement.profit_cents)}。`);setPage(1);updated()}catch(e){setSaveError(errorMessage(e));setLocalRevision(v=>v+1)}finally{setBusy(false)}
  }
  return <div className="state-enter"><Tabs defaultValue="report" className="accounting-workspace-tabs"><TabsList variant="line" aria-label="财务统计导航"><TabsTrigger value="report">订单利润分析</TabsTrigger><TabsTrigger value="settlements">清账与记录</TabsTrigger></TabsList><TabsContent value="report" className="state-enter"><AccountingReport revision={revision+localRevision}/></TabsContent><TabsContent value="settlements" className="state-enter"><Feedback error={error} loading={loading} data={data}/><Feedback error={saveError}/>{message&&<Alert><Check size={15}/><AlertDescription>{message}</AlertDescription></Alert>}{data&&<><WorkspacePanel title="本次待清账" description={(data.fromAt===null?'首次清账从最早完成订单开始':`${data.historyTotal?'上次清账':'首次起算'}：${time(data.fromAt)}`)+' · 北京时间'} action={<div className="workspace-contact-list"><Button variant="outline" onClick={()=>setCostsOpen(true)}><Settings2 size={15}/>采购成本</Button><Button onClick={()=>void close()} disabled={busy||loading||Boolean(data.totals.missing_cost_count)||!((data.totals.order_count??0)+(data.totals.adjustment_count??0))}>{busy?'记录中…':'记录本次清账'}</Button></div>}><WorkspaceMetrics items={[{label:'本次利润',value:profit(data.totals),accent:true},{label:'完成订单',value:`${data.totals.order_count??0} 单`},{label:'付款金额',value:money(data.totals.sale_cents)},{label:'采购成本',value:data.totals.missing_cost_count?'待补成本':money(data.totals.cost_cents)}]}/><Groups groups={data.groups}/><p className="workspace-caption">清账利润 = 客户付款 − 采购成本 − 退款（本次退款扣减 {money(data.totals.refund_cents)}），尚未扣除代理分成。记录清账会保存本周期并开启下一周期，不会发起转账。较早下单、后来完成的订单会补入下一期。<br/>售后或金额待核对的 {data.heldCount} 笔暂缓，解除后补入；采购款返还需另行核对。更新于 {time(data.asOf)}。</p></WorkspacePanel><WorkspacePanel title="历史清账记录" description={`共 ${data.historyTotal} 次 · 每次统计保留当时的成本与金额`}><Table className="workspace-table"><TableHeader><TableRow>{['统计周期（北京时间）','订单 / 调整','付款金额','采购成本','退款扣减','利润','操作'].map(s=><TableHead key={s}>{s}</TableHead>)}</TableRow></TableHeader><TableBody>{data.history.map(h=><TableRow key={h.id}><TableCell><strong>{time(h.closed_at)}</strong><small>起：{time(h.from_at)}</small></TableCell><TableCell>{h.order_count} 单 / {h.adjustment_count} 笔</TableCell><TableCell>{money(h.sale_cents)}</TableCell><TableCell>{money(h.cost_cents)}</TableCell><TableCell>{money(h.refund_cents)}</TableCell><TableCell><strong>{money(h.profit_cents)}</strong></TableCell><TableCell><Button size="sm" variant="outline" onClick={()=>setDetail(h.id)}>明细</Button></TableCell></TableRow>)}</TableBody></Table>{!data.history.length&&<div className="workspace-empty"><h3>尚未清账</h3><p>记录后可以在这里回看每个周期。</p></div>}{data.historyTotal>10&&<div className="workspace-page-bar"><span>第 {page} 页</span><div><Button variant="outline" disabled={page===1||loading} onClick={()=>setPage(p=>p-1)}>上一页</Button><Button variant="outline" disabled={page*10>=data.historyTotal||loading} onClick={()=>setPage(p=>p+1)}>下一页</Button></div></div>}</WorkspacePanel></>}</TabsContent></Tabs>{detail&&<Detail key={detail} id={detail} onClose={()=>setDetail(undefined)}/ >}{costsOpen&&data&&<Costs costs={data.costs} version={data.version} onUpdated={updated} onClose={()=>setCostsOpen(false)}/>}</div>
}
