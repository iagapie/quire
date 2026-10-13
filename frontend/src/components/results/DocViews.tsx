// Read-only views of JSON documents (Cosmos DB results): a tree, a table of the top-level
// fields, and the JSON text.
import { useEffect, useMemo, useRef, useState } from 'react'
import { ChevronRight, Copy } from 'lucide-react'
import { toast } from 'sonner'
import { type JVal, display, field, format, isContainer, typeName } from '../../lib/jsonval'
import { cn, copyText, mod } from '../../lib/utils'
import { ContextMenu, type MenuEntry } from '../ui'
import { Editor } from '../Editor'
import { quoted } from './DataGrid'
import { ColumnResizer } from './ColumnResizer'
import { saveViewWidth, saveWidth, savedWidths, viewWidth } from '../../lib/colwidths'

const MAX_COLUMNS = 60

export const valueClass = (v: JVal) =>
  cn(
    v.t === 'string' && 'text-syn-string',
    v.t === 'number' && 'text-syn-number tabular-nums',
    (v.t === 'bool' || v.t === 'null') && 'text-syn-keyword',
    isContainer(v) && 'text-faint',
  )

export function Value({ v, className }: { v: JVal; className?: string }) {
  const text = display(v)
  return (
    <span className={cn('selectable truncate', valueClass(v), className)} title={text.length > 60 ? text.slice(0, 2000) : undefined}>
      {v.t === 'string' && text === '' ? <span className="text-faint italic">empty</span> : text}
    </span>
  )
}

function joinPath(base: string, key: string, array: boolean) {
  if (array) return `${base}[${key}]`
  return base ? `${base}.${key}` : key
}

/** Text for copying documents: one as it is, several as a JSON array. */
export function docsText(docs: JVal[]): string {
  if (docs.length === 1) return format(docs[0])
  return '[\n' + docs.map((d) => '  ' + format(d, '  ', '  ')).join(',\n') + '\n]'
}

export async function copyDocs(docs: JVal[], indices: Iterable<number>) {
  const picked = [...indices].sort((a, b) => a - b).map((i) => docs[i]).filter(Boolean)
  if (picked.length === 0) return
  try {
    await copyText(docsText(picked))
    toast.success(picked.length === 1 ? 'Document copied' : `${picked.length} documents copied`)
  } catch (err) {
    toast.error('Could not copy', { description: err instanceof Error ? err.message : String(err) })
  }
}

/** The documents a context menu acts on: the whole selection when the row is part of it. */
function menuTargets(selected: Set<number>, row: number): number[] {
  return selected.has(row) && selected.size > 1 ? [...selected].sort((a, b) => a - b) : [row]
}

const copyDocsLabel = (sel: number[], offset: number) => (sel.length > 1 ? `Copy ${sel.length} documents` : `Copy document #${offset + sel[0] + 1}`)
const copyValue = (v: JVal) => copyText(isContainer(v) ? format(v) : display(v)).then(() => toast.success('Value copied'))

/** A width that stays put: table cells otherwise grow with their content. */
const fixed = (w: number | undefined) => (w ? { width: w, minWidth: w, maxWidth: w } : undefined)

/** Classes for the field last clicked and the one a context menu is open on. */
const cellState = (menu: boolean, active: boolean) => (menu ? 'cell-menu' : active && 'cell-active')

function useRangeSelect(selected: Set<number>, onSelect: (s: Set<number>) => void) {
  const anchor = useRef<number | null>(null)
  return (i: number, e: React.MouseEvent) => {
    if (e.shiftKey && anchor.current !== null) {
      const [a, b] = [Math.min(anchor.current, i), Math.max(anchor.current, i)]
      onSelect(new Set(Array.from({ length: b - a + 1 }, (_, k) => a + k)))
      return
    }
    if (e.metaKey || e.ctrlKey) {
      const next = new Set(selected)
      if (next.has(i)) next.delete(i)
      else next.add(i)
      onSelect(next)
    } else onSelect(new Set([i]))
    anchor.current = i
  }
}

