import { useState } from 'react'
import { CalendarIcon } from 'lucide-react'
import { zhCN } from 'date-fns/locale'
import { Calendar } from '@/components/ui/calendar'
import { Popover,PopoverTrigger,PopoverContent } from '@/components/ui/popover'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select,SelectTrigger,SelectValue,SelectContent,SelectItem } from '@/components/ui/select'
import { Card,CardHeader,CardTitle,CardDescription,CardContent,CardAction } from '@/components/ui/card'
import type { ReactNode } from 'react'
import './workspace-controls.css'

export function WorkspaceSelect({label,value,onChange,options,disabled=false}:{label:string;value:string;onChange:(value:string)=>void;options:{value:string;label:string}[];disabled?:boolean}) {
  return <div className="workspace-control"><Label>{label}</Label><Select value={value} onValueChange={v=>{if(v!==null)onChange(String(v))}} items={options} disabled={disabled}><SelectTrigger aria-label={label}><SelectValue /></SelectTrigger><SelectContent className="motion-popover" alignItemWithTrigger={false}>{options.map(o=><SelectItem value={o.value} key={o.value}>{o.label}</SelectItem>)}</SelectContent></Select></div>
}
export function DateTimePicker({id,label,value,onChange,dateOnly=false}:{id:string;label:string;value:string;onChange:(value:string)=>void;dateOnly?:boolean}) {
  const [open,setOpen]=useState(false)
  const day=value.slice(0,10); const selected=day?new Date(day+'T12:00:00'):undefined
  const clock=value.includes('T')?value.slice(11):'00:00:00'
  return <div className="workspace-control"><Label htmlFor={id}>{label}</Label><div className="workspace-date-row"><Popover open={open} onOpenChange={setOpen}><PopoverTrigger render={<Button id={id} variant="outline" aria-label={label} />}><CalendarIcon size={15}/>{day||'不限日期'}</PopoverTrigger><PopoverContent className="workspace-calendar motion-popover" align="start"><Calendar mode="single" selected={selected} defaultMonth={selected} locale={zhCN} captionLayout="label" onSelect={date=>{if(date){const d=[date.getFullYear(),String(date.getMonth()+1).padStart(2,'0'),String(date.getDate()).padStart(2,'0')].join('-');onChange(dateOnly?d:d+'T'+clock)}else onChange('');setOpen(false)}} /><Button size="sm" variant="ghost" onClick={()=>{onChange('');setOpen(false)}}>清除日期</Button></PopoverContent></Popover>{!dateOnly&&<Input aria-label={label+'时间'} type="time" step="1" value={day?clock:''} disabled={!day} onChange={e=>{const clockValue=e.currentTarget.value;onChange(day+'T'+(clockValue||'00:00:00'))}} onInput={e=>{const clockValue=e.currentTarget.value;onChange(day+'T'+(clockValue||'00:00:00'))}} />}</div></div>
}
export function WorkspacePanel({title,description,action,children,className=''}:{title:string;description?:string;action?:ReactNode;children:ReactNode;className?:string}) {
  return <Card className={'workspace-panel '+className}><CardHeader><CardTitle>{title}</CardTitle>{description&&<CardDescription>{description}</CardDescription>}{action&&<CardAction>{action}</CardAction>}</CardHeader><CardContent>{children}</CardContent></Card>
}
export function WorkspaceMetrics({items}:{items:{label:string;value:ReactNode;accent?:boolean}[]}) {
  return <div className="workspace-metrics">{items.map(i=><div key={i.label} data-accent={i.accent||undefined}><span>{i.label}</span><strong>{i.value}</strong></div>)}</div>
}
