import { useState, type ComponentProps } from 'react'
import { Autocomplete } from '@base-ui/react/autocomplete'
import { Mail } from 'lucide-react'
import { Input } from '@/components/ui/input'
import { emailSuggestions } from '@/lib/email'
import '@/styles/email-autocomplete.css'

type Props = Omit<ComponentProps<typeof Input>, 'value' | 'onChange'> & {
  value: string
  onValueChange: (value: string) => void
}

export function EmailAutocomplete({ value, onValueChange, disabled, ...props }: Props) {
  const [open, setOpen] = useState(false)
  const suggestions = emailSuggestions(value)
  return <Autocomplete.Root items={suggestions} filter={null} value={value} onValueChange={onValueChange}
    open={open && !disabled && suggestions.length > 0} onOpenChange={setOpen} disabled={disabled} autoHighlight openOnInputClick>
    <div className="purchase-email-input">
      <Mail size={18} aria-hidden="true" />
      <Autocomplete.Input render={<Input />} {...props} type="email" inputMode="email" autoComplete="email" autoCapitalize="none" spellCheck={false} />
    </div>
    <Autocomplete.Portal>
      <Autocomplete.Positioner className="email-suggestions-positioner" sideOffset={6} align="start">
        <Autocomplete.Popup className="email-suggestions-popup">
          <Autocomplete.List className="email-suggestions-list">
            {(email: string) => <Autocomplete.Item key={email} value={email} className="email-suggestion">
              <span>{email.slice(0, email.indexOf('@') + 1)}</span><strong>{email.slice(email.indexOf('@') + 1)}</strong>
            </Autocomplete.Item>}
          </Autocomplete.List>
        </Autocomplete.Popup>
      </Autocomplete.Positioner>
    </Autocomplete.Portal>
  </Autocomplete.Root>
}