interface FlatRow {
  id: string
  doc: number
  depth: number
  key: string
  path: string
  value: JVal
  top: boolean
}

export function DocTree({ docs, offset, resetKey, selected, onSelect }: { docs: JVal[]; offset: number; resetKey?: string; selected: Set<number>; onSelect: (s: Set<number>) => void }) {
  const [open, setOpen] = useState<Record<string, boolean>>({})
  const select = useRangeSelect(selected, onSelect)
  // Key and Type columns are resizable, the value takes the rest; unset is the default width.
  const [keyW, setKeyW] = useState(() => viewWidth('tree.key'))
  const [typeW, setTypeW] = useState(() => viewWidth('tree.type'))
  const resetView = (name: string, set: (w: undefined) => void) => {
    set(undefined)
    saveViewWidth(name, null)
  }
  const cols = `${keyW ? `${keyW}px` : 'minmax(220px,38%)'} minmax(0,1fr) ${typeW ?? 120}px`
  // The field last clicked, and the one a context menu is open on.
  const [active, setActive] = useState<string | null>(null)
  const [menuAt, setMenuAt] = useState<string | null>(null)
  useEffect(() => {
    setOpen({})
    setActive(null)
  }, [resetKey])

  const rows = useMemo(() => {
    const out: FlatRow[] = []
    const walk = (doc: number, v: JVal, depth: number, base: string) => {
      const items: [string, JVal][] = v.t === 'object' ? v.v : v.t === 'array' ? v.v.map((x, i) => [String(i), x]) : []
      for (const [k, x] of items) {
        const path = joinPath(base, k, v.t === 'array')
        const id = `${doc}/${path}`
        out.push({ id, doc, depth, key: v.t === 'array' ? `[${k}]` : k, path, value: x, top: false })
        if (isContainer(x) && open[id]) walk(doc, x, depth + 1, path)
      }
    }
    docs.forEach((d, i) => {
      const id = `${i}/`
      out.push({ id, doc: i, depth: 0, key: '', path: '', value: d, top: true })
      if (open[id]) walk(i, d, 1, '')
    })
    return out
  }, [docs, open])

  const toggle = (id: string) => setOpen((o) => ({ ...o, [id]: !o[id] }))

  const menuFor = (r: FlatRow): MenuEntry[] => {
    const sel = menuTargets(selected, r.doc)
    return [
      { label: `Copy value of ${quoted(r.key)}`, icon: <Copy size={14} />, hidden: r.top, onSelect: () => copyValue(r.value) },
      { label: 'Copy property path', icon: <Copy size={14} />, hidden: r.top, onSelect: () => copyText('c' + (r.path.startsWith('[') ? '' : '.') + r.path) },
      { label: copyDocsLabel(sel, offset), icon: <Copy size={14} />, shortcut: `${mod}C`, onSelect: () => copyDocs(docs, sel) },
    ]
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-hidden" role="tree">
      <div className="grid shrink-0 border-b border-line bg-panel text-[11px] font-medium tracking-wide text-faint uppercase" style={{ gridTemplateColumns: cols }}>
        <div className="relative px-3 py-1.5">
          Key
          <ColumnResizer width={keyW} min={120} onChange={setKeyW} onCommit={(w) => saveViewWidth('tree.key', w)} onReset={() => resetView('tree.key', setKeyW)} />
        </div>
        <div className="border-l border-line px-3 py-1.5">Value</div>
        <div className="relative border-l border-line px-3 py-1.5">
          Type
          <ColumnResizer
            side="left"
            width={typeW ?? 120}
            min={60}
            onChange={setTypeW}
            onCommit={(w) => saveViewWidth('tree.type', w)}
            onReset={() => resetView('tree.type', setTypeW)}
          />
        </div>
      </div>
      <div className="min-h-0 flex-1 overflow-auto font-mono text-[12px]">
        {rows.map((r) => {
          const container = isContainer(r.value)
          const id = r.top ? field(r.value, 'id') : undefined
          return (
            <ContextMenu key={r.id} items={menuFor(r)} onOpenChange={(o) => setMenuAt(o ? r.id : null)}>
              <div
                role="treeitem"
                aria-expanded={container ? !!open[r.id] : undefined}
                aria-selected={active === r.id}
                onClick={(e) => {
                  setActive(r.id)
                  select(r.doc, e)
                }}
                onContextMenu={() => {
                  setActive(r.id)
                  if (!selected.has(r.doc)) onSelect(new Set([r.doc]))
                }}
                onDoubleClick={() => container && toggle(r.id)}
                style={{ gridTemplateColumns: cols }}
                className={cn(
                  'grid-cell grid h-6.5 items-center',
                  selected.has(r.doc) && 'bg-accent-soft',
                  cellState(menuAt === r.id, active === r.id),
                  r.top && 'border-t border-line/60 first:border-t-0',
                )}
              >
                <div className="flex min-w-0 items-center gap-1 pr-2" style={{ paddingLeft: 6 + r.depth * 16 }}>
                  <button
                    tabIndex={-1}
                    className={cn('flex size-4 shrink-0 items-center justify-center text-faint', !container && 'invisible')}
                    onClick={(e) => {
                      e.stopPropagation()
                      toggle(r.id)
                    }}
                    aria-label="Toggle"
                  >
                    <ChevronRight size={12} className={cn('transition-transform', open[r.id] && 'rotate-90')} />
                  </button>
                  {r.top ? (
                    <span className="truncate">
                      <span className={active === r.id ? 'font-medium text-accent' : 'text-faint'}>({offset + r.doc + 1})</span>{' '}
                      {id ? (
                        <>
                          <span className="text-syn-key">id</span>
                          <span className="text-faint">: </span>
                          <Value v={id} />
                        </>
                      ) : (
                        <span className="text-faint">{isContainer(r.value) ? 'document' : 'value'}</span>
                      )}
                    </span>
                  ) : (
                    <span className="truncate text-syn-key">{r.key}</span>
                  )}
                </div>
                <div className="flex min-w-0 items-center border-l border-line/50 px-3">
                  <Value v={r.value} />
                </div>
                <div className="truncate border-l border-line/50 px-3 font-sans text-[12px] text-muted">{r.top && r.value.t === 'object' ? 'Document' : typeName(r.value)}</div>
              </div>
            </ContextMenu>
          )
        })}
      </div>
    </div>
  )
}

