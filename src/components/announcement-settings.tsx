import { useEffect, useState } from 'react'
import { Check, Eye, RefreshCw } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Checkbox } from '@/components/ui/checkbox'
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Feedback } from '@/components/workspace-loading'
import { AnnouncementDialog } from '@/components/announcement-dialog'
import { api, errorMessage } from '@/lib/api'
import { useResource } from '@/lib/use-resource'
import { announcementContentLimit, announcementTitleLimit, type AnnouncementPlacement, type AnnouncementSettings } from '../../shared/announcements'
import './support-contacts.css'

const placements: [AnnouncementPlacement, string][] = [['home', '首页'], ['product', '商品页'], ['both', '首页和商品页']]

function AnnouncementForm({ endpoint, settings, loading, readOnly, onSaved, onEditing }: { endpoint: string; settings: AnnouncementSettings; loading: boolean; readOnly: boolean; onSaved: () => void; onEditing: () => void }) {
  const [draft, setDraft] = useState(settings)
  const [busy, setBusy] = useState(false); const [error, setError] = useState(''); const [preview, setPreview] = useState(false)
  const changed = draft.enabled !== settings.enabled || draft.placement !== settings.placement || draft.title !== settings.title || draft.content !== settings.content
  function edit(patch: Partial<AnnouncementSettings>) { setDraft(previous => ({ ...previous, ...patch })); setError(''); onEditing() }
  async function save() {
    if (busy || loading || readOnly || !changed) return
    if (draft.enabled && (!draft.title.trim() || !draft.content.trim())) { setError('启用公告前，请填写标题和内容。'); return }
    setBusy(true); setError('')
    try { await api<AnnouncementSettings>(endpoint, draft); onSaved() }
    catch (e) { setError(errorMessage(e)) } finally { setBusy(false) }
  }
  return <form className="support-settings-form announcement-settings-form" onSubmit={event => { event.preventDefault(); void save() }}>
    <FieldGroup>
      <Field><FieldLabel htmlFor="announcement-title">标题</FieldLabel><Input id="announcement-title" value={draft.title} onChange={event => edit({ title: event.target.value })} maxLength={announcementTitleLimit} disabled={busy || readOnly} placeholder="例如：国庆服务安排" /></Field>
      <Field><FieldLabel htmlFor="announcement-content">公告文字</FieldLabel><Textarea id="announcement-content" value={draft.content} onChange={event => edit({ content: event.target.value })} maxLength={announcementContentLimit} rows={8} disabled={busy || readOnly} placeholder="填写要告知客户的信息，支持换行。" /><FieldDescription className="announcement-word-count">{draft.content.length} / {announcementContentLimit}</FieldDescription></Field>
      <FieldGroup className="announcement-settings-options">
        <Field><label className="announcement-checkbox"><Checkbox checked={draft.enabled} onCheckedChange={checked => edit({ enabled: checked })} disabled={busy || readOnly} />启用弹窗公告</label></Field>
        <Field><FieldLabel>弹出位置</FieldLabel><div className="announcement-placements" role="group" aria-label="弹出位置">{placements.map(([value, label]) => <Button key={value} type="button" variant={draft.placement === value ? 'default' : 'outline'} aria-pressed={draft.placement === value} onClick={() => edit({ placement: value })} disabled={busy || readOnly}>{label}</Button>)}</div></Field>
      </FieldGroup>
    </FieldGroup>
    <Feedback error={error} />
    <div className="announcement-settings-actions">{!readOnly && <Button type="submit" disabled={busy || loading || !changed}>{busy ? '保存中…' : '保存公告'}</Button>}<Button type="button" variant="outline" disabled={!draft.content.trim()} onClick={() => setPreview(true)}><Eye size={15} />预览弹窗</Button></div>
    <AnnouncementDialog announcement={draft} open={preview} onClose={() => setPreview(false)} />
  </form>
}

export function AnnouncementSettingsPanel({ endpoint, revision = 0, readOnly = false, onSummaryChange }: { endpoint: string; revision?: number; readOnly?: boolean; onSummaryChange?: (summary: string) => void }) {
  const [localRevision, setLocalRevision] = useState(0); const [message, setMessage] = useState('')
  const { data, error, loading } = useResource<AnnouncementSettings>(endpoint, revision + localRevision)
  useEffect(() => { onSummaryChange?.(error ? '读取失败，展开后重试' : data ? data.enabled ? `已开启 · ${data.title}` : '未开启 · 向客户展示商店公告' : '正在读取…') }, [data, error, onSummaryChange])
  return <section className="admin-panel support-settings-panel">
    <div className="admin-panel-title"><h2>站点公告</h2><Button type="button" variant="ghost" size="icon" aria-label="刷新公告" disabled={loading} onClick={() => { setMessage(''); setLocalRevision(value => value + 1) }}><RefreshCw size={15} /></Button></div>
    <p className="support-settings-description">填写标题和公告文字，在你的商店指定页面弹出。</p>
    <Feedback error={error} loading={loading} data={data} shape="compact" />
    {data && <AnnouncementForm key={endpoint + ':' + data.version} endpoint={endpoint} settings={data} loading={loading} readOnly={readOnly} onEditing={() => setMessage('')} onSaved={() => { setMessage('公告已保存'); setLocalRevision(value => value + 1) }} />}
    {message && <p className="support-settings-success" role="status"><Check size={14} />{message}</p>}
  </section>
}
