import { useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { api, errorMessage } from '@/lib/api'
import { Feedback } from './shared'
import type { PayoutAccount } from './model'

export function PayoutAccountDialog({ account, onClose, onSaved }: {
  account: PayoutAccount
  onClose: () => void
  onSaved: () => void
}) {
  const [phone, setPhone] = useState(account.phone)
  const [name, setName] = useState(account.name)
  const [version] = useState(account.version)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const submitting = useRef(false)
  const valid = /^1[3-9][0-9]{9}$/.test(phone.trim()) && Boolean(name.trim()) && name.trim().length <= 50

  async function save() {
    if (submitting.current || !valid) return
    submitting.current = true
    setBusy(true)
    setError('')
    try {
      await api('/api/partner/payout-account', { phone: phone.trim(), name: name.trim(), version })
      onSaved()
      onClose()
    } catch (e) {
      setError(errorMessage(e))
    } finally {
      submitting.current = false
      setBusy(false)
    }
  }

  return <Dialog open onOpenChange={open => { if (!open && !submitting.current) onClose() }}>
    <DialogContent className="motion-dialog partner-dialog" showCloseButton={!busy}>
      <DialogHeader>
        <DialogTitle>{account.phone ? '修改支付宝收款信息' : '绑定支付宝收款信息'}</DialogTitle>
        <DialogDescription>绑定后提现自动使用此账户，无需每次重复填写。</DialogDescription>
      </DialogHeader>
      <form className="partner-form" onSubmit={e => { e.preventDefault(); void save() }}>
        <FieldGroup>
          <Field>
            <FieldLabel htmlFor="alipay-phone">支付宝手机号 <span aria-hidden="true">*</span></FieldLabel>
            <Input id="alipay-phone" type="tel" inputMode="tel" autoComplete="tel" required pattern="1[3-9][0-9]{9}" maxLength={11} value={phone} onChange={e => setPhone(e.target.value)} disabled={busy} placeholder="输入支付宝绑定的 11 位手机号" />
          </Field>
          <Field>
            <FieldLabel htmlFor="alipay-name">收款人姓名 <span aria-hidden="true">*</span></FieldLabel>
            <Input id="alipay-name" autoComplete="name" required maxLength={50} value={name} onChange={e => setName(e.target.value)} disabled={busy} placeholder="输入支付宝实名认证姓名" />
            <FieldDescription>请核对手机号和真实姓名，平台将按此信息转账。修改仅用于之后的新申请。</FieldDescription>
          </Field>
        </FieldGroup>
        <Feedback error={error} />
        <Button type="submit" disabled={busy || !valid}>{busy ? '保存中…' : '保存收款信息'}</Button>
      </form>
    </DialogContent>
  </Dialog>
}
