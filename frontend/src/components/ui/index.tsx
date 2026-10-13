import * as Dialog from '@radix-ui/react-dialog'
import * as Tooltip from '@radix-ui/react-tooltip'
import * as CM from '@radix-ui/react-context-menu'
import * as DM from '@radix-ui/react-dropdown-menu'
import { X } from 'lucide-react'
import { forwardRef, type ButtonHTMLAttributes, type InputHTMLAttributes, type ReactNode } from 'react'
import { cn } from '../../lib/utils'

type ButtonVariant = 'primary' | 'secondary' | 'ghost' | 'danger'

export const Button = forwardRef<
  HTMLButtonElement,
  ButtonHTMLAttributes<HTMLButtonElement> & { variant?: ButtonVariant; size?: 'sm' | 'md' }
>(({ variant = 'secondary', size = 'md', className, ...props }, ref) => (
  <button
    ref={ref}
    className={cn(
      'inline-flex items-center justify-center gap-1.5 rounded-md font-medium whitespace-nowrap transition-colors disabled:pointer-events-none disabled:opacity-45',
      size === 'sm' ? 'h-6 px-2 text-xs' : 'h-7.5 px-3 text-[13px]',
      variant === 'primary' && 'bg-accent text-accent-fg hover:brightness-110',
      variant === 'secondary' && 'border border-line-strong bg-raised text-fg shadow-xs hover:bg-hover',
      variant === 'ghost' && 'text-muted hover:bg-hover hover:text-fg',
      variant === 'danger' && 'bg-danger text-white hover:brightness-110',
      className,
    )}
    {...props}
  />
))
Button.displayName = 'Button'

export function Tip({ label, children, side = 'bottom' }: { label: ReactNode; children: ReactNode; side?: 'top' | 'bottom' | 'left' | 'right' }) {
  return (
    <Tooltip.Root>
      <Tooltip.Trigger asChild>{children}</Tooltip.Trigger>
      <Tooltip.Portal>
        <Tooltip.Content
          side={side}
          sideOffset={6}
          className="z-50 rounded-md border border-line bg-raised px-2 py-1 text-xs text-fg shadow-lg"
        >
          {label}
        </Tooltip.Content>
      </Tooltip.Portal>
    </Tooltip.Root>
  )
}

export const IconButton = forwardRef<
  HTMLButtonElement,
  ButtonHTMLAttributes<HTMLButtonElement> & { label: ReactNode; active?: boolean; tone?: 'default' | 'accent' | 'danger' }
>(({ label, active, tone = 'default', className, children, ...props }, ref) => {
  const btn = (
    <button
      ref={ref}
      aria-label={typeof label === 'string' ? label : undefined}
      className={cn(
        'inline-flex size-7 shrink-0 items-center justify-center rounded-md transition-colors disabled:pointer-events-none disabled:opacity-35',
        tone === 'default' && 'text-muted hover:bg-hover hover:text-fg',
        tone === 'accent' && 'text-accent hover:bg-accent-soft',
        tone === 'danger' && 'text-danger hover:bg-danger-soft',
        active && 'bg-active text-fg',
        className,
      )}
      {...props}
    >
      {children}
    </button>
  )
  return <Tip label={label}>{btn}</Tip>
})
IconButton.displayName = 'IconButton'

export const Input = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement> & { mono?: boolean }>(
  ({ className, mono, ...props }, ref) => (
    <input
      ref={ref}
      spellCheck={false}
      autoComplete="off"
      autoCorrect="off"
      autoCapitalize="off"
      className={cn(
        'h-7.5 w-full min-w-0 rounded-md border border-line-strong bg-sunken px-2.5 text-[13px] text-fg placeholder:text-faint',
        'focus:border-accent focus:outline-none focus:ring-2 focus:ring-accent-soft disabled:opacity-50',
        mono && 'font-mono text-[12px]',
        className,
      )}
      {...props}
    />
  ),
)
Input.displayName = 'Input'

export function Kbd({ children }: { children: ReactNode }) {
  return <kbd className="rounded border border-line bg-sunken px-1 font-sans text-[10px] text-muted">{children}</kbd>
}

export function Modal({
  open,
  onOpenChange,
  title,
  description,
  children,
  footer,
  width = 'max-w-lg',
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: ReactNode
  description?: ReactNode
  children: ReactNode
  footer?: ReactNode
  width?: string
}) {
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-40 bg-black/40 backdrop-blur-[1px]" />
        <Dialog.Content
          className={cn(
            'fixed top-1/2 left-1/2 z-50 flex max-h-[88vh] w-[calc(100vw-48px)] -translate-x-1/2 -translate-y-1/2 flex-col rounded-xl border border-line bg-panel shadow-2xl',
            width,
          )}
          onOpenAutoFocus={(e) => {
            const first = (e.currentTarget as HTMLElement).querySelector<HTMLElement>('[data-autofocus]')
            if (first) {
              e.preventDefault()
              first.focus()
            }
          }}
        >
          <div className="flex items-start justify-between gap-4 border-b border-line px-5 py-3.5">
            <div>
              <Dialog.Title className="text-[14px] font-semibold">{title}</Dialog.Title>
              {description ? (
                <Dialog.Description className="mt-0.5 text-xs text-muted">{description}</Dialog.Description>
              ) : (
                <Dialog.Description className="sr-only">{typeof title === 'string' ? title : 'Dialog'}</Dialog.Description>
              )}
            </div>
            <Dialog.Close asChild>
              <IconButton label="Close">
                <X size={15} />
              </IconButton>
            </Dialog.Close>
          </div>
          <div className="min-h-0 flex-1 overflow-auto">{children}</div>
          {footer && <div className="flex items-center justify-end gap-2 border-t border-line px-5 py-3">{footer}</div>}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  )
}

