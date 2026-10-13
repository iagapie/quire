import { useEffect, useMemo, useRef, useState } from 'react'
import {
  AlertTriangle,
  Braces,
  CheckCircle2,
  ChevronLeft,
  ChevronRight,
  ChevronsLeft,
  CopyPlus,
  Hash,
  Plus,
  RefreshCw,
  RotateCcw,
  ListTree,
  Rows3,
  Sheet,
  Square,
  Trash2,
  WifiOff,
} from 'lucide-react'
import { toast } from 'sonner'
import { Queries, errorMessage, type Column, type EditStatement, type Edit } from '../../lib/api'
import { useStore, type ResultState, type Tab, type ViewMode } from '../../lib/store'
import { qualifiedName, rowObject, toCSV, toInsert, toJSON, toMarkdown, toTSV } from '../../lib/sql'
import { cn, copyText, formatDuration, formatNumber, mod } from '../../lib/utils'
import { confirmAction } from '../Confirm'
import { Button, IconButton, Input, Modal, Spinner } from '../ui'
import { Editor } from '../Editor'
import { toJVal } from '../../lib/jsonval'
import { saveViewWidth, viewWidth, widthKey } from '../../lib/colwidths'
import { ColumnResizer } from './ColumnResizer'
import { DocJson, DocTable, DocTree, copyDocs } from './DocViews'
import { CellValue, DataGrid, type Cell, type CopyFormat, type GridActions, type GridRow } from './DataGrid'

const PAGE_SIZES = [50, 100, 200, 500, 1000]

function ViewSwitch({ view, onChange, documents }: { view: ViewMode; onChange: (v: ViewMode) => void; documents?: boolean }) {
  const opts: { v: ViewMode; label: string; icon: React.ReactNode }[] = [
    { v: 'table', label: 'Table', icon: <Sheet size={13} /> },
    documents ? { v: 'record', label: 'Tree', icon: <ListTree size={13} /> } : { v: 'record', label: 'Record', icon: <Rows3 size={13} /> },
    { v: 'json', label: 'JSON', icon: <Braces size={13} /> },
  ]
  return (
    <div className="flex h-7 shrink-0 items-center rounded-md border border-line bg-sunken p-0.5" role="radiogroup" aria-label="View">
      {opts.map((o) => (
        <button
          key={o.v}
          role="radio"
          aria-checked={view === o.v}
          aria-label={o.label}
          title={o.label}
          onClick={() => onChange(o.v)}
          className={cn('flex h-full items-center gap-1 rounded px-2 text-xs', view === o.v ? 'bg-raised font-medium text-fg shadow-xs' : 'text-muted hover:text-fg')}
        >
          {o.icon}
          <span className="hidden @3xl:inline">{o.label}</span>
        </button>
      ))}
    </div>
  )
}

function QueryBar({ result, onApply, disabled }: { result: ResultState; onApply: (where: string, orderBy: string, limit: number) => void; disabled?: boolean }) {
  const t = result.table!
  const applied = { where: t.where, orderBy: t.orderBy, limit: t.limit ? String(t.limit) : '' }
  const [draft, setDraft] = useState(applied)
  useEffect(() => setDraft({ where: t.where, orderBy: t.orderBy, limit: t.limit ? String(t.limit) : '' }), [t.where, t.orderBy, t.limit])
  const changed = draft.where !== applied.where || draft.orderBy !== applied.orderBy || draft.limit !== applied.limit
  const label = 'shrink-0 text-[11px] font-medium text-faint uppercase tracking-wide'
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault()
        onApply(draft.where, draft.orderBy, Math.max(0, Number(draft.limit) || 0))
      }}
      className="flex flex-wrap items-center gap-x-3 gap-y-1.5 border-b border-line bg-panel px-3 py-2"
    >
      <label className="flex min-w-60 flex-[3] items-center gap-2">
        <span className={label}>Where</span>
        <Input mono value={draft.where} onChange={(e) => setDraft({ ...draft, where: e.target.value })} placeholder="id > 100 AND status = 'open'" className="h-7" />
      </label>
      <label className="flex min-w-40 flex-[1.4] items-center gap-2">
        <span className={label}>Order by</span>
        <Input mono value={draft.orderBy} onChange={(e) => setDraft({ ...draft, orderBy: e.target.value })} placeholder="created_at DESC" className="h-7" />
      </label>
      <label className="flex items-center gap-2">
        <span className={label}>Limit</span>
        <Input mono type="number" min={0} value={draft.limit} placeholder="∞" onChange={(e) => setDraft({ ...draft, limit: e.target.value })} className="h-7 w-24" />
      </label>
      <div className="flex items-center gap-1">
        {changed && (
          <IconButton type="button" label="Discard changes" onClick={() => setDraft(applied)}>
            <RotateCcw size={14} />
          </IconButton>
        )}
        <Button type="submit" size="sm" variant={changed ? 'primary' : 'secondary'} disabled={disabled}>
          Apply
        </Button>
      </div>
    </form>
  )
}

