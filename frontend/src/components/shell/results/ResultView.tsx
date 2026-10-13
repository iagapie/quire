import { useEffect, useMemo, useRef, useState } from 'react'
import {
  AlertTriangle,
  Braces,
  ChevronLeft,
  ChevronRight,
  ChevronsLeft,
  Hash,
  ListTree,
  Pencil,
  Plus,
  RefreshCw,
  RotateCcw,
  Sheet,
  Square,
  Trash2,
  WifiOff,
} from 'lucide-react'
import { toast } from 'sonner'
import { Shell, errorMessage, type Query } from '../../../lib/shell/api'
import { type BVal, field, format, parseEJSON, toCanonical } from '../../../lib/shell/ejson'
import { keys, useStore, type DocResultState, type Tab, type ViewMode } from '../../../lib/store'
import { cn, formatDuration, formatNumber } from '../../../lib/utils'
import { confirmAction } from '../../Confirm'
import { Button, IconButton, Input, Spinner } from '../../ui'
import { DocTable } from './DocTable'
import { DocTree } from './DocTree'
import { JsonView, ValueView } from './JsonView'
import { DocumentDialog, type DocDialogState } from './DocumentDialog'
import { copyDocs, type DocActions } from './common'

const PAGE_SIZES = [20, 50, 100, 250, 500, 1000]

function ViewSwitch({ view, onChange }: { view: ViewMode; onChange: (v: ViewMode) => void }) {
  const opts: { v: ViewMode; label: string; icon: React.ReactNode }[] = [
    { v: 'tree', label: 'Tree', icon: <ListTree size={13} /> },
    { v: 'table', label: 'Table', icon: <Sheet size={13} /> },
    { v: 'json', label: 'JSON', icon: <Braces size={13} /> },
  ]
  return (
    <div className="flex h-7 items-center rounded-md border border-line bg-sunken p-0.5" role="radiogroup" aria-label="View">
      {opts.map((o) => (
        <button
          key={o.v}
          role="radio"
          aria-checked={view === o.v}
          aria-label={o.label}
          title={o.label}
          onClick={() => onChange(o.v)}
          className={cn(
            'flex h-full items-center gap-1 rounded px-2 text-xs',
            view === o.v ? 'bg-raised font-medium text-fg shadow-xs' : 'text-muted hover:text-fg',
          )}
        >
          {o.icon}
          <span className="hidden @3xl:inline">{o.label}</span>
        </button>
      ))}
    </div>
  )
}

