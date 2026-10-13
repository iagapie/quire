import { type ReactNode } from 'react'
import { ChevronRight } from 'lucide-react'
import { create } from 'zustand'
import { cn } from '../lib/utils'
import { ContextMenu, type MenuEntry } from './ui'

// Rows of the sidebar tree, and the tables (or collections) picked in it.

/** Tables picked together (⌘/Ctrl-click, ⇧-click, ⌘/Ctrl+A), always within one schema. */
export interface Marked {
  sessionId: string
  db: string
  schema: string
  tables: string[]
}

interface TreeSelection {
  /** The row with the cursor. */
  focus: string
  marked: Marked | null
  /** The table a ⇧-click range starts from. */
  anchor: string
  setFocus(k: string): void
  mark(m: Marked | null, anchor?: string, focus?: string): void
}

export const useSel = create<TreeSelection>()((set) => ({
  focus: '',
  marked: null,
  anchor: '',
  setFocus: (focus) => set({ focus }),
  mark: (marked, anchor, focus) => set((s) => ({ marked, anchor: anchor ?? s.anchor, focus: focus ?? s.focus })),
}))

export const same = (m: Marked | null, sessionId: string, db: string, schema: string) => !!m && m.sessionId === sessionId && m.db === db && m.schema === schema

/** The tables marked in a schema, when more than one is. */
export function markedIn(m: Marked | null, sessionId: string, db: string, schema: string): string[] | null {
  return same(m, sessionId, db, schema) && m!.tables.length > 1 ? m!.tables : null
}

export function Row({
  depth,
  expandable,
  open,
  onToggle,
  onOpen,
  icon,
  label,
  trailing,
  menu,
  k,
  marked,
  onPick,
  onSelectAll,
  onContext,
  muted,
  title,
}: {
  depth: number
  expandable?: boolean
  open?: boolean
  onToggle?: () => void
  onOpen?: () => void
  icon: ReactNode
  label: ReactNode
  trailing?: ReactNode
  menu?: MenuEntry[]
  /** Identifies the row for the cursor. */
  k: string
  /** Part of a multiple selection. */
  marked?: boolean
  /** Handles clicks (with modifiers); rows without it clear the multiple selection. */
  onPick?: (e: React.MouseEvent) => void
  /** ⌘/Ctrl+A on the row. */
  onSelectAll?: () => void
  onContext?: () => void
  muted?: boolean
  title?: string
}) {
  const selected = useSel((s) => s.focus === k)
  const row = (
    <div
      role="treeitem"
      aria-expanded={expandable ? !!open : undefined}
      aria-selected={selected}
      tabIndex={0}
      title={title}
      onClick={(e) => {
        useSel.getState().setFocus(k)
        if (onPick) onPick(e)
        else useSel.getState().mark(null)
        if (expandable && !onOpen) onToggle?.()
      }}
      onContextMenu={onContext}
      onDoubleClick={() => (onOpen ? onOpen() : undefined)}
      onKeyDown={(e) => {
        if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'a' && onSelectAll) {
          e.preventDefault()
          onSelectAll()
        } else if (e.key === 'Escape') useSel.getState().mark(null)
        else if (e.key === 'Enter') (onOpen ?? onToggle)?.()
        else if (e.key === 'ArrowRight' && expandable && !open) onToggle?.()
        else if (e.key === 'ArrowLeft' && expandable && open) onToggle?.()
        else if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
          e.preventDefault()
          const items = Array.from(document.querySelectorAll<HTMLElement>('[role="treeitem"]'))
          const i = items.indexOf(e.currentTarget)
          items[e.key === 'ArrowDown' ? i + 1 : i - 1]?.focus()
        }
      }}
      onFocus={() => useSel.getState().setFocus(k)}
      className={cn(
        'group flex h-6.5 items-center gap-1 rounded-md pr-1.5 text-[13px] outline-none',
        marked ? 'bg-accent-soft text-fg' : selected ? 'bg-active text-fg' : 'hover:bg-hover',
        'focus-visible:ring-1 focus-visible:ring-accent',
        muted && 'text-muted',
      )}
      style={{ paddingLeft: 4 + depth * 14 }}
    >
      <span
        className={cn('flex size-4 shrink-0 items-center justify-center text-faint', !expandable && 'invisible')}
        onClick={(e) => {
          if (onOpen) {
            e.stopPropagation()
            onToggle?.()
          }
        }}
      >
        <ChevronRight size={13} className={cn('transition-transform', open && 'rotate-90')} />
      </span>
      <span className="flex shrink-0 items-center text-muted">{icon}</span>
      <span className="ml-1 min-w-0 flex-1 truncate">{label}</span>
      {trailing}
    </div>
  )
  return menu ? <ContextMenu items={menu}>{row}</ContextMenu> : row
}

export function Message({
  depth,
  children,
  tone = 'muted',
  onRetry,
  action = 'Retry',
}: {
  depth: number
  children: ReactNode
  tone?: 'muted' | 'error'
  onRetry?: () => void
  action?: string
}) {
  return (
    <div
      className={cn('flex items-start gap-2 py-1 pr-2 text-xs', tone === 'error' ? 'text-danger' : 'text-faint')}
      style={{ paddingLeft: 24 + depth * 14 }}
    >
      <span className="selectable min-w-0 flex-1 break-words">{children}</span>
      {onRetry && (
        <button className="shrink-0 text-accent hover:underline" onClick={onRetry}>
          {action}
        </button>
      )}
    </div>
  )
}

