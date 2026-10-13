import { useMemo, useRef, useState } from 'react'
import { Copy, CopyPlus, Pencil, Trash2 } from 'lucide-react'
import { type BVal, display, format } from '../../../lib/shell/ejson'
import { cn, copyText, mod } from '../../../lib/utils'
import { ContextMenu, type MenuEntry } from '../../ui'
import { type DocActions, Value, copyDocs, fieldLabel, menuTargets, targetClass } from './common'
import { ResizeHandle, useColumnWidths } from './columnWidths'

const MAX_COLUMNS = 60

export function DocTable({
  docs,
  offset,
  selected,
  onSelect,
  actions,
  widthsKey,
}: {
  docs: BVal[]
  offset: number
  selected: Set<number>
  onSelect: (s: Set<number>) => void
  actions: DocActions
  /** Identifies the collection whose column widths are saved; unsaved when absent. */
  widthsKey?: string
}) {
  const anchor = useRef<number | null>(null)
  // Unset columns size to their content.
  const { widths, startResize, resetWidth } = useColumnWidths('quire.doc.table.columnWidths', widthsKey)
  // The cell last clicked, outlined so it is clear what was picked.
  const [active, setActive] = useState<{ row: number; col: string } | null>(null)
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
    if (docs.some((d) => d.t !== 'object')) cols.unshift('')
    return cols.slice(0, MAX_COLUMNS)
  }, [docs])

  const colStyle = (c: string): React.CSSProperties | undefined => {
    const w = widths[c]
    return w === undefined ? undefined : { width: w, minWidth: w, maxWidth: w }
  }

  const select = (i: number, e: React.MouseEvent) => {
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

  const menu = (i: number, col: string, cell?: BVal): MenuEntry[] => {
    const sel = menuTargets(selected, i)
    const many = sel.length > 1
    return [
      { label: 'Edit document…', icon: <Pencil size={14} />, hidden: !actions.editable, onSelect: () => actions.onEdit?.(i) },
      { separator: true, hidden: !actions.editable },
      { label: col ? `Copy value of ${fieldLabel(col)}` : 'Copy value', icon: <Copy size={14} />, hidden: !cell, onSelect: () => cell && copyText(cell.t === 'object' || cell.t === 'array' ? format(cell) : display(cell)) },
      { label: 'Copy field name', icon: <Copy size={14} />, hidden: !col, onSelect: () => copyText(col) },
      { separator: true, hidden: !cell && !col },
      { label: many ? `Copy ${sel.length} documents` : `Copy document #${offset + i + 1}`, icon: <Copy size={14} />, shortcut: `${mod}C`, onSelect: () => copyDocs(docs, sel) },
      { label: many ? `Copy ${sel.length} as JSON` : 'Copy as JSON', icon: <Copy size={14} />, onSelect: () => copyDocs(docs, sel, true) },
      { separator: true, hidden: !actions.editable },
      { label: 'Duplicate document…', icon: <CopyPlus size={14} />, hidden: !actions.editable, onSelect: () => actions.onDuplicate?.(i) },
      {
        label: sel.length > 1 ? `Delete ${sel.length} documents…` : 'Delete document…',
        icon: <Trash2 size={14} />,
        danger: true,
        hidden: !actions.editable,
        onSelect: () => actions.onDelete?.(sel),
      },
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
                style={colStyle(c)}
                className="relative max-w-80 border-r border-b border-line px-3 py-1.5 text-left font-sans text-[11px] font-medium text-muted"
              >
                <div className="truncate">{c || 'value'}</div>
                <ResizeHandle onPointerDown={(e) => startResize(c, e)} onReset={() => resetWidth(c)} />
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {docs.map((d, i) => (
            <tr
              key={i}
              onClick={(e) => select(i, e)}
              onContextMenu={() => !selected.has(i) && onSelect(new Set([i]))}
              onDoubleClick={() => actions.editable && actions.onEdit?.(i)}
              className={cn('group', selected.has(i) ? 'bg-accent-soft' : 'hover:bg-hover')}
            >
              <td
                className={cn(
                  // Opaque so scrolled cells pass beneath it; the inset shadow tints it like the row.
                  'sticky left-0 border-r border-b border-line/60 bg-bg px-2 py-1 text-right tabular-nums',
                  selected.has(i)
                    ? 'text-accent shadow-[inset_0_0_0_999px_var(--color-accent-soft)]'
                    : 'text-faint group-hover:text-muted group-hover:shadow-[inset_0_0_0_999px_var(--color-hover)]',
                )}
              >
                {offset + i + 1}
              </td>
              {columns.map((c) => {
                const v = c === '' ? (d.t === 'object' ? undefined : d) : d.t === 'object' ? d.v.find((e) => e[0] === c)?.[1] : undefined
                return (
                  <ContextMenu key={c} items={menu(i, c, v)}>
                    <td
                      style={colStyle(c)}
                      data-active={active?.row === i && active.col === c && selected.has(i)}
                      onClick={() => setActive({ row: i, col: c })}
                      onContextMenu={() => setActive({ row: i, col: c })}
                      className={cn('max-w-80 border-r border-b border-line/60 px-3 py-1 hover:bg-hover', targetClass)}
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