const menuContent = 'z-50 min-w-48 rounded-lg border border-line bg-raised p-1 text-[13px] shadow-xl'
const menuItem =
  'flex h-7 items-center gap-2 rounded-md px-2 text-fg outline-none data-[disabled]:opacity-40 data-[highlighted]:bg-accent data-[highlighted]:text-accent-fg'

export interface MenuEntry {
  label?: string
  icon?: ReactNode
  shortcut?: string
  onSelect?: () => void
  danger?: boolean
  disabled?: boolean
  separator?: boolean
  hidden?: boolean
}

function entries(items: MenuEntry[], Item: typeof CM.Item | typeof DM.Item, Sep: typeof CM.Separator | typeof DM.Separator) {
  return items
    .filter((i) => !i.hidden)
    .map((i, idx) =>
      i.separator ? (
        <Sep key={idx} className="my-1 h-px bg-line" />
      ) : (
        <Item
          key={idx}
          disabled={i.disabled}
          onSelect={i.onSelect}
          className={cn(menuItem, i.danger && 'text-danger data-[highlighted]:bg-danger data-[highlighted]:text-white')}
        >
          <span className="flex w-4 justify-center opacity-80">{i.icon}</span>
          <span className="flex-1">{i.label}</span>
          {i.shortcut && <span className="text-xs opacity-60">{i.shortcut}</span>}
        </Item>
      ),
    )
}

export function ContextMenu({ items, children, onOpenChange }: { items: MenuEntry[]; children: ReactNode; onOpenChange?: (open: boolean) => void }) {
  return (
    <CM.Root onOpenChange={onOpenChange}>
      <CM.Trigger asChild>{children}</CM.Trigger>
      <CM.Portal>
        <CM.Content className={menuContent}>{entries(items, CM.Item, CM.Separator)}</CM.Content>
      </CM.Portal>
    </CM.Root>
  )
}

export function DropdownMenu({ items, children, align = 'start' }: { items: MenuEntry[]; children: ReactNode; align?: 'start' | 'end' }) {
  return (
    <DM.Root>
      <DM.Trigger asChild>{children}</DM.Trigger>
      <DM.Portal>
        <DM.Content align={align} sideOffset={4} className={menuContent}>
          {entries(items, DM.Item, DM.Separator)}
        </DM.Content>
      </DM.Portal>
    </DM.Root>
  )
}

export function Spinner({ size = 14, className }: { size?: number; className?: string }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" className={cn('animate-spin', className)} aria-hidden>
      <circle cx="12" cy="12" r="9" stroke="currentColor" strokeOpacity="0.2" strokeWidth="3" fill="none" />
      <path d="M21 12a9 9 0 0 0-9-9" stroke="currentColor" strokeWidth="3" strokeLinecap="round" fill="none" />
    </svg>
  )
}

export function Field({ label, hint, children, className }: { label: ReactNode; hint?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <label className={cn('flex flex-col gap-1.5', className)}>
      <span className="text-xs font-medium text-muted">{label}</span>
      {children}
      {hint && <span className="text-[11px] text-faint">{hint}</span>}
    </label>
  )
}

export function Switch({
  checked,
  onChange,
  label,
  hint,
  disabled,
}: {
  checked: boolean
  onChange: (v: boolean) => void
  label: ReactNode
  hint?: ReactNode
  disabled?: boolean
}) {
  return (
    <label className={cn('flex items-start gap-2.5 py-0.5', disabled && 'opacity-50')}>
      <button
        type="button"
        role="switch"
        disabled={disabled}
        aria-checked={checked}
        onClick={() => onChange(!checked)}
        className={cn(
          'relative mt-0.5 h-4.5 w-8 shrink-0 rounded-full transition-colors',
          checked ? 'bg-accent' : 'bg-line-strong',
        )}
      >
        <span
          className={cn(
            'absolute top-0.5 left-0.5 size-3.5 rounded-full bg-white shadow transition-transform',
            checked && 'translate-x-3.5',
          )}
        />
      </button>
      <span className="flex flex-col">
        <span className="text-[13px]">{label}</span>
        {hint && <span className="text-[11px] text-faint">{hint}</span>}
      </span>
    </label>
  )
}

export function Select({
  value,
  onChange,
  options,
  className,
  disabled,
}: {
  value: string
  onChange: (v: string) => void
  options: { value: string; label: string }[]
  className?: string
  disabled?: boolean
}) {
  return (
    <select
      value={value}
      disabled={disabled}
      onChange={(e) => onChange(e.target.value)}
      className={cn(
        'h-7.5 rounded-md border border-line-strong bg-sunken px-2 text-[13px] text-fg focus:border-accent focus:outline-none disabled:opacity-60',
        className,
      )}
    >
      {options.map((o) => (
        <option key={o.value} value={o.value}>
          {o.label}
        </option>
      ))}
    </select>
  )
}
