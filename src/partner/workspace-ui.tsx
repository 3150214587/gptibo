import { useState, type ReactNode } from 'react'
import './workspace.css'
import { ChevronDown } from 'lucide-react'
import { Button } from '@/components/ui/button'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Tabs,
  TabsContent,
  TabsIndicator,
  TabsList,
  TabsTrigger,
} from '@/components/ui/tabs'

/** Each panel mounts on first visit and stays mounted, preserving drafts. */
export function WorkspaceTabs({
  label,
  items,
  defaultValue,
  value,
  onValueChange,
}: {
  label: string
  items: { value: string; label: string; content: ReactNode }[]
  defaultValue?: string
  value?: string
  onValueChange?: (value: string) => void
}) {
  const initial = value ?? defaultValue ?? items[0]?.value ?? ''
  const [local, setLocal] = useState(initial)
  const [visited, setVisited] = useState<string[]>([initial])
  const active = value ?? local
  // Record controlled changes as well as clicks before a panel can be left.
  if (!visited.includes(active)) setVisited([...visited, active])
  return (
    <Tabs
      className="partner-page-tabs"
      value={active}
      onValueChange={(next) => {
        const key = String(next)
        setLocal(key)
        setVisited((previous) =>
          previous.includes(key) ? previous : [...previous, key],
        )
        onValueChange?.(key)
      }}
    >
      <div className="partner-tab-scroll">
        <TabsList variant="line" aria-label={label}>
          {items.map((item) => (
            <TabsTrigger key={item.value} value={item.value}>
              {item.label}
            </TabsTrigger>
          ))}
          <TabsIndicator className="partner-tab-indicator" />
        </TabsList>
      </div>
      {items.map((item) => (
        <TabsContent key={item.value} value={item.value} keepMounted>
          {(visited.includes(item.value) || active === item.value) &&
            item.content}
        </TabsContent>
      ))}
    </Tabs>
  )
}

export function WorkspaceSelect({
  label,
  value,
  onValueChange,
  options,
  disabled,
  id,
}: {
  label: string
  value: string
  onValueChange: (value: string) => void
  options: { value: string; label: string }[]
  disabled?: boolean
  id?: string
}) {
  return (
    <Select
      value={value}
      items={options}
      disabled={disabled}
      onValueChange={(next) => {
        if (next !== null) onValueChange(next)
      }}
    >
      <SelectTrigger id={id} aria-label={label} className="partner-select">
        <SelectValue />
      </SelectTrigger>
      <SelectContent
        className="motion-popover partner-select-popup"
        alignItemWithTrigger={false}
      >
        {options.map((option) => (
          <SelectItem key={option.value} value={option.value}>
            {option.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

export function WorkspaceDisclosure({
  title,
  children,
  defaultOpen = false,
  className = '',
}: {
  title: ReactNode
  children: ReactNode
  defaultOpen?: boolean
  className?: string
}) {
  return (
    <Collapsible
      defaultOpen={defaultOpen}
      className={`partner-disclosure motion-collapsible ${className}`}
    >
      <CollapsibleTrigger
        render={<Button variant="ghost" />}
        className="partner-disclosure-trigger"
      >
        {title}
        <ChevronDown size={15} aria-hidden="true" />
      </CollapsibleTrigger>
      <CollapsibleContent keepMounted>
        <div className="partner-disclosure-body">{children}</div>
      </CollapsibleContent>
    </Collapsible>
  )
}
