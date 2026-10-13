import { useId, type CSSProperties } from 'react'
import { Mail, MessageCircle, MessagesSquare } from 'lucide-react'
import { CopyButton } from './copy-button'
import type { SupportContacts } from '../../shared/support-contacts'
import '@/styles/recharge-success-contacts.css'

export function RechargeSuccessContacts({ contacts, name }: { contacts?: SupportContacts; name?: string | null }) {
  const headingId = useId()
  const channels = contacts ? [
    { key: 'wechat', label: '微信', value: contacts.wechat, icon: MessageCircle },
    { key: 'qq', label: 'QQ', value: contacts.qq, icon: MessagesSquare },
    { key: 'email', label: '邮箱', value: contacts.email, icon: Mail },
  ].filter(channel => channel.value.trim()) : []
  if (!name || !channels.length) return null
  return <section className="success-contacts" aria-labelledby={headingId}>
    <div className="success-contacts-surface">
      <header className="success-contacts-heading"><h4 id={headingId}>客服联系方式</h4></header>
      <dl className="success-contact-channels">
        {channels.map((channel, index) => {
          const Icon = channel.icon
          return <div key={channel.key} className="success-contact-channel" style={{ '--contact-delay': `${600 + index * 150}ms` } as CSSProperties}>
            <dt><Icon size={14} aria-hidden="true" />{channel.label}</dt>
            <dd>{channel.key === 'email' ? <a href={'mailto:' + encodeURIComponent(channel.value)}>{channel.value}</a> : channel.value}</dd>
            <CopyButton iconOnly variant="ghost" size="icon" value={channel.value} label={'复制' + channel.label} title={'复制' + channel.label} />
          </div>
        })}
      </dl>
    </div>
  </section>
}
