import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { Copy, CopyPlus, Eraser, KeyRound, Pencil, Trash2 } from 'lucide-react'
import { toast } from 'sonner'
import type { Column } from '../../lib/api'
import { saveWidth, savedWidths } from '../../lib/colwidths'
import { cn, copyText, mod, shift } from '../../lib/utils'
import { ContextMenu, type MenuEntry } from '../ui'
import { ColumnResizer } from './ColumnResizer'

export type Cell = string | null

/** A row of the grid: the page row it came from (or -1 for a new row) and its values. */
export interface GridRow {
  key: string
  /** Index in the page, -1 for rows being inserted. */
  index: number
  values: Cell[]
  /** Columns changed by pending edits. */
  changed?: Set<number>
  inserted?: boolean
  deleted?: boolean
}

export type CopyFormat = 'tsv' | 'csv' | 'json' | 'insert' | 'markdown'

export interface GridActions {
  /** Columns that can be edited, by index. */
  writable: Set<number>
  keyColumns: Set<number>
  canEdit: boolean
  onEdit?: (row: GridRow, col: number, value: Cell) => void
  onDuplicate?: (rows: GridRow[]) => void
  onDelete?: (rows: GridRow[]) => void
  onCopy: (rows: GridRow[], format: CopyFormat) => void
  onCopyValue: (v: Cell) => void
}

const ROW_H = 26
const OVERSCAN = 12
const MIN_W = 48

/** A name for menu labels, quoted and cut short. */
export const quoted = (name: string) => `“${name.length > 32 ? name.slice(0, 31) + '…' : name}”`

function autoWidths(cols: Column[], rows: GridRow[]): number[] {
  const sample = rows.slice(0, 80)
  return cols.map((c, i) => {
    let w = Math.max(c.name.length * 7.4 + 26, c.type.length * 6.2 + 26)
    for (const r of sample) {
      const v = r.values[i]
      w = Math.max(w, Math.min(v === null ? 32 : v.length, 60) * 7.2 + 24)
    }
    return Math.round(Math.min(Math.max(w, 64), 420))
  })
}

/** Auto widths, with the widths saved for this table in place of them. */
function initialWidths(cols: Column[], rows: GridRow[], widthKey: string): { widths: number[]; manual: Set<number> } {
  const saved = savedWidths(widthKey)
  const manual = new Set<number>()
  const widths = autoWidths(cols, rows).map((w, i) => {
    const s = saved[cols[i].name]
    if (s === undefined) return w
    manual.add(i)
    return Math.max(MIN_W, s)
  })
  return { widths, manual }
}

interface CellRef {
  key: string
  col: number
}
const isCell = (c: CellRef | null, key: string, col: number) => c?.key === key && c.col === col