/** Pending changes to a single-table result. */
interface Pending {
  updates: Map<number, Map<number, Cell>>
  inserts: Cell[][]
  deletes: Set<number>
}

const emptyPending = (): Pending => ({ updates: new Map(), inserts: [], deletes: new Set() })
const pendingCount = (p: Pending) => [...p.updates.values()].filter((m) => m.size > 0).length + p.inserts.length + p.deletes.size

export function ResultView({ tab, result }: { tab: Tab; result: ResultState }) {
  const { fetchPage, count, cancelResult, updateTab, requery } = useStore.getState()
  const pageSize = useStore((s) => s.pageSize)
  const readOnly = useStore((s) => s.sessions[tab.sessionId]?.readOnly ?? false)
  const editor = useStore((s) => s.drivers[s.profiles.find((p) => p.id === tab.sessionId)?.driver ?? '']?.editor ?? 'StandardSQL')
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [pending, setPending] = useState<Pending>(emptyPending)
  const [preview, setPreview] = useState<EditStatement[] | null>(null)
  const [applying, setApplying] = useState(false)
  const box = useRef<HTMLDivElement>(null)

  const cols = result.columns
  const pageRows = useMemo<Cell[][]>(() => (result.page?.rows ?? []).map((r) => r ?? []), [result.page])
  // Document databases return one JSON document per row.
  const documentDriver = useStore((s) => !!s.drivers[s.profiles.find((p) => p.id === tab.sessionId)?.driver ?? '']?.document)
  const isDoc = documentDriver && cols.length === 1 && cols[0].kind === 'json'
  const docs = useMemo(() => (isDoc ? pageRows.map((r) => toJVal(r[0] ?? null)) : []), [isDoc, pageRows])
  const [docSel, setDocSel] = useState<Set<number>>(new Set())
  useEffect(() => {
    setSelected(new Set())
    setDocSel(new Set())
    setPending(emptyPending())
  }, [result.page])

  const ti = result.table
  const canEdit = !!ti && (ti.key?.length ?? 0) > 0 && !readOnly && result.kind === 'rows'
  const colIndex = useMemo(() => new Map(cols.map((c, i) => [c.name, i])), [cols])
  const writable = useMemo(() => new Set((ti?.writable ?? []).map((c) => colIndex.get(c)).filter((i): i is number => i !== undefined)), [ti, colIndex])
  const keyColumns = useMemo(() => new Set((ti?.key ?? []).map((c) => colIndex.get(c)).filter((i): i is number => i !== undefined)), [ti, colIndex])

  const rows = useMemo<GridRow[]>(() => {
    const out: GridRow[] = pageRows.map((values, index) => {
      const upd = pending.updates.get(index)
      return {
        key: `r${index}`,
        index,
        values: upd ? values.map((v, c) => (upd.has(c) ? upd.get(c)! : v)) : values,
        changed: upd ? new Set(upd.keys()) : undefined,
        deleted: pending.deletes.has(index),
      }
    })
    pending.inserts.forEach((values, k) => out.push({ key: `n${k}`, index: -1, values, inserted: true, changed: new Set(values.map((_, i) => i)) }))
    return out
  }, [pageRows, pending])

  // Resized columns are remembered per table, not per query.
  const widthsAt = ti ? widthKey(tab.sessionId, ti.database || tab.database, ti.schema, ti.table) : ''
  const tableName = ti ? qualifiedName(editor, ti.schema ? '' : ti.database !== tab.database ? ti.database : '', ti.schema, ti.table) : 'my_table'
  const reload = () => (ti ? requery(tab.id, result.id, ti.where, ti.orderBy, ti.limit) : fetchPage(tab.id, result.id, result.offset))

  const copy = async (sel: GridRow[], fmt: CopyFormat) => {
    const vals = sel.map((r) => r.values)
    const text = fmt === 'tsv' ? toTSV(cols, vals) : fmt === 'csv' ? toCSV(cols, vals) : fmt === 'json' ? toJSON(cols, vals) : fmt === 'insert' ? toInsert(editor, tableName, cols, vals) : toMarkdown(cols, vals)
    try {
      await copyText(text)
      toast.success(sel.length === 1 ? 'Row copied' : `${sel.length} rows copied`, { description: fmt.toUpperCase() })
    } catch (err) {
      toast.error('Could not copy', { description: errorMessage(err) })
    }
  }

  const guardPending = async () => {
    if (pendingCount(pending) === 0) return true
    return confirmAction({ title: 'Discard changes?', message: `${pendingCount(pending)} changes have not been applied.`, confirmLabel: 'Discard', danger: true })
  }
  const goPage = async (offset: number, size?: number) => {
    if (await guardPending()) void fetchPage(tab.id, result.id, offset, size)
  }

  const actions: GridActions = {
    writable,
    keyColumns,
    canEdit,
    onCopy: copy,
    onCopyValue: (v) => void copyText(v ?? 'NULL').then(() => toast.success('Value copied')),
    onEdit: (row, col, value) =>
      setPending((p) => {
        const next = { ...p, updates: new Map(p.updates), inserts: [...p.inserts] }
        if (row.inserted) {
          const k = Number(row.key.slice(1))
          next.inserts[k] = next.inserts[k].map((v, i) => (i === col ? value : v))
        } else {
          const m = new Map(next.updates.get(row.index) ?? [])
          if (pageRows[row.index][col] === value) m.delete(col)
          else m.set(col, value)
          next.updates.set(row.index, m)
        }
        return next
      }),
    onDuplicate: (sel) =>
      setPending((p) => ({
        ...p,
        inserts: [
          ...p.inserts,
          // Keys with a default (serial, identity) are left out so the database fills them.
          ...sel.map((r) => r.values.map((v, i) => (keyColumns.has(i) && ti?.defaults?.includes(cols[i].name) ? null : v))),
        ],
      })),
    onDelete: (sel) =>
      setPending((p) => {
        const next = { ...p, deletes: new Set(p.deletes), inserts: [...p.inserts] }
        const dropInserted = new Set(sel.filter((r) => r.inserted).map((r) => Number(r.key.slice(1))))
        next.inserts = next.inserts.filter((_, k) => !dropInserted.has(k))
        for (const r of sel) if (!r.inserted) next.deletes.add(r.index)
        return next
      }),
  }

  const edits = (): Edit[] => {
    if (!ti) return []
    const keyOf = (index: number) => Object.fromEntries((ti.key ?? []).map((k) => [k, pageRows[index][colIndex.get(k)!]]))
    const out: Edit[] = []
    for (const [index, m] of pending.updates) {
      if (m.size === 0 || pending.deletes.has(index)) continue
      out.push({ op: 'update', key: keyOf(index), values: Object.fromEntries([...m].map(([c, v]) => [cols[c].name, v])) })
    }
    for (const values of pending.inserts) {
      // Unset columns with defaults are left to the database.
      const v: Record<string, Cell> = {}
      values.forEach((x, i) => {
        if (writable.has(i) && !(x === null && ti.defaults?.includes(cols[i].name))) v[cols[i].name] = x
      })
      out.push({ op: 'insert', key: {}, values: v })
    }
    for (const index of pending.deletes) out.push({ op: 'delete', key: keyOf(index), values: {} })
    return out
  }
  const request = () => ({ table: ti!, edits: edits(), kinds: Object.fromEntries(cols.map((c) => [c.name, c.kind])) })

  const showPreview = async () => {
    try {
      setPreview((await Queries.PlanEdits(tab.sessionId, request())) ?? [])
    } catch (err) {
      toast.error('Cannot apply these changes', { description: errorMessage(err) })
    }
  }
  const apply = async () => {
    setApplying(true)
    try {
      const n = await Queries.ApplyEdits(tab.sessionId, tab.id, request())
      setPreview(null)
      setPending(emptyPending())
      toast.success(`${n} ${n === 1 ? 'change' : 'changes'} applied`, { description: tab.tx?.manual ? 'Commit the transaction to keep them.' : undefined })
      void reload()
      const s = useStore.getState()
      if (tab.tx?.manual) s.updateTab(tab.id, { tx: await Queries.TransactionState(tab.id) })
    } catch (err) {
      toast.error('Changes not applied', { description: errorMessage(err) })
    } finally {
      setApplying(false)
    }
  }

  if (result.kind === 'error') {
    return (
      <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-auto p-4">
        <div className="flex items-start gap-2.5 rounded-lg border border-danger/30 bg-danger-soft p-3 text-danger">
          <AlertTriangle size={16} className="mt-0.5 shrink-0" />
          <div className="selectable min-w-0 font-mono text-[12px] break-words whitespace-pre-wrap">{result.error}</div>
        </div>
        {result.statement && <pre className="selectable rounded-lg bg-sunken p-3 font-mono text-[12px] whitespace-pre-wrap text-muted">{result.statement}</pre>}
      </div>
    )
  }

  if (result.kind === 'message') {
    return (
      <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-auto p-4">
        <div className="flex items-start gap-2.5 rounded-lg border border-line bg-panel p-3">
          <CheckCircle2 size={16} className="mt-0.5 shrink-0 text-ok" />
          <div className="min-w-0">
            <div className="text-[13px] font-medium">{result.message}</div>
            <div className="mt-0.5 text-xs text-faint">Ran in {formatDuration(result.durationMs)}</div>
          </div>
        </div>
        {result.statement && <pre className="selectable rounded-lg bg-sunken p-3 font-mono text-[12px] whitespace-pre-wrap text-muted">{result.statement}</pre>}
      </div>
    )
  }

  const page = result.page
  const firstRow = result.offset + 1
  const lastRow = result.offset + pageRows.length
  const total = result.count
  const changes = pendingCount(pending)

  return (
    <div className="@container flex min-h-0 flex-1 flex-col">
      {ti && <QueryBar result={result} disabled={result.loading} onApply={async (w, o, l) => (await guardPending()) && requery(tab.id, result.id, w, o, l)} />}
      <div className="scroll-x flex h-10 shrink-0 items-center gap-1 border-b border-line px-2">
        {result.loading ? (
          <IconButton label="Stop" tone="danger" onClick={() => cancelResult(tab.id, result.id)}>
            <Square size={13} fill="currentColor" />
          </IconButton>
        ) : (
          <IconButton label="Refresh" onClick={async () => (await guardPending()) && reload()}>
            <RefreshCw size={14} />
          </IconButton>
        )}
        <div className="mx-1 h-4 w-px bg-line" />
        <IconButton label="First page" disabled={result.offset === 0 || result.loading} onClick={() => goPage(0)}>
          <ChevronsLeft size={15} />
        </IconButton>
        <IconButton label="Previous page" disabled={result.offset === 0 || result.loading} onClick={() => goPage(Math.max(0, result.offset - result.pageSize))}>
          <ChevronLeft size={15} />
        </IconButton>
        <IconButton label="Next page" disabled={!page?.hasMore || result.loading} onClick={() => goPage(result.offset + result.pageSize)}>
          <ChevronRight size={15} />
        </IconButton>
        <select
          aria-label="Page size"
          value={result.pageSize}
          onChange={(e) => {
            const size = Number(e.target.value)
            useStore.setState({ pageSize: size })
            void goPage(0, size)
          }}
          className="mx-1 h-7 rounded-md border border-line bg-sunken px-1.5 text-xs text-fg focus:outline-none"
        >
          {[...new Set([...PAGE_SIZES, pageSize, result.pageSize])]
            .sort((a, b) => a - b)
            .map((n) => (
              <option key={n} value={n}>
                {n} / page
              </option>
            ))}
        </select>
        <span className="text-xs whitespace-nowrap text-muted tabular-nums">
          {pageRows.length === 0 ? 'No rows' : `${formatNumber(firstRow)}–${formatNumber(lastRow)}`}
          {total != null && <> of {formatNumber(total)}</>}
        </span>
        {total == null && (
          <Button size="sm" variant="ghost" onClick={() => count(tab.id, result.id)} disabled={result.counting} className="ml-1">
            {result.counting ? <Spinner size={11} /> : <Hash size={12} />}
            <span className="hidden @2xl:inline">Count</span>
          </Button>
        )}
        <div className="flex-1" />
        {result.retry && (
          <span className="mr-2 flex items-center gap-1 truncate text-xs text-warn" title={result.retry}>
            <WifiOff size={13} /> Reconnecting…
          </span>
        )}
        {!result.loading && <span className="mr-2 hidden text-xs whitespace-nowrap text-faint tabular-nums @xl:inline">{formatDuration(result.durationMs)}</span>}
        {changes > 0 && (
          <span className="mr-1 flex items-center gap-1">
            <span className="text-xs whitespace-nowrap text-warn">{changes} pending</span>
            <Button size="sm" variant="primary" onClick={showPreview}>
              Review
            </Button>
            <Button size="sm" variant="ghost" onClick={() => setPending(emptyPending())}>
              Discard
            </Button>
          </span>
        )}
        {canEdit && (
          <>
            <IconButton label="Add row" onClick={() => setPending((p) => ({ ...p, inserts: [...p.inserts, cols.map(() => null)] }))}>
              <Plus size={15} />
            </IconButton>
            <IconButton label="Duplicate selected" disabled={selected.size === 0} onClick={() => actions.onDuplicate?.(rows.filter((r) => selected.has(r.key)))}>
              <CopyPlus size={14} />
            </IconButton>
            <IconButton label="Delete selected" tone="danger" disabled={selected.size === 0} onClick={() => actions.onDelete?.(rows.filter((r) => selected.has(r.key)))}>
              <Trash2 size={14} />
            </IconButton>
            <div className="mx-1 h-4 w-px bg-line" />
          </>
        )}
        <ViewSwitch view={tab.view} documents={isDoc} onChange={(v) => updateTab(tab.id, { view: v })} />
      </div>
      {ti && !canEdit && !readOnly && (ti.key?.length ?? 0) === 0 && (
        <div className="border-b border-line bg-sunken px-3 py-1 text-[11px] text-faint">Read-only: {ti.table} has no primary key in this result, so rows cannot be identified for editing.</div>
      )}
      <div className="relative flex min-h-0 flex-1 flex-col">
        {result.loading && <div className="progress-bar absolute inset-x-0 top-0 z-30 h-0.5 overflow-hidden" />}
        {result.error && (
          <div className="flex items-start gap-2 border-b border-danger/30 bg-danger-soft px-3 py-2 text-xs text-danger">
            <AlertTriangle size={14} className="mt-px shrink-0" />
            <span className="selectable min-w-0 flex-1 break-words">{result.error}</span>
            <button className="shrink-0 font-medium hover:underline" onClick={reload}>
              Retry
            </button>
          </div>
        )}
        {rows.length === 0 && !result.loading ? (
          <div className="flex flex-1 flex-col items-center justify-center gap-1 text-center text-muted">
            <span className="text-[13px]">No rows</span>
            {ti?.where && <span className="text-xs text-faint">Try a broader filter.</span>}
          </div>
        ) : (
          <div
            ref={box}
            tabIndex={-1}
            className={cn('flex min-h-0 flex-1 flex-col outline-none', result.loading && 'opacity-60')}
            onKeyDown={(e) => {
              const el = e.target as HTMLElement
              if (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.isContentEditable) return
              const cmd = e.metaKey || e.ctrlKey
              if (isDoc) {
                if (cmd && e.key.toLowerCase() === 'a') {
                  e.preventDefault()
                  setDocSel(new Set(docs.map((_, i) => i)))
                } else if (cmd && e.key.toLowerCase() === 'c' && docSel.size > 0) {
                  e.preventDefault()
                  void copyDocs(docs, docSel)
                } else if (e.key === 'Escape') setDocSel(new Set())
                return
              }
              if (cmd && e.key.toLowerCase() === 'a') {
                e.preventDefault()
                setSelected(new Set(rows.map((r) => r.key)))
              } else if (cmd && e.key.toLowerCase() === 'c' && selected.size > 0) {
                e.preventDefault()
                void copy(rows.filter((r) => selected.has(r.key)), e.shiftKey ? 'json' : 'tsv')
              } else if (e.key === 'Escape' && selected.size > 0) {
                setSelected(new Set())
              } else if (canEdit && (e.key === 'Delete' || e.key === 'Backspace') && selected.size > 0) {
                actions.onDelete?.(rows.filter((r) => selected.has(r.key)))
              }
            }}
          >
            {isDoc ? (
              tab.view === 'json' ? (
                <DocJson docs={docs} />
              ) : tab.view === 'record' ? (
                <DocTree docs={docs} offset={result.offset} resetKey={result.id} selected={docSel} onSelect={setDocSel} />
              ) : (
                <DocTable docs={docs} offset={result.offset} selected={docSel} onSelect={setDocSel} widthKey={widthsAt} />
              )
            ) : tab.view === 'json' ? (
              <JsonRows cols={cols} rows={rows} />
            ) : tab.view === 'record' ? (
              <RecordView cols={cols} rows={rows} offset={result.offset} selected={selected} onSelect={setSelected} />
            ) : (
              <DataGrid columns={cols} rows={rows} offset={result.offset} selected={selected} onSelect={setSelected} actions={actions} widthKey={widthsAt} />
            )}
          </div>
        )}
      </div>
      <Modal
        open={!!preview}
        onOpenChange={(o) => !o && setPreview(null)}
        title={`Apply ${changes} ${changes === 1 ? 'change' : 'changes'}?`}
        description={tab.tx?.manual ? 'They run in the tab transaction; commit it to keep them.' : 'They run in one transaction: all of them apply, or none.'}
        width="max-w-3xl"
        footer={
          <>
            <Button onClick={() => preview && copyText(preview.map((p) => p.text + ';').join('\n')).then(() => toast.success('SQL copied'))}>Copy SQL</Button>
            <div className="flex-1" />
            <Button onClick={() => setPreview(null)}>Cancel</Button>
            <Button variant="primary" data-autofocus onClick={apply} disabled={applying}>
              {applying && <Spinner size={13} />}
              Apply
            </Button>
          </>
        }
      >
        <div className="h-[min(360px,50vh)] bg-sunken">
          <Editor value={(preview ?? []).map((p) => p.text + ';').join('\n')} readOnly sql={{ dialect: editor }} className="h-full" lineNumbers={false} />
        </div>
      </Modal>
    </div>
  )
}

