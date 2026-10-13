import { Button } from '@/components/ui/button'
import { Collapsible, CollapsibleTrigger, CollapsibleContent } from '@/components/ui/collapsible'
import { useId, useState, type ReactNode } from 'react'
import { ArrowUpRight, Bell, ChevronDown, Globe, Megaphone, Palette, MessagesSquare, type LucideIcon } from 'lucide-react'
import { SupportContactsPanel } from '@/components/support-contacts'
import { AnnouncementSettingsPanel } from '@/components/announcement-settings'
import { NotificationSettings } from './NotificationSettings'
import { BrandingSettings } from './BrandingSettings'
import { CustomDomain } from './CustomDomain'
import './store-settings.css'

type Section = 'branding' | 'domain' | 'contacts' | 'announcement' | 'notifications'

function SettingsSection({ title, summary, icon: Icon, open, onToggle, children }: { title: string; summary: string; icon: LucideIcon; open: boolean; onToggle: () => void; children: ReactNode }) {
  const id = useId()
  return <Collapsible className="store-settings-section motion-collapsible" open={open} onOpenChange={onToggle} data-open={open}>
    <h3 className="store-settings-section-title">
      <CollapsibleTrigger render={<Button variant="ghost" />} id={id + '-trigger'} className="store-settings-trigger" aria-expanded={open} aria-controls={id + '-panel'}>
        <Icon className="store-settings-icon" size={20} strokeWidth={1.6} aria-hidden="true" />
        <span className="store-settings-label"><strong>{title}</strong><span>{summary}</span></span>
        <span className="store-settings-action" aria-hidden="true">{open ? '收起' : '设置'}</span><ChevronDown className="store-settings-chevron" size={17} aria-hidden="true" />
      </CollapsibleTrigger>
    </h3>
    {/* Keep forms mounted so collapsing a section never discards a draft. */}
    <CollapsibleContent keepMounted id={id + '-panel'} role="region" aria-labelledby={id + '-trigger'} className="store-settings-content">{children}</CollapsibleContent>
  </Collapsible>
}

export function StoreSettings({ prefix, revision, readOnly, storeUrl, active = true, accountOnly = false }: { prefix: string; revision: number; readOnly: boolean; storeUrl: string; active?: boolean; accountOnly?: boolean }) {
  const [open, setOpen] = useState<Section | null>(null)
  const [branding, setBranding] = useState('正在读取…')
  const [domain, setDomain] = useState('正在读取…')
  const [contacts, setContacts] = useState('正在读取…')
  const [announcement, setAnnouncement] = useState('正在读取…')
  const [notifications, setNotifications] = useState('正在读取…')
  const toggle = (section: Section) => setOpen(current => current === section ? null : section)
  return <div className="partner-store-settings">
    <div className="partner-section-heading">
      <div><h2>{accountOnly ? '账号设置' : '商店设置'}</h2><p>{accountOnly ? '通知设置应用于账号下所有商店。店铺形象、联系方式和域名请到“我的商店”管理。' : '店铺形象、域名和客户沟通，都在这里管理。'}</p></div>
      <a href={storeUrl} target="_blank" rel="noreferrer">预览商店 <ArrowUpRight size={14} /></a>
    </div>
    <div className="store-settings-list">
      {!accountOnly && <><SettingsSection title="店铺形象" summary={branding} icon={Palette} open={open === 'branding'} onToggle={() => toggle('branding')}>
        <BrandingSettings endpoint={prefix + '/branding'} revision={revision} readOnly={readOnly} onSummaryChange={setBranding} />
      </SettingsSection>
      <SettingsSection title="自定义域名" summary={domain} icon={Globe} open={open === 'domain'} onToggle={() => toggle('domain')}>
        <CustomDomain prefix={prefix} readOnly={readOnly} polling={active && open === 'domain'} onSummaryChange={setDomain} />
      </SettingsSection>
      <SettingsSection title="联系方式" summary={contacts} icon={MessagesSquare} open={open === 'contacts'} onToggle={() => toggle('contacts')}>
        <SupportContactsPanel endpoint={prefix + '/support-contacts'} revision={revision} title="联系方式" description="客户在你的专属商店点击“联系支持”时显示。" readOnly={readOnly} onSummaryChange={setContacts} />
      </SettingsSection>
      <SettingsSection title="商店公告" summary={announcement} icon={Megaphone} open={open === 'announcement'} onToggle={() => toggle('announcement')}>
        <AnnouncementSettingsPanel endpoint={prefix + '/announcement'} revision={revision} readOnly={readOnly} onSummaryChange={setAnnouncement} />
      </SettingsSection>
      </>}<SettingsSection title="订单通知" summary={notifications} icon={Bell} open={open === 'notifications'} onToggle={() => toggle('notifications')}>
        <NotificationSettings endpoint={prefix + '/notification-settings'} revision={revision} readOnly={readOnly} onSummaryChange={setNotifications} />
      </SettingsSection>
    </div>
    <p className="store-settings-note">{readOnly ? '当前为只读预览，设置由代理本人修改。' : '各项设置分别保存。收起分组不会清空本页未保存的内容。'}</p>
  </div>
}