export function DataGrid({
  columns,
  rows,
  offset,
  selected,
  onSelect,
  actions,
  widthKey = '',
}: {
  columns: Column[]
  rows: GridRow[]
  offset: number
  selected: Set<string>
  onSelect: (s: Set<string>) => void
  actions: GridActions
  /** Where resized column widths are saved (see widthKey in lib/colwidths); '' keeps them for this result only. */
  widthKey?: string
}) {
  const box = useRef<HTMLDivElement>(null)
  const anchor = useRef<number | null>(null)
  const [scroll, setScroll] = useState({ top: 0, height: 400 })
  const [editing, setEditing] = useState<CellRef | null>(null)
  // The cell last clicked, and the one a context menu is open on.
  const [active, setActive] = useState<CellRef | null>(null)
  const [menuAt, setMenuAt] = useState<CellRef | null>(null)
  const [colW, setColW] = useState<number[]>([])
  // Columns the user resized keep their width.
  const manual = useRef<Set<number>>(new Set())
  const sig = columns.map((c) => c.name).join('\u0000')
  // Before paint, so the first frame already has the saved widths.
  useLayoutEffect(() => {
    const next = initialWidths(columns, rows, widthKey)
    manual.current = next.manual
    setColW(next.widths)
  }, [sig, widthKey]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    setActive(null)
    setMenuAt(null)
  }, [sig, offset])
  // New pages may hold wider values: columns grow, never shrink, while browsing.
  useEffect(() => {
    const next = autoWidths(columns, rows)
    setColW((cur) => (cur.length === next.length && next.some((w, i) => w > cur[i] && !manual.current.has(i)) ? cur.map((w, i) => (manual.current.has(i) ? w : Math.max(w, next[i]))) : cur))
  }, [rows, columns])

  useLayoutEffect(() => {
    const el = box.current
    if (!el) return
    const update = () => setScroll({ top: el.scrollTop, height: el.clientHeight })
    update()
    const ro = new ResizeObserver(update)
    ro.observe(el)
    return () => ro.disconnect()
  }, [])

  const first = Math.max(0, Math.floor(scroll.top / ROW_H) - OVERSCAN)
  const last = Math.min(rows.length, Math.ceil((scroll.top + scroll.height) / ROW_H) + OVERSCAN)
  const total = useMemo(() => colW.reduce((a, b) => a + b, 0), [colW])
  const numW = Math.max(44, String(offset + rows.length).length * 8 + 18)

  const select = (i: number, e: React.MouseEvent) => {
    const key = rows[i].key
    if (e.shiftKey && anchor.current !== null) {
      const [a, b] = [Math.min(anchor.current, i), Math.max(anchor.current, i)]
      onSelect(new Set(rows.slice(a, b + 1).map((r) => r.key)))
      return
    }
    if (e.metaKey || e.ctrlKey) {
      const next = new Set(selected)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      onSelect(next)
    } else onSelect(new Set([key]))
    anchor.current = i
  }

  const targets = (row: GridRow) => (selected.has(row.key) && selected.size > 1 ? rows.filter((r) => selected.has(r.key)) : [row])

  const rowName = (row: GridRow) => (row.inserted ? 'new row' : `row #${offset + row.index + 1}`)

  const menu = (row: GridRow, col: number): MenuEntry[] => {
    const sel = targets(row)
    const many = sel.length > 1
    const n = many ? ` ${sel.length} rows` : ` ${rowName(row)}`
    const editable = actions.canEdit && actions.writable.has(col) && !row.deleted
    const name = columns[col].name
    return [
      { label: `Copy value of ${quoted(name)}`, icon: <Copy size={14} />, onSelect: () => actions.onCopyValue(row.values[col]) },
      { label: 'Copy column name', icon: <Copy size={14} />, onSelect: () => void copyText(name).then(() => toast.success('Column name copied')) },
      { separator: true },
      { label: `Copy${n} as TSV`, icon: <Copy size={14} />, shortcut: `${mod}C`, onSelect: () => actions.onCopy(sel, 'tsv') },
      { label: `Copy${n} as CSV`, icon: <Copy size={14} />, onSelect: () => actions.onCopy(sel, 'csv') },
      { label: `Copy${n} as JSON`, icon: <Copy size={14} />, shortcut: `${shift}${mod}C`, onSelect: () => actions.onCopy(sel, 'json') },
      { label: `Copy${n} as INSERT`, icon: <Copy size={14} />, onSelect: () => actions.onCopy(sel, 'insert') },
      { label: `Copy${n} as Markdown`, icon: <Copy size={14} />, onSelect: () => actions.onCopy(sel, 'markdown') },
      { separator: true, hidden: !actions.canEdit },
      { label: 'Edit cell', icon: <Pencil size={14} />, shortcut: '↵', hidden: !actions.canEdit, disabled: !editable, onSelect: () => setEditing({ key: row.key, col }) },
      { label: 'Set to NULL', icon: <Eraser size={14} />, hidden: !actions.canEdit, disabled: !editable || row.values[col] === null, onSelect: () => actions.onEdit?.(row, col, null) },
      { label: `Duplicate${n}`, icon: <CopyPlus size={14} />, hidden: !actions.canEdit, onSelect: () => actions.onDuplicate?.(sel) },
      { label: `Delete${n}`, icon: <Trash2 size={14} />, danger: true, hidden: !actions.canEdit, onSelect: () => actions.onDelete?.(sel) },
    ]
  }

  // Enter edits the cell of a single selected row, starting with the first writable column.
  useEffect(() => {
    const el = box.current
    if (!el) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Enter' || editing || !actions.canEdit || selected.size !== 1) return
      if ((e.target as HTMLElement).tagName === 'INPUT') return
      const row = rows.find((r) => selected.has(r.key))
      const col = [...actions.writable][0]
      if (row && col !== undefined) {
        e.preventDefault()
        setEditing({ key: row.key, col })
      }
    }
    el.addEventListener('keydown', onKey)
    return () => el.removeEventListener('keydown', onKey)
  }, [rows, selected, editing, actions])

  const resize = (i: number, w: number) => setColW((cur) => cur.map((x, k) => (k === i ? w : x)))
  const commitWidth = (i: number, w: number) => {
    manual.current.add(i)
    saveWidth(widthKey, columns[i].name, w)
  }
  // Double-clicking the edge gives the column its automatic width back.
  const resetWidth = (i: number) => {
    manual.current.delete(i)
    saveWidth(widthKey, columns[i].name, null)
    resize(i, autoWidths(columns, rows)[i])
  }

  return (
    <div ref={box} className="min-h-0 flex-1 overflow-auto font-mono text-[12px]" onScroll={(e) => setScroll({ top: e.currentTarget.scrollTop, height: e.currentTarget.clientHeight })}>
      <div style={{ width: numW + total, minWidth: '100%' }}>
        <div className="sticky top-0 z-10 flex border-b border-line bg-panel" style={{ height: 34 }}>
          <div className="sticky left-0 z-20 flex shrink-0 items-center justify-end border-r border-line bg-panel px-2 font-sans text-[11px] text-faint" style={{ width: numW }}>
            #
          </div>
          {columns.map((c, i) => (
            <div key={i} className="relative flex shrink-0 flex-col justify-center border-r border-line px-2.5 font-sans leading-tight" style={{ width: colW[i] }} title={`${c.name} · ${c.type || c.kind}`}>
              <span className="flex items-center gap-1 truncate text-[11px] font-medium text-fg">
                {actions.keyColumns.has(i) && <KeyRound size={10} className="shrink-0 text-warn" />}
                <span className="truncate">{c.name}</span>
              </span>
              <span className="truncate text-[10px] text-faint lowercase">{c.type || c.kind}</span>
              <ColumnResizer width={colW[i] ?? 0} min={MIN_W} onChange={(w) => resize(i, w)} onCommit={(w) => commitWidth(i, w)} onReset={() => resetWidth(i)} />
            </div>
          ))}
        </div>
        <div className="relative" style={{ height: rows.length * ROW_H }}>
          {rows.slice(first, last).map((row, k) => {
            const i = first + k
            const isSel = selected.has(row.key)
            const isActive = active?.key === row.key
            return (
              <div
                key={row.key}
                className={cn(
                  'grid-row absolute inset-x-0 flex',
                  isSel ? 'bg-accent-soft' : row.inserted && 'bg-accent-soft/40',
                  row.deleted && 'text-danger line-through decoration-danger/60',
                )}
                style={{ top: i * ROW_H, height: ROW_H }}
                onClick={(e) => select(i, e)}
                onContextMenu={() => !isSel && onSelect(new Set([row.key]))}
              >
                <div
                  className={cn(
                    'grid-num sticky left-0 z-[5] flex shrink-0 items-center justify-end border-r border-b border-line/60 px-2 tabular-nums',
                    isSel ? 'bg-accent-soft' : 'bg-bg',
                    isActive ? 'font-medium text-accent' : 'text-faint',
                  )}
                  style={{ width: numW }}
                >
                  {row.inserted ? <span className="text-accent">+</span> : offset + row.index + 1}
                </div>
                {columns.map((c, col) => {
                  const v = row.values[col]
                  const isEditing = editing?.key === row.key && editing.col === col
                  const changed = row.changed?.has(col)
                  return (
                    <ContextMenu key={col} items={menu(row, col)} onOpenChange={(o) => setMenuAt(o ? { key: row.key, col } : null)}>
                      <div
                        className={cn(
                          'grid-cell flex shrink-0 items-center overflow-hidden border-r border-b border-line/60 px-2.5',
                          changed && 'bg-warn/15',
                          isCell(menuAt, row.key, col) ? 'cell-menu' : isCell(active, row.key, col) && 'cell-active',
                        )}
                        style={{ width: colW[col] }}
                        onClick={() => setActive({ key: row.key, col })}
                        onContextMenu={() => setActive({ key: row.key, col })}
                        onDoubleClick={() => actions.canEdit && actions.writable.has(col) && !row.deleted && setEditing({ key: row.key, col })}
                      >
                        {isEditing ? (
                          <CellEditor
                            value={v}
                            onDone={(nv) => {
                              setEditing(null)
                              if (nv !== undefined && nv !== v) actions.onEdit?.(row, col, nv)
                              box.current?.focus()
                            }}
                          />
                        ) : (
                          <CellValue v={v} kind={c.kind} />
                        )}
                      </div>
                    </ContextMenu>
                  )
                })}
              </div>
            )
          })}
        </div>
      </div>
    </div>
  )
}

