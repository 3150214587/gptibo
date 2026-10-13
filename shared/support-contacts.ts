export type SupportContacts = { wechat: string; qq: string; email: string }
export type SupportContactsSettings = { contacts: SupportContacts; version: number }

export const emptySupportContacts: SupportContacts = { wechat: '', qq: '', email: '' }