function JsonRows({ cols, rows }: { cols: Column[]; rows: GridRow[] }) {
  const text = useMemo(() => JSON.stringify(rows.map((r) => rowObject(cols, r.values)), null, 2), [cols, rows])
  return <Editor value={text} readOnly json className="min-h-0 flex-1 overflow-hidden" />
}

function RecordView({ cols, rows, offset, selected, onSelect }: { cols: Column[]; rows: GridRow[]; offset: number; selected: Set<string>; onSelect: (s: Set<string>) => void }) {
  const idx = Math.max(0, rows.findIndex((r) => selected.has(r.key)))
  const row = rows[idx]
  const go = (i: number) => rows[i] && onSelect(new Set([rows[i].key]))
  // The column of names is resizable; unset fits the longest name.
  const [nameW, setNameW] = useState(() => viewWidth('record.name'))
  if (!row) return null
  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
      <div className="flex h-9 shrink-0 items-center gap-1 border-b border-line px-2 text-xs text-muted">
        <IconButton label="Previous row" disabled={idx === 0} onClick={() => go(idx - 1)}>
          <ChevronLeft size={15} />
        </IconButton>
        <IconButton label="Next row" disabled={idx >= rows.length - 1} onClick={() => go(idx + 1)}>
          <ChevronRight size={15} />
        </IconButton>
        <span className="tabular-nums">
          Row {row.inserted ? 'new' : formatNumber(offset + row.index + 1)} · {idx + 1} of {rows.length} on this page
        </span>
        <span className="ml-auto text-faint">
          <kbd className="font-sans">{mod}C</kbd> copies the row
        </span>
      </div>
      <div className="min-h-0 flex-1 overflow-auto">
        <div className="grid font-mono text-[12px]" style={{ gridTemplateColumns: `${nameW ? `${nameW}px` : 'minmax(120px,max-content)'} minmax(0,1fr)` }}>
          {cols.map((c, i) => (
            <div key={i} className="contents">
              <div className="relative min-w-0 border-r border-b border-line/60 bg-panel px-3 py-1.5 font-sans text-xs" title={c.name}>
                <div className="truncate font-medium text-fg">{c.name}</div>
                <div className="truncate text-[10px] text-faint lowercase">{c.type || c.kind}</div>
                <ColumnResizer
                  width={nameW}
                  min={80}
                  onChange={setNameW}
                  onCommit={(w) => saveViewWidth('record.name', w)}
                  onReset={() => {
                    setNameW(undefined)
                    saveViewWidth('record.name', null)
                  }}
                />
              </div>
              <div className={cn('flex min-w-0 items-start border-b border-line/60 px-3 py-1.5', row.changed?.has(i) && 'bg-warn/15')}>
                {row.values[i] !== null && row.values[i]!.length > 80 ? (
                  <pre className="selectable min-w-0 font-mono text-[12px] break-words whitespace-pre-wrap">{row.values[i]}</pre>
                ) : (
                  <CellValue v={row.values[i]} kind={c.kind} left />
                )}
              </div>
            </div>
          ))}
        </div>
      </div>
    </div>
  )
}