export function CellValue({ v, kind, left }: { v: Cell; kind: string; left?: boolean }) {
  if (v === null) return <span className="text-faint italic">NULL</span>
  if (v === '') return <span className="text-faint/70 italic">empty</span>
  const text = v.length > 300 ? v.slice(0, 300) + '…' : v.replace(/\s+/g, ' ')
  return (
    <span
      className={cn(
        'selectable truncate',
        kind === 'number' && 'text-syn-number tabular-nums',
        kind === 'number' && !left && 'ml-auto',
        kind === 'bool' && 'text-syn-keyword',
        kind === 'datetime' && 'text-syn-type',
        kind === 'binary' && 'text-faint',
        kind === 'json' && 'text-syn-string',
      )}
      title={v.length > 40 ? v.slice(0, 2000) : undefined}
    >
      {text}
    </span>
  )
}

function CellEditor({ value, onDone }: { value: Cell; onDone: (v: Cell | undefined) => void }) {
  const [text, setText] = useState(value ?? '')
  const ref = useRef<HTMLInputElement>(null)
  useEffect(() => {
    ref.current?.focus()
    ref.current?.select()
  }, [])
  return (
    <input
      ref={ref}
      value={text}
      spellCheck={false}
      placeholder={value === null ? 'NULL' : ''}
      onChange={(e) => setText(e.target.value)}
      onClick={(e) => e.stopPropagation()}
      onBlur={() => onDone(text === '' && value === null ? null : text)}
      onKeyDown={(e) => {
        e.stopPropagation()
        if (e.key === 'Enter') onDone(text === '' && value === null ? null : text)
        else if (e.key === 'Escape') onDone(undefined)
      }}
      className="h-5 w-full min-w-0 rounded border border-accent bg-raised px-1 font-mono text-[12px] text-fg outline-none"
    />
  )
}
