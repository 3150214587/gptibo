import { useStorefrontI18n } from '@/lib/storefront-i18n'
import { useEffect, useState } from 'react'
import { ArrowRight, Check, RefreshCw } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { CopyButton } from '@/components/copy-button'
import { Feedback } from '@/components/workspace-loading'
import { api, errorMessage } from '@/lib/api'
import { useResource } from '@/lib/use-resource'
import type { SupportContacts, SupportContactsSettings } from '../../shared/support-contacts'
import './support-contacts.css'

function ContactsForm({ endpoint, settings, loading, readOnly, onSaved, onEditing }: { endpoint: string; settings: SupportContactsSettings; loading: boolean; readOnly: boolean; onSaved: () => void; onEditing: () => void }) {
  const [contacts, setContacts] = useState(settings.contacts)
  const [busy, setBusy] = useState(false); const [error, setError] = useState('')
  const changed = Object.keys(contacts).some(key => contacts[key as keyof SupportContacts] !== settings.contacts[key as keyof SupportContacts])
  function edit(key: keyof SupportContacts, value: string) { setContacts(previous => ({ ...previous, [key]: value })); setError(''); onEditing() }
  async function save() {
    if (busy || loading || readOnly || !changed) return
    setBusy(true); setError('')
    try { await api<SupportContactsSettings>(endpoint, { ...contacts, version: settings.version }); onSaved() }
    catch (e) { setError(errorMessage(e)) } finally { setBusy(false) }
  }
  return <form className="support-settings-form" onSubmit={e => { e.preventDefault(); void save() }}>
    <FieldGroup>
      <Field><FieldLabel htmlFor="support-wechat">微信</FieldLabel><Input id="support-wechat" value={contacts.wechat} onChange={e => edit('wechat', e.target.value)} maxLength={64} autoComplete="off" placeholder="微信号" disabled={busy || readOnly} /></Field>
      <Field><FieldLabel htmlFor="support-qq">QQ</FieldLabel><Input id="support-qq" value={contacts.qq} onChange={e => edit('qq', e.target.value)} maxLength={20} inputMode="numeric" autoComplete="off" placeholder="QQ 号" disabled={busy || readOnly} /></Field>
      <Field><FieldLabel htmlFor="support-email">联系邮箱</FieldLabel><Input id="support-email" type="email" value={contacts.email} onChange={e => edit('email', e.target.value)} maxLength={254} autoComplete="off" placeholder="用于客户咨询的邮箱" disabled={busy || readOnly} /><FieldDescription>留空的联系方式不会显示。此邮箱仅用于联系支持。</FieldDescription></Field>
    </FieldGroup>
    <Feedback error={error} />
    {!readOnly && <Button type="submit" disabled={busy || loading || !changed}>{busy ? '保存中…' : '保存联系方式'}</Button>}
  </form>
}

export function SupportContactsPanel({ endpoint, revision = 0, title = '平台联系方式', description = '显示在平台商店的“联系支持”中。', readOnly = false, onSummaryChange }: { endpoint: string; revision?: number; title?: string; description?: string; readOnly?: boolean; onSummaryChange?: (summary: string) => void }) {
  const [localRevision, setLocalRevision] = useState(0); const [message, setMessage] = useState('')
  const { data, error, loading } = useResource<SupportContactsSettings>(endpoint, revision + localRevision)
  useEffect(() => {
    const channels = data ? [data.contacts.wechat && '微信', data.contacts.qq && 'QQ', data.contacts.email && '邮箱'].filter(Boolean) : []
    onSummaryChange?.(error ? '读取失败，展开后重试' : data ? channels.length ? `已设置${channels.join('、')}` : '未设置 · 添加客户联系渠道' : '正在读取…')
  }, [data, error, onSummaryChange])
  return <section className="admin-panel support-settings-panel">
    <div className="admin-panel-title"><h2>{title}</h2><Button type="button" variant="ghost" size="icon" aria-label="刷新联系方式" disabled={loading} onClick={() => { setMessage(''); setLocalRevision(value => value + 1) }}><RefreshCw size={15} /></Button></div>
    <p className="support-settings-description">{description}</p>
    <Feedback error={error} loading={loading} data={data} shape="compact" />
    {data && <ContactsForm key={endpoint + ':' + data.version} endpoint={endpoint} settings={data} loading={loading} readOnly={readOnly} onEditing={() => setMessage('')} onSaved={() => { setMessage('联系方式已保存'); setLocalRevision(value => value + 1) }} />}
    {message && <p className="support-settings-success" role="status"><Check size={14} />{message}</p>}
  </section>
}

export function SupportContactsDialog({ open, onOpenChange, contacts, name, error, onHelp, onRetry }: { open: boolean; onOpenChange: (open: boolean) => void; contacts?: SupportContacts; name: string; error?: string; onHelp: () => void; onRetry: () => void }) {
  const { t } = useStorefrontI18n()
  const channels = contacts ? [{ key: 'wechat', label: '微信', value: contacts.wechat }, { key: 'qq', label: 'QQ', value: contacts.qq }, { key: 'email', label: '邮箱', value: contacts.email }].filter(item => item.value) : []
  return <Dialog open={open} onOpenChange={onOpenChange}><DialogContent className="support-contacts-dialog motion-dialog">
    <DialogHeader><DialogTitle>{t('联系支持')}</DialogTitle><DialogDescription>{contacts ? name : t('正在读取商店联系方式…')}</DialogDescription></DialogHeader>
    <div className="support-contact-body">
      {!contacts ? <><Feedback error={error ? t(error) : undefined} loading={!error} shape="compact" />{error && <Button variant="outline" onClick={onRetry}>{t('重新读取')}</Button>}</> : channels.length ? <dl className="support-contact-list">{channels.map(item => <div className="support-contact-row" key={item.key}>
        <dt>{t(item.label)}</dt>
        <dd>{item.key === 'email' ? <a href={'mailto:' + encodeURIComponent(item.value)}>{item.value}</a> : item.value}</dd>
        <CopyButton className="support-contact-copy" variant="ghost" size="icon" iconOnly label={t('复制{channel}', { channel: t(item.label) })} title={t('复制{channel}', { channel: t(item.label) })} value={item.value} />
      </div>)}</dl> : <div className="support-contact-empty"><p>{t('商店暂未提供联系方式')}</p><span>{t('已有订单可在“我的订单”查看进度或申请售后。')}</span></div>}
    </div>
    <DialogFooter className="support-contact-footer"><Button className="support-help-button" variant="ghost" onClick={() => { onOpenChange(false); onHelp() }}><span>{t('查看常见问题')}</span><ArrowRight data-icon="inline-end" /></Button></DialogFooter>
  </DialogContent></Dialog>
}
