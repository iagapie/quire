import { useEffect, useMemo, useRef, useState } from 'react'
import { ChevronRight, Copy, CopyPlus, Pencil, Trash2, Eraser } from 'lucide-react'
import { type BVal, field, format, isContainer, typeName, display } from '../../../lib/shell/ejson'
import { cn, copyText, mod } from '../../../lib/utils'
import { ContextMenu, type MenuEntry } from '../../ui'
import { type DocActions, Value, copyDocs, fieldLabel, joinPath, menuTargets, targetClass } from './common'
import { ResizeHandle, useColumnWidths } from './columnWidths'

interface FlatRow {
  id: string
  doc: number
  depth: number
  key: string
  path: string
  value: BVal
  top: boolean
}

export function DocTree({
  docs,
  offset,
  resetKey,
  widthsKey,
  selected,
  onSelect,
  actions,
}: {
  docs: BVal[]
  offset: number
  /** Expanded nodes are kept across refreshes and reset when this changes. */
  resetKey?: string
  /** Identifies the collection whose column widths are saved; unsaved when absent. */
  widthsKey?: string
  selected: Set<number>
  onSelect: (s: Set<number>) => void
  actions: DocActions
}) {
  const [open, setOpen] = useState<Record<string, boolean>>({})
  const [editing, setEditing] = useState<string | null>(null)
  // The row last clicked, outlined so it is clear which field was picked.
  const [active, setActive] = useState<string | null>(null)
  const anchor = useRef<number | null>(null)
  const { widths, startResize, resetWidth } = useColumnWidths('quire.doc.tree.columnWidths', widthsKey)
  // Header and rows share the template; the value column takes what is left.
  const gridStyle = {
    '--tree-cols': `${widths.key ? `${widths.key}px` : 'minmax(220px,38%)'} minmax(80px,1fr) ${widths.type ?? 120}px`,
  } as React.CSSProperties

  useEffect(() => {
    setOpen({})
  }, [resetKey])
  useEffect(() => setEditing(null), [docs])

  const rows = useMemo(() => {
    const out: FlatRow[] = []
    const walk = (doc: number, v: BVal, depth: number, base: string) => {
      const items: [string, BVal][] = v.t === 'object' ? v.v : v.t === 'array' ? v.v.map((x, i) => [String(i), x]) : []
      for (const [k, x] of items) {
        const path = joinPath(base, k)
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

  const select = (doc: number, e: React.MouseEvent) => {
    if (e.shiftKey && anchor.current !== null) {
      const [a, b] = [Math.min(anchor.current, doc), Math.max(anchor.current, doc)]
      onSelect(new Set(Array.from({ length: b - a + 1 }, (_, i) => a + i)))
      return
    }
    if (e.metaKey || e.ctrlKey) {
      const next = new Set(selected)
      if (next.has(doc)) next.delete(doc)
      else next.add(doc)
      onSelect(next)
    } else {
      onSelect(new Set([doc]))
    }
    anchor.current = doc
  }

  const menuFor = (r: FlatRow): MenuEntry[] => {
    const docIdx = r.doc
    const sel = menuTargets(selected, docIdx)
    const many = sel.length > 1
    const canEditField = actions.editable && !r.top && r.path !== '_id' && !r.path.startsWith('_id.')
    return [
      { label: 'Edit document…', icon: <Pencil size={14} />, shortcut: '↵', hidden: !actions.editable, onSelect: () => actions.onEdit?.(docIdx) },
      {
        label: 'Edit value',
        icon: <Pencil size={14} />,
        hidden: !canEditField || isContainer(r.value),
        onSelect: () => setEditing(r.id),
      },
      { label: 'Remove field', icon: <Eraser size={14} />, hidden: !canEditField, onSelect: () => actions.onUnsetField?.(docIdx, r.path) },
      { separator: true, hidden: !actions.editable },
      { label: `Copy value of ${fieldLabel(r.path)}`, icon: <Copy size={14} />, hidden: r.top, onSelect: () => copyText(isContainer(r.value) ? format(r.value) : display(r.value)) },
      { label: 'Copy field path', icon: <Copy size={14} />, hidden: r.top, onSelect: () => copyText(r.path) },
      { separator: true, hidden: r.top },
      { label: many ? `Copy ${sel.length} documents` : `Copy document #${offset + docIdx + 1}`, icon: <Copy size={14} />, shortcut: `${mod}C`, onSelect: () => copyDocs(docs, sel) },
      { label: many ? `Copy ${sel.length} as JSON` : 'Copy as JSON', icon: <Copy size={14} />, onSelect: () => copyDocs(docs, sel, true) },
      { separator: true, hidden: !actions.editable },
      { label: 'Duplicate document…', icon: <CopyPlus size={14} />, hidden: !actions.editable, onSelect: () => actions.onDuplicate?.(docIdx) },
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
    <div className="flex min-h-0 flex-1 flex-col" role="tree" style={gridStyle}>
      <div className="grid shrink-0 grid-cols-(--tree-cols) border-b border-line bg-panel text-[11px] font-medium tracking-wide text-faint uppercase">
        <div className="relative px-3 py-1.5">
          Key
          <ResizeHandle onPointerDown={(e) => startResize('key', e)} onReset={() => resetWidth('key')} />
        </div>
        <div className="border-l border-line px-3 py-1.5">Value</div>
        <div className="relative border-l border-line px-3 py-1.5">
          Type
          <ResizeHandle side="left" onPointerDown={(e) => startResize('type', e, -1)} onReset={() => resetWidth('type')} />
        </div>
      </div>
      <div className="min-h-0 flex-1 overflow-auto font-mono text-[12px]">
        {rows.map((r) => {
          const container = isContainer(r.value)
          const isSel = selected.has(r.doc)
          const id = r.top ? field(r.value, '_id') : undefined
          const row = (
            <div
              role="treeitem"
              aria-expanded={container ? !!open[r.id] : undefined}
              data-active={active === r.id && isSel}
              onClick={(e) => {
                select(r.doc, e)
                setActive(r.id)
              }}
              onContextMenu={() => {
                if (!selected.has(r.doc)) onSelect(new Set([r.doc]))
                setActive(r.id)
              }}
              onDoubleClick={() => {
                if (r.top) {
                  if (actions.editable) actions.onEdit?.(r.doc)
                  else toggle(r.id)
                } else if (container) toggle(r.id)
                else if (actions.editable && r.path !== '_id') setEditing(r.id)
              }}
              className={cn(
                'grid h-6.5 grid-cols-(--tree-cols) items-center',
                isSel ? 'bg-accent-soft' : 'hover:bg-hover',
                targetClass,
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
                    <span className="text-faint">({offset + r.doc + 1})</span>{' '}
                    {id ? (
                      <>
                        <span className="text-syn-key">_id</span>
                        <span className="text-faint">: </span>
                        <Value v={id} />
                      </>
                    ) : (
                      <span className="text-faint">document</span>
                    )}
                  </span>
                ) : (
                  <span className="truncate text-syn-key">{r.key}</span>
                )}
              </div>
              <div className="flex min-w-0 items-center border-l border-line/50 px-3">
                {editing === r.id ? (
                  <InlineEditor
                    initial={format(r.value, '')}
                    onCancel={() => setEditing(null)}
                    onSave={async (text) => {
                      const ok = (await actions.onSetField?.(r.doc, r.path, text)) ?? false
                      if (ok) setEditing(null)
                    }}
                  />
                ) : (
                  <Value v={r.value} />
                )}
              </div>
              <div className="truncate border-l border-line/50 px-3 font-sans text-[12px] text-muted">{r.top ? 'Document' : typeName(r.value)}</div>
            </div>
          )
          return (
            <ContextMenu key={r.id} items={menuFor(r)}>
              {row}
            </ContextMenu>
          )
        })}
      </div>
    </div>
  )
}

function InlineEditor({ initial, onSave, onCancel }: { initial: string; onSave: (text: string) => Promise<void>; onCancel: () => void }) {
  const [text, setText] = useState(initial)
  const [busy, setBusy] = useState(false)
  const ref = useRef<HTMLInputElement>(null)
  useEffect(() => {
    ref.current?.focus()
    ref.current?.select()
  }, [])
  return (
    <input
      ref={ref}
      value={text}
      disabled={busy}
      spellCheck={false}
      onChange={(e) => setText(e.target.value)}
      onClick={(e) => e.stopPropagation()}
      onDoubleClick={(e) => e.stopPropagation()}
      onKeyDown={async (e) => {
        e.stopPropagation()
        if (e.key === 'Escape') onCancel()
        if (e.key === 'Enter') {
          setBusy(true)
          await onSave(text)
          setBusy(false)
        }
      }}
      onBlur={() => !busy && onCancel()}
      title={'Enter to save, Esc to cancel. Shell syntax: "text", 42, Double(1), ISODate("…"), ObjectId("…")'}
      className="h-5.5 w-full rounded border border-accent bg-sunken px-1.5 font-mono text-[12px] text-fg outline-none"
    />
  )
}