export function DocTable({
  docs,
  offset,
  selected,
  onSelect,
  widthKey = '',
}: {
  docs: JVal[]
  offset: number
  selected: Set<number>
  onSelect: (s: Set<number>) => void
  /** Where resized column widths are saved, as in DataGrid. */
  widthKey?: string
}) {
  const select = useRangeSelect(selected, onSelect)
  const [colW, setColW] = useState<Record<string, number>>(() => savedWidths(widthKey))
  useEffect(() => setColW(savedWidths(widthKey)), [widthKey])
  const setWidth = (c: string, w: number | null) =>
    setColW((cur) => {
      const next = { ...cur }
      if (w === null) delete next[c]
      else next[c] = w
      return next
    })
  const [active, setActive] = useState<{ row: number; col: string } | null>(null)
  const [menuAt, setMenuAt] = useState<{ row: number; col: string } | null>(null)
  const isAt = (at: { row: number; col: string } | null, row: number, col: string) => at?.row === row && at.col === col
  useEffect(() => setActive(null), [docs])
  const columns = useMemo(() => {
    const cols: string[] = []
    const seen = new Set<string>()
    for (const d of docs) {
      if (d.t !== 'object') continue
      for (const [k] of d.v) {
        if (!seen.has(k)) {
          seen.add(k)
          cols.push(k)
        }
      }
      if (cols.length >= MAX_COLUMNS) break
    }
    // id first and Cosmos DB's system properties (_rid, _ts…) last.
    const rank = (c: string) => (c === 'id' ? 0 : c.startsWith('_') ? 2 : 1)
    cols.sort((a, b) => rank(a) - rank(b))
    if (docs.some((d) => d.t !== 'object')) cols.unshift('')
    return cols.slice(0, MAX_COLUMNS)
  }, [docs])

  const menu = (i: number, col: string, cell?: JVal): MenuEntry[] => {
    const sel = menuTargets(selected, i)
    return [
      { label: `Copy value of ${quoted(col || 'value')}`, icon: <Copy size={14} />, hidden: !cell, onSelect: () => cell && copyValue(cell) },
      { label: 'Copy field name', icon: <Copy size={14} />, hidden: !col, onSelect: () => copyText(col).then(() => toast.success('Field name copied')) },
      { separator: true },
      { label: copyDocsLabel(sel, offset), icon: <Copy size={14} />, shortcut: `${mod}C`, onSelect: () => copyDocs(docs, sel) },
    ]
  }

  return (
    <div className="min-h-0 flex-1 overflow-auto">
      <table className="w-max min-w-full border-separate border-spacing-0 font-mono text-[12px]">
        <thead className="sticky top-0 z-10 bg-panel">
          <tr>
            <th className="sticky left-0 z-20 w-12 border-r border-b border-line bg-panel px-2 py-1.5 text-right font-sans text-[11px] font-medium text-faint">#</th>
            {columns.map((c) => (
              <th
                key={c}
                className={cn('relative truncate border-r border-b border-line px-3 py-1.5 text-left font-sans text-[11px] font-medium text-muted', !colW[c] && 'max-w-80')}
                style={fixed(colW[c])}
              >
                {c || 'value'}
                <ColumnResizer
                  width={colW[c]}
                  onChange={(w) => setWidth(c, w)}
                  onCommit={(w) => saveWidth(widthKey, c, w)}
                  onReset={() => {
                    setWidth(c, null)
                    saveWidth(widthKey, c, null)
                  }}
                />
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {docs.map((d, i) => (
            <tr key={i} onClick={(e) => select(i, e)} onContextMenu={() => !selected.has(i) && onSelect(new Set([i]))} className={cn('grid-row', selected.has(i) && 'bg-accent-soft')}>
              <td
                className={cn(
                  'grid-num sticky left-0 border-r border-b border-line/60 px-2 py-1 text-right tabular-nums',
                  selected.has(i) ? 'bg-accent-soft' : 'bg-bg',
                  active?.row === i ? 'font-medium text-accent' : 'text-faint',
                )}
              >
                {offset + i + 1}
              </td>
              {columns.map((c) => {
                const v = c === '' ? (d.t === 'object' ? undefined : d) : field(d, c)
                return (
                  <ContextMenu key={c} items={menu(i, c, v)} onOpenChange={(o) => setMenuAt(o ? { row: i, col: c } : null)}>
                    <td
                      className={cn('grid-cell border-r border-b border-line/60 px-3 py-1', !colW[c] && 'max-w-80', cellState(isAt(menuAt, i, c), isAt(active, i, c)))}
                      style={fixed(colW[c])}
                      onClick={() => setActive({ row: i, col: c })}
                      onContextMenu={() => setActive({ row: i, col: c })}
                    >
                      <div className="flex min-w-0">{v ? <Value v={v} /> : <span className="text-faint/50">—</span>}</div>
                    </td>
                  </ContextMenu>
                )
              })}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

export function DocJson({ docs }: { docs: JVal[] }) {
  const text = useMemo(() => (docs.length === 0 ? '[]' : '[\n' + docs.map((d) => '  ' + format(d, '  ', '  ')).join(',\n') + '\n]'), [docs])
  return <Editor value={text} readOnly json className="min-h-0 flex-1 overflow-hidden" />
}
