import { useState } from 'react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { api, errorMessage } from '@/lib/api'
import { Feedback } from '@/partner/shared'
import { usePartnerData, type Partner } from '@/partner/model'
import '@/partner/children.css'

export function RecruitPermission({ partner, onSaved, disabled }: { partner: Partner; onSaved: () => void; disabled: boolean }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  async function save() {
    if (busy) return
    setBusy(true); setError('')
    try { await api(`/api/admin/partners/${partner.id}/recruit-permission`, { enabled: !partner.can_recruit, version: partner.version }); onSaved() }
    catch (e) { setError(errorMessage(e)) } finally { setBusy(false) }
  }
  return <section className="hierarchy-setting">
    <div className="hierarchy-setting-heading"><div><h3>发展二级代理</h3><p>{partner.parent_id ? '此账号为二级代理，不能继续发展下级。' : '由总后台授权，一级代理自行开通与管理直属二级。'}</p></div><Badge variant={partner.can_recruit ? 'secondary' : 'outline'}>{partner.can_recruit ? '已开放' : '未开放'}</Badge></div>
    {!partner.parent_id && <><div className="hierarchy-setting-actions"><p>关闭仅限制新增，已有二级仍可经营。<br />停用一级账号后，其二级停止新接单。</p><Button variant={partner.can_recruit ? 'outline' : 'default'} disabled={disabled || busy} onClick={() => void save()}>{busy ? '保存中…' : partner.can_recruit ? '关闭新增二级权限' : '开放发展二级权限'}</Button></div><Feedback error={error} /></>}
  </section>
}

export function ParentRelationship({ partner, onSaved, disabled }: { partner: Partner; onSaved: () => void; disabled: boolean }) {
  const [query, setQuery] = useState('')
  const [search, setSearch] = useState('')
  const [parentId, setParentId] = useState(partner.parent_id ?? '')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const choices = usePartnerData<{ rows: Partner[] }>('/api/admin/partners?page=1&q=' + encodeURIComponent(search), 0)
  const candidates = choices.data?.rows.filter(p => p.id !== partner.id && !p.parent_id && p.can_recruit && p.active) ?? []
  const items = [{ value: '', label: '无上级（一级代理）' }, ...(partner.parent_id && !candidates.some(p => p.id === partner.parent_id) ? [{ value: partner.parent_id, label: '现有上级' }] : []), ...candidates.map(p => ({ value: p.id, label: `${p.name} · ${p.email}` }))]
  async function save() {
    if (busy) return
    setBusy(true); setError('')
    try { await api(`/api/admin/partners/${partner.id}/parent`, { parentId, version: partner.version }); onSaved() }
    catch (e) { setError(errorMessage(e)) } finally { setBusy(false) }
  }
  return <section className="hierarchy-setting">
    <div className="hierarchy-setting-heading"><div><h3>上下级关系</h3><p>可调整二级归属，或解除关系升为一级。</p></div><Badge variant="secondary">{partner.parent_id ? '二级代理' : '一级代理'}</Badge></div>
    <form onSubmit={e => { e.preventDefault(); setSearch(query) }}><Field><FieldLabel htmlFor="parent-search">查找上级</FieldLabel><div className="hierarchy-search"><Input id="parent-search" placeholder="一级代理邮箱或名称" value={query} onChange={e => setQuery(e.target.value)} disabled={busy || disabled} /><Button type="submit" variant="outline" disabled={busy || disabled || choices.loading}>查找</Button></div><FieldDescription>仅可选择已启用且开放发展权限的一级代理。</FieldDescription></Field></form>
    <form className="hierarchy-parent-form" onSubmit={e => { e.preventDefault(); void save() }}><Field><FieldLabel htmlFor="parent-choice">上级代理</FieldLabel><Select items={items} value={parentId} onValueChange={v => { if (v !== null) setParentId(v) }} disabled={busy || disabled}><SelectTrigger id="parent-choice"><SelectValue /></SelectTrigger><SelectContent>{items.map(item => <SelectItem key={item.value} value={item.value}>{item.label}</SelectItem>)}</SelectContent></Select><FieldDescription>仅新订单采用新归属，历史收益保留。变更后账号需重新登录；拥有下级的一级不能降为二级。</FieldDescription></Field><Feedback error={error || choices.error} /><Button type="submit" variant="outline" disabled={busy || disabled || parentId === (partner.parent_id ?? '')}>{busy ? '保存中…' : '保存上下级关系'}</Button></form>
  </section>
}