function QueryBar({ result, onApply, disabled }: { result: DocResultState; onApply: (q: Query) => void; disabled?: boolean }) {
  const applied = result.query!
  const [draft, setDraft] = useState<Query>(applied)
  useEffect(() => setDraft(applied), [applied])
  const changed = JSON.stringify(draft) !== JSON.stringify(applied)
  const set = (patch: Partial<Query>) => setDraft((d) => ({ ...d, ...patch }))
  const submit = (e?: React.FormEvent) => {
    e?.preventDefault()
    onApply({ ...draft, skip: Math.max(0, Number(draft.skip) || 0), limit: Math.max(0, Number(draft.limit) || 0) })
  }
  const label = 'shrink-0 text-[11px] font-medium text-faint uppercase tracking-wide'

  if (applied.kind === 'aggregate') {
    return (
      <form onSubmit={submit} className="flex items-start gap-2 border-b border-line bg-panel px-3 py-2">
        <span className={cn(label, 'pt-1.5')}>Pipeline</span>
        <textarea
          value={draft.pipeline}
          spellCheck={false}
          rows={Math.min(6, Math.max(1, draft.pipeline.split('\n').length))}
          onChange={(e) => set({ pipeline: e.target.value })}
          onKeyDown={(e) => (e.metaKey || e.ctrlKey) && e.key === 'Enter' && submit()}
          className="min-w-0 flex-1 resize-y rounded-md border border-line-strong bg-sunken px-2.5 py-1 font-mono text-[12px] focus:border-accent focus:outline-none"
        />
        <Button type="submit" size="sm" variant={changed ? 'primary' : 'secondary'} disabled={disabled} className="mt-0.5">
          Apply
        </Button>
      </form>
    )
  }

  return (
    <form onSubmit={submit} className="flex flex-wrap items-center gap-x-3 gap-y-1.5 border-b border-line bg-panel px-3 py-2">
      <label className="flex min-w-72 flex-[3] items-center gap-2">
        <span className={label}>Filter</span>
        <Input mono value={draft.filter} onChange={(e) => set({ filter: e.target.value })} placeholder="{ }" className="h-7" />
      </label>
      <label className="flex min-w-44 flex-[1.4] items-center gap-2">
        <span className={label}>Projection</span>
        <Input mono value={draft.projection} onChange={(e) => set({ projection: e.target.value })} placeholder="{ }" className="h-7" />
      </label>
      <label className="flex min-w-36 flex-1 items-center gap-2">
        <span className={label}>Sort</span>
        <Input mono value={draft.sort} onChange={(e) => set({ sort: e.target.value })} placeholder="{ }" className="h-7" />
      </label>
      <label className="flex items-center gap-2">
        <span className={label}>Skip</span>
        <Input mono type="number" min={0} value={draft.skip || ''} placeholder="0" onChange={(e) => set({ skip: Number(e.target.value) })} className="h-7 w-20" />
      </label>
      <label className="flex items-center gap-2">
        <span className={label}>Limit</span>
        <Input mono type="number" min={0} value={draft.limit || ''} placeholder="∞" onChange={(e) => set({ limit: Number(e.target.value) })} className="h-7 w-20" />
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

export function ResultView({ tab, result }: { tab: Tab; result: DocResultState }) {
  const { fetchDocs: fetchPage, countDocs: count, cancelResult, updateTab } = useStore.getState()
  const pageSize = useStore((s) => s.pageSize)
  const readOnly = useStore((s) => s.sessions[tab.sessionId]?.readOnly ?? false)
  const isView = useStore((s) => {
    const q = result.query
    if (!q) return false
    return s.collections[keys.db(tab.sessionId, q.database)]?.items?.find((c) => c.name === q.collection)?.type === 'view'
  })
  const [selected, setSelected] = useState<Set<number>>(new Set())
  const [dialog, setDialog] = useState<DocDialogState | null>(null)

  const docs = useMemo<BVal[]>(() => (result.page?.docs ?? []).map(parseEJSON), [result.page])
  const value = useMemo<BVal | null>(() => (result.value ? parseEJSON(result.value) : null), [result.value])
  useEffect(() => setSelected(new Set()), [result.query, result.offset])

  const q = result.query
  const editable = result.kind === 'find' && !readOnly && !isView && !!q
  const reload = () => q && fetchPage(tab.id, result.id, q, result.offset)

  const idOf = (i: number) => {
    const id = field(docs[i], '_id')
    return id ? toCanonical(id) : null
  }

  const actions: DocActions = {
    editable,
    onEdit: (i) => {
      const id = idOf(i)
      if (!q || !id) return toast.error('This document has no _id and cannot be edited')
      setDialog({ mode: 'edit', sessionId: tab.sessionId, db: q.database, coll: q.collection, id })
    },
    onDuplicate: (i) => {
      if (!q || docs[i].t !== 'object') return
      const copy: BVal = { t: 'object', v: docs[i].v.filter(([k]) => k !== '_id') }
      setDialog({ mode: 'insert', sessionId: tab.sessionId, db: q.database, coll: q.collection, text: format(copy) })
    },
    onSetField: async (i, path, text) => {
      const id = idOf(i)
      if (!q || !id) return false
      try {
        await Shell.SetField(tab.sessionId, q.database, q.collection, id, path, text)
        void reload()
        return true
      } catch (err) {
        toast.error('Could not update field', { description: errorMessage(err) })
        return false
      }
    },
    onUnsetField: async (i, path) => {
      const id = idOf(i)
      if (!q || !id) return
      const ok = await confirmAction({
        title: 'Remove field?',
        message: (
          <>
            Field <b className="font-mono text-fg">{path}</b> will be removed from this document.
          </>
        ),
        confirmLabel: 'Remove',
        danger: true,
      })
      if (!ok) return
      try {
        await Shell.UnsetField(tab.sessionId, q.database, q.collection, id, path)
        void reload()
      } catch (err) {
        toast.error('Could not remove field', { description: errorMessage(err) })
      }
    },
    onDelete: async (indices) => {
      if (!q) return
      const ids = indices.map(idOf).filter((x): x is string => !!x)
      if (ids.length === 0) return
      const ok = await confirmAction({
        title: ids.length === 1 ? 'Delete document?' : `Delete ${ids.length} documents?`,
        message: (
          <>
            {ids.length === 1 ? 'The document' : `${ids.length} documents`} will be permanently deleted from <b className="text-fg">{q.database}.{q.collection}</b>.
          </>
        ),
        confirmLabel: 'Delete',
        danger: true,
      })
      if (!ok) return
      try {
        const n = await Shell.DeleteDocuments(tab.sessionId, q.database, q.collection, ids)
        toast.success(`${n} document${n === 1 ? '' : 's'} deleted`)
        void reload()
      } catch (err) {
        toast.error('Delete failed', { description: errorMessage(err) })
      }
    },
  }

  if (result.kind === 'error') {
    return (
      <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-auto p-4">
        <div className="flex items-start gap-2.5 rounded-lg border border-danger/30 bg-danger-soft p-3 text-danger">
          <AlertTriangle size={16} className="mt-0.5 shrink-0" />
          <div className="selectable min-w-0 font-mono text-[12px] break-words whitespace-pre-wrap">{result.error}</div>
        </div>
        {result.statement && (
          <pre className="selectable rounded-lg bg-sunken p-3 font-mono text-[12px] whitespace-pre-wrap text-muted">{result.statement}</pre>
        )}
      </div>
    )
  }

  if (result.kind === 'value' && value) {
    const asDocs = value.t === 'array' && value.v.length > 0 && value.v.every((x) => x.t === 'object') ? value.v : value.t === 'object' ? [value] : null
    return (
      <div className="flex min-h-0 flex-1 flex-col">
        <div className="flex h-10 shrink-0 items-center gap-2 border-b border-line px-3 text-xs text-muted">
          <span>{asDocs ? `${asDocs.length} document${asDocs.length === 1 ? '' : 's'}` : 'Value'}</span>
          <span className="ml-auto tabular-nums">{formatDuration(result.durationMs)}</span>
          {asDocs && <ViewSwitch view={tab.view} onChange={(v) => updateTab(tab.id, { view: v })} />}
        </div>
        {asDocs ? (
          <DocsBody view={tab.view} docs={asDocs} offset={0} selected={selected} onSelect={setSelected} actions={{ editable: false }} />
        ) : (
          <ValueView value={value} />
        )}
      </div>
    )
  }

  const page = result.page
  const first = result.offset + 1
  const last = result.offset + docs.length
  const total = result.count

  return (
    <div className="@container flex min-h-0 flex-1 flex-col">
      {q && <QueryBar result={result} disabled={result.loading} onApply={(nq) => fetchPage(tab.id, result.id, nq, 0)} />}
      <div className="flex h-10 shrink-0 items-center gap-1 scroll-x border-b border-line px-2">
        {result.loading ? (
          <IconButton label="Stop" tone="danger" onClick={() => cancelResult(tab.id, result.id)}>
            <Square size={13} fill="currentColor" />
          </IconButton>
        ) : (
          <IconButton label="Refresh" onClick={reload}>
            <RefreshCw size={14} />
          </IconButton>
        )}
        <div className="mx-1 h-4 w-px bg-line" />
        <IconButton label="First page" disabled={result.offset === 0 || result.loading} onClick={() => q && fetchPage(tab.id, result.id, q, 0)}>
          <ChevronsLeft size={15} />
        </IconButton>
        <IconButton
          label="Previous page"
          disabled={result.offset === 0 || result.loading}
          onClick={() => q && fetchPage(tab.id, result.id, q, Math.max(0, result.offset - result.pageSize))}
        >
          <ChevronLeft size={15} />
        </IconButton>
        <IconButton
          label="Next page"
          disabled={!page?.hasMore || result.loading}
          onClick={() => q && fetchPage(tab.id, result.id, q, result.offset + result.pageSize)}
        >
          <ChevronRight size={15} />
        </IconButton>
        <select
          aria-label="Page size"
          value={result.pageSize}
          onChange={(e) => {
            const size = Number(e.target.value)
            useStore.setState({ pageSize: size })
            if (q) void fetchPage(tab.id, result.id, q, 0, size)
          }}
          className="mx-1 h-7 rounded-md border border-line bg-sunken px-1.5 text-xs text-fg focus:outline-none"
        >
          {[...new Set([...PAGE_SIZES, pageSize, result.pageSize])].sort((a, b) => a - b).map((n) => (
            <option key={n} value={n}>
              {n} / page
            </option>
          ))}
        </select>
        <span className="text-xs whitespace-nowrap text-muted tabular-nums">
          {docs.length === 0 ? 'No documents' : `${formatNumber(first)}–${formatNumber(last)}`}
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
        {!result.loading && <span className="mr-2 hidden text-xs whitespace-nowrap @xl:inline text-faint tabular-nums">{formatDuration(result.durationMs)}</span>}
        {editable && (
          <>
            <IconButton label="Insert document" onClick={() => q && setDialog({ mode: 'insert', sessionId: tab.sessionId, db: q.database, coll: q.collection })}>
              <Plus size={15} />
            </IconButton>
            <IconButton label="Edit document" disabled={selected.size !== 1} onClick={() => actions.onEdit?.([...selected][0])}>
              <Pencil size={14} />
            </IconButton>
            <IconButton label="Delete selected" tone="danger" disabled={selected.size === 0} onClick={() => actions.onDelete?.([...selected])}>
              <Trash2 size={14} />
            </IconButton>
            <div className="mx-1 h-4 w-px bg-line" />
          </>
        )}
        <ViewSwitch view={tab.view} onChange={(v) => updateTab(tab.id, { view: v })} />
      </div>
      <div className="relative flex min-h-0 flex-1 flex-col">
        {result.loading && <div className="progress-bar absolute inset-x-0 top-0 z-20 h-0.5 overflow-hidden" />}
        {result.error && (
          <div className="flex items-start gap-2 border-b border-danger/30 bg-danger-soft px-3 py-2 text-xs text-danger">
            <AlertTriangle size={14} className="mt-px shrink-0" />
            <span className="selectable min-w-0 flex-1 break-words">{result.error}</span>
            <button className="shrink-0 font-medium hover:underline" onClick={reload}>
              Retry
            </button>
          </div>
        )}
        {docs.length === 0 && !result.loading ? (
          <div className="flex flex-1 flex-col items-center justify-center gap-1 text-center text-muted">
            <span className="text-[13px]">No documents found</span>
            {q?.filter && <span className="text-xs text-faint">Try a broader filter.</span>}
          </div>
        ) : (
          <div className={cn('flex min-h-0 flex-1 flex-col', result.loading && 'opacity-60')}>
            <DocsBody view={tab.view} docs={docs} offset={result.offset} resetKey={`${JSON.stringify(q)}:${result.offset}`}
              widthsKey={q && `${tab.sessionId}/${q.database}/${q.collection}`}
              selected={selected} onSelect={setSelected} actions={actions} />
          </div>
        )}
      </div>
      <DocumentDialog state={dialog} onClose={() => setDialog(null)} onSaved={reload} />
    </div>
  )
}

function DocsBody({
  view,
  docs,
  offset,
  resetKey,
  widthsKey,
  selected,
  onSelect,
  actions,
}: {
  view: ViewMode
  docs: BVal[]
  offset: number
  resetKey?: string
  widthsKey?: string
  selected: Set<number>
  onSelect: (s: Set<number>) => void
  actions: DocActions
}) {
  const box = useRef<HTMLDivElement>(null)

  // The native Edit > Copy menu sends a copy event instead of a key press.
  useEffect(() => {
    const onCopy = (e: ClipboardEvent) => {
      if (!box.current?.contains(document.activeElement) || isTextField(document.activeElement) || selected.size === 0) return
      e.preventDefault()
      void copyDocs(docs, selected)
    }
    document.addEventListener('copy', onCopy)
    return () => document.removeEventListener('copy', onCopy)
  }, [docs, selected])

  if (view === 'json') return <JsonView docs={docs} offset={offset} />
  return (
    <div
      ref={box}
      tabIndex={-1}
      className="flex min-h-0 flex-1 flex-col outline-none"
      onKeyDown={(e) => {
        if (isTextField(e.target as Element)) return
        const cmd = e.metaKey || e.ctrlKey
        if (cmd && e.key.toLowerCase() === 'a') {
          e.preventDefault()
          onSelect(new Set(docs.map((_, i) => i)))
        } else if (cmd && e.key.toLowerCase() === 'c' && selected.size > 0) {
          e.preventDefault()
          void copyDocs(docs, selected, e.shiftKey)
        } else if (e.key === 'Escape' && selected.size > 0) {
          onSelect(new Set())
        } else if (actions.editable && (e.key === 'Delete' || e.key === 'Backspace') && selected.size > 0) {
          actions.onDelete?.([...selected])
        } else if (actions.editable && e.key === 'Enter' && selected.size === 1) {
          actions.onEdit?.([...selected][0])
        }
      }}
    >
      {view === 'table' ? (
        <DocTable docs={docs} offset={offset} selected={selected} onSelect={onSelect} actions={actions} widthsKey={widthsKey} key={widthsKey} />
      ) : (
        <DocTree docs={docs} offset={offset} resetKey={resetKey} widthsKey={widthsKey} key={widthsKey} selected={selected} onSelect={onSelect} actions={actions} />
      )}
    </div>
  )
}

function isTextField(el: Element | null): boolean {
  return !!el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.tagName === 'SELECT' || (el as HTMLElement).isContentEditable)
}
