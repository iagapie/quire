import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import {
  AlertTriangle,
  ArrowRight,
  Ban,
  Check,
  ChevronRight,
  Circle,
  Download,
  Eye,
  FileJson,
  FolderOpen,
  Info,
  Plus,
  RefreshCw,
  RotateCcw,
  Search,
  Square,
  Table2,
  Upload,
  X,
  XCircle,
} from 'lucide-react'
import { toast } from 'sonner'
import { Events } from '@wailsio/runtime'
import { Transfers, errorMessage, type CollectionStat, type FilesDropped, type ImportFile, type ImportScan, type TransferItem, type TransferJob } from '../../lib/shell/api'
import { keys, useStore, type Tab } from '../../lib/store'
import {
  FORMATS,
  IMPORT_MODES,
  clock,
  compact,
  eta,
  exportFileName,
  isFinished,
  itemFraction,
  jobFraction,
  jobTitle,
  prettyPath,
  rate,
  totals,
  type TransferMode,
} from '../../lib/shell/transfer'
import { openCollection } from '../../lib/shell/ui'
import { cn, formatBytes, formatNumber, isMac, readLocal, writeLocal } from '../../lib/utils'

const REVEAL = isMac ? 'Show in Finder' : 'Show in folder'
/** Without progress for this long, a running transfer is shown as waiting for the server. */
const STALL_MS = 6000
import { confirmAction } from '../Confirm'
import { Button, IconButton, Input, Spinner, Switch } from '../ui'

// ---------------------------------------------------------------------------
// Shared pieces

export function ProgressBar({ value, tone = 'accent', className }: { value: number | null; tone?: 'accent' | 'danger' | 'warn' | 'muted'; className?: string }) {
  const color = { accent: 'bg-accent', danger: 'bg-danger', warn: 'bg-warn', muted: 'bg-faint' }[tone]
  return (
    <div
      role="progressbar"
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={value === null ? undefined : Math.round(value * 100)}
      className={cn('relative h-1.5 overflow-hidden rounded-full bg-sunken', value === null && 'progress-bar', className)}
    >
      {value !== null && <div className={cn('h-full rounded-full transition-[width] duration-300 ease-out', color)} style={{ width: `${Math.max(0, Math.min(1, value)) * 100}%` }} />}
    </div>
  )
}

/** A small circular progress indicator, for tabs and the sidebar. */
export function ProgressRing({ value, size = 14, className }: { value: number; size?: number; className?: string }) {
  const r = 5
  const c = 2 * Math.PI * r
  return (
    <svg width={size} height={size} viewBox="0 0 14 14" className={cn('-rotate-90 shrink-0', className)} aria-hidden>
      <circle cx="7" cy="7" r={r} fill="none" stroke="currentColor" strokeOpacity="0.2" strokeWidth="2" />
      <circle cx="7" cy="7" r={r} fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeDasharray={c} strokeDashoffset={c * (1 - Math.max(0.03, value))} className="transition-[stroke-dashoffset] duration-300" />
    </svg>
  )
}

function Section({ title, hint, aside, children }: { title: ReactNode; hint?: ReactNode; aside?: ReactNode; children: ReactNode }) {
  return (
    <section className="flex flex-col gap-3">
      <div className="flex items-end justify-between gap-3">
        <div className="min-w-0">
          <h3 className="text-[13px] font-semibold">{title}</h3>
          {hint && <p className="mt-0.5 text-xs text-faint">{hint}</p>}
        </div>
        {aside}
      </div>
      {children}
    </section>
  )
}

function Choice({ selected, onSelect, title, hint, badge, tone = 'accent' }: { selected: boolean; onSelect: () => void; title: ReactNode; hint: ReactNode; badge?: ReactNode; tone?: 'accent' | 'danger' }) {
  return (
    <button
      type="button"
      role="radio"
      aria-checked={selected}
      onClick={onSelect}
      className={cn(
        'flex min-w-0 flex-col items-start gap-1 rounded-lg border px-3 py-2.5 text-left transition-colors',
        selected
          ? tone === 'danger'
            ? 'border-danger bg-danger-soft'
            : 'border-accent bg-accent-soft'
          : 'border-line-strong hover:border-faint hover:bg-hover',
      )}
    >
      <span className="flex w-full items-center gap-2 text-[13px] font-medium">
        <span
          className={cn(
            'flex size-3.5 shrink-0 items-center justify-center rounded-full border',
            selected ? (tone === 'danger' ? 'border-danger bg-danger' : 'border-accent bg-accent') : 'border-line-strong',
          )}
        >
          {selected && <span className="size-1.5 rounded-full bg-white" />}
        </span>
        {title}
        {badge && <span className="ml-auto font-mono text-[11px] font-normal text-faint">{badge}</span>}
      </span>
      <span className="pl-5.5 text-[11px] leading-snug text-muted">{hint}</span>
    </button>
  )
}

function Header({ tab, mode, children }: { tab: Tab; mode: TransferMode; children?: ReactNode }) {
  const profile = useStore((s) => s.profiles.find((p) => p.id === tab.sessionId))
  const session = useStore((s) => s.sessions[tab.sessionId])
  return (
    <div className="flex h-11 shrink-0 items-center gap-1.5 border-b border-line bg-panel px-3">
      <span className={cn('flex size-6 items-center justify-center rounded-md', mode === 'export' ? 'bg-accent-soft text-accent' : 'bg-sunken text-fg')}>
        {mode === 'export' ? <Download size={13} /> : <Upload size={13} />}
      </span>
      <span className="text-[13px] font-semibold">{mode === 'export' ? 'Export' : 'Import'}</span>
      <div className="ml-2 flex min-w-0 items-center gap-1 text-xs text-muted">
        <span className="truncate">{profile?.name}</span>
        <ChevronRight size={12} className="shrink-0 text-faint" />
        <span className="truncate font-medium text-fg">{tab.database}</span>
        {!session && (
          <button className="ml-1 rounded px-1.5 py-0.5 font-medium text-accent hover:bg-accent-soft" onClick={() => useStore.getState().connect(tab.sessionId)}>
            Connect
          </button>
        )}
      </div>
      <div className="ml-auto flex shrink-0 items-center gap-1.5">{children}</div>
    </div>
  )
}

function Footer({ summary, children }: { summary: ReactNode; children: ReactNode }) {
  return (
    <div className="flex h-14 shrink-0 items-center gap-3 border-t border-line bg-panel px-4">
      <div className="min-w-0 flex-1 truncate text-xs text-muted">{summary}</div>
      {children}
    </div>
  )
}

function Checkbox({ checked, indeterminate, onChange, label }: { checked: boolean; indeterminate?: boolean; onChange: (v: boolean, e: React.MouseEvent) => void; label?: string }) {
  return (
    <button
      type="button"
      role="checkbox"
      aria-checked={indeterminate ? 'mixed' : checked}
      aria-label={label}
      onClick={(e) => {
        e.stopPropagation()
        onChange(!checked, e)
      }}
      className={cn(
        'flex size-4 shrink-0 items-center justify-center rounded border transition-colors',
        checked || indeterminate ? 'border-accent bg-accent text-accent-fg' : 'border-line-strong bg-raised hover:border-faint',
      )}
    >
      {indeterminate ? <span className="h-0.5 w-2 rounded bg-current" /> : checked && <Check size={11} strokeWidth={3} />}
    </button>
  )
}

const plural = (n: number, one: string, many = `${one}s`) => `${formatNumber(n)} ${n === 1 ? one : many}`

// ---------------------------------------------------------------------------
// Events

/** Stores a job snapshot and announces jobs that finish while their tab is not in view. */
export function onDocTransferEvent(job: TransferJob) {
  const s = useStore.getState()
  const prev = s.docJobs[job.id]
  s.setDocJob(job)
  if (!prev || prev.state !== 'running' || !isFinished(job.state)) return
  if (job.kind === 'import') {
    void s.loadCollections(job.sessionId, job.database)
    for (const it of job.items ?? []) if (s.docIndexes[keys.coll(job.sessionId, job.database, it.collection)]) void s.loadDocIndexes(job.sessionId, job.database, it.collection)
  }
  const tab = s.tabs.find((t) => t.transfer?.jobId === job.id)
  if (job.state === 'cancelled' || (tab && tab.id === s.activeTab && document.hasFocus())) return
  const t = totals(job)
  const description = `${plural(t.done, 'collection')} · ${plural(t.docs, 'document')}${t.failedItems ? ` · ${t.failedItems} failed` : ''}`
  const view = { label: 'View', onClick: () => useStore.getState().openDocTransferTab(job.kind as TransferMode, job.sessionId, job.database, { jobId: job.id }) }
  if (job.state === 'done') toast.success(jobTitle(job), { description, action: view })
  else toast.error(jobTitle(job), { description, action: view, duration: 10_000 })
}

// ---------------------------------------------------------------------------
// Tab

export function ShellTransferTab({ tab }: { tab: Tab }) {
  const ref = tab.docTransfer!
  if (ref.jobId) return <JobView tab={tab} jobId={ref.jobId} />
  return ref.mode === 'export' ? <ExportSetup tab={tab} /> : <ImportSetup tab={tab} />
}

function started(tab: Tab, job: TransferJob) {
  const s = useStore.getState()
  s.setDocJob(job)
  s.updateTab(tab.id, { docTransfer: { ...tab.docTransfer!, jobId: job.id }, title: `${job.kind === 'export' ? 'Export' : 'Import'} · ${tab.database}` })
}

// ---------------------------------------------------------------------------
// Export setup

interface ExportPrefs {
  format: string
  canonical: boolean
  gzip: boolean
  indexes: boolean
  dir: string
}

const EXPORT_PREFS = 'quire.doc.export'

function ExportSetup({ tab }: { tab: Tab }) {
  const session = useStore((s) => s.sessions[tab.sessionId])
  const [stats, setStats] = useState<CollectionStat[] | null>(null)
  const [loadError, setLoadError] = useState('')
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [search, setSearch] = useState('')
  const [prefs, setPrefs] = useState<ExportPrefs>(() => readLocal(EXPORT_PREFS, { format: 'jsonl', canonical: true, gzip: false, indexes: true, dir: '' }))
  const [starting, setStarting] = useState(false)
  const [error, setError] = useState('')
  const anchor = useRef<number | null>(null)

  const setPref = (patch: Partial<ExportPrefs>) =>
    setPrefs((p) => {
      const next = { ...p, ...patch }
      writeLocal(EXPORT_PREFS, next)
      return next
    })

  useEffect(() => {
    if (!session) return
    let live = true
    setLoadError('')
    Transfers.CollectionStats(tab.sessionId, tab.database)
      .then((list) => {
        if (!live) return
        const all = list ?? []
        setStats(all)
        const wanted = tab.docTransfer?.collections
        setSelected(new Set(wanted?.length ? all.filter((c) => wanted.includes(c.name)).map((c) => c.name) : all.filter((c) => c.type !== 'view').map((c) => c.name)))
      })
      .catch((err) => live && setLoadError(errorMessage(err)))
    return () => {
      live = false
    }
  }, [session, tab.sessionId, tab.database, tab.docTransfer?.collections])

  const visible = useMemo(() => (stats ?? []).filter((c) => c.name.toLowerCase().includes(search.trim().toLowerCase())), [stats, search])
  const chosen = (stats ?? []).filter((c) => selected.has(c.name))
  const docs = chosen.reduce((n, c) => n + Math.max(0, c.count), 0)
  const size = chosen.reduce((n, c) => n + Math.max(0, c.size), 0)
  const allVisible = visible.length > 0 && visible.every((c) => selected.has(c.name))
  const someVisible = visible.some((c) => selected.has(c.name))
  const isJSON = prefs.format !== 'bson'

  const toggle = (i: number, on: boolean, e?: React.MouseEvent) => {
    setSelected((cur) => {
      const next = new Set(cur)
      const range = e?.shiftKey && anchor.current !== null ? visible.slice(Math.min(anchor.current, i), Math.max(anchor.current, i) + 1) : [visible[i]]
      for (const c of range) {
        if (on) next.add(c.name)
        else next.delete(c.name)
      }
      return next
    })
    anchor.current = i
  }

  const chooseDir = async () => {
    try {
      const dir = await Transfers.PickExportDir()
      if (dir) setPref({ dir })
    } catch (err) {
      toast.error('Could not open the folder picker', { description: errorMessage(err) })
    }
  }

  const start = async () => {
    if (!prefs.dir) {
      await chooseDir()
      return
    }
    setStarting(true)
    setError('')
    try {
      const job = await Transfers.StartExport({
        sessionId: tab.sessionId,
        database: tab.database,
        collections: (stats ?? []).filter((c) => selected.has(c.name)).map((c) => c.name),
        dir: prefs.dir,
        format: prefs.format,
        canonical: prefs.canonical,
        gzip: prefs.gzip,
        indexes: prefs.indexes,
      })
      started(tab, job)
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setStarting(false)
    }
  }

  const files = chosen.slice(0, 3).map((c) => exportFileName(c.name, prefs.format, prefs.gzip))

  return (
    <div className="flex h-full min-h-0 flex-col">
      <Header tab={tab} mode="export" />
      <div className="min-h-0 flex-1 overflow-auto">
        <div className="mx-auto flex max-w-3xl flex-col gap-6 px-6 py-5">
          {error && <div className="selectable rounded-md bg-danger-soft px-3 py-2 text-xs text-danger">{error}</div>}

          <Section
            title="Collections"
            hint={stats ? `${selected.size} of ${stats.length} selected` : 'Loading…'}
            aside={
              <div className="relative w-56">
                <Search size={13} className="pointer-events-none absolute top-1/2 left-2 -translate-y-1/2 text-faint" />
                <Input value={search} onChange={(e) => setSearch(e.target.value)} placeholder="Filter" className="h-7 w-full pl-7 text-xs" />
              </div>
            }
          >
            <div className="overflow-hidden rounded-lg border border-line">
              <div className="flex h-8 items-center gap-3 border-b border-line bg-sunken px-3 text-[11px] font-medium text-faint">
                <Checkbox
                  checked={allVisible}
                  indeterminate={!allVisible && someVisible}
                  label="Select all"
                  onChange={() =>
                    setSelected((cur) => {
                      const next = new Set(cur)
                      for (const c of visible) {
                        if (allVisible) next.delete(c.name)
                        else next.add(c.name)
                      }
                      return next
                    })
                  }
                />
                <span className="flex-1">Name</span>
                <span className="w-24 text-right">Documents</span>
                <span className="w-20 text-right">Size</span>
              </div>
              <div className="max-h-72 overflow-auto">
                {!stats && !loadError && (
                  <div className="flex h-20 items-center justify-center text-muted">
                    <Spinner />
                  </div>
                )}
                {loadError && <div className="px-3 py-3 text-xs text-danger">{loadError}</div>}
                {stats && visible.length === 0 && <div className="px-3 py-4 text-center text-xs text-faint">No collections match.</div>}
                {visible.map((c, i) => {
                  const on = selected.has(c.name)
                  return (
                    <div
                      key={c.name}
                      onClick={(e) => toggle(i, !on, e)}
                      className={cn('flex h-8 cursor-default items-center gap-3 border-b border-line/60 px-3 text-[13px] last:border-b-0', on ? 'bg-accent-soft/40' : 'hover:bg-hover')}
                    >
                      <Checkbox checked={on} label={c.name} onChange={(v, e) => toggle(i, v, e)} />
                      <span className="flex min-w-0 flex-1 items-center gap-2">
                        {c.type === 'view' ? <Eye size={13} className="shrink-0 text-muted" /> : <Table2 size={13} className="shrink-0 text-muted" />}
                        <span className="truncate">{c.name}</span>
                        {c.type === 'view' && <span className="rounded bg-sunken px-1 text-[10px] text-faint">view</span>}
                      </span>
                      <span className="w-24 text-right text-xs text-muted tabular-nums">{c.count >= 0 ? formatNumber(c.count) : '—'}</span>
                      <span className="w-20 text-right text-xs text-muted tabular-nums">{c.size >= 0 ? formatBytes(c.size) : '—'}</span>
                    </div>
                  )
                })}
              </div>
            </div>
          </Section>

          <Section title="Format">
            <div role="radiogroup" className="grid grid-cols-3 gap-2">
              {FORMATS.map((f) => (
                <Choice key={f.value} selected={prefs.format === f.value} onSelect={() => setPref({ format: f.value })} title={f.label} hint={f.hint} badge={f.ext} />
              ))}
            </div>
            {isJSON && (
              <div className="flex items-center gap-3">
                <span className="text-xs whitespace-nowrap text-muted">Extended JSON</span>
                <div role="radiogroup" className="flex h-7 items-center rounded-md border border-line bg-sunken p-0.5 text-xs">
                  {[
                    { v: true, label: 'Canonical', hint: 'Keeps every BSON type exactly; best for re-importing.' },
                    { v: false, label: 'Relaxed', hint: 'Plain numbers and dates; easier to read, may lose number types.' },
                  ].map((o) => (
                    <button
                      key={o.label}
                      role="radio"
                      aria-checked={prefs.canonical === o.v}
                      title={o.hint}
                      onClick={() => setPref({ canonical: o.v })}
                      className={cn('h-full rounded px-2.5 transition-colors', prefs.canonical === o.v ? 'bg-raised text-fg shadow-xs' : 'text-muted hover:text-fg')}
                    >
                      {o.label}
                    </button>
                  ))}
                </div>
                <span className="text-[11px] text-faint">{prefs.canonical ? 'Exact types, best for re-importing.' : 'Easier to read; number types may change on import.'}</span>
              </div>
            )}
          </Section>

          <Section title="Options">
            <div className="grid grid-cols-2 gap-x-6 gap-y-3">
              <Switch checked={prefs.gzip} onChange={(v) => setPref({ gzip: v })} label="Compress with gzip" hint="Smaller files (.gz); import reads them as is." />
              <Switch checked={prefs.indexes} onChange={(v) => setPref({ indexes: v })} label="Save indexes" hint="Writes <collection>.metadata.json, as mongodump does; restored on import." />
            </div>
          </Section>

          <Section title="Destination" hint="One file per collection. Existing files with the same name are replaced.">
            <div className="flex items-center gap-2">
              <button
                onClick={chooseDir}
                className="flex h-8 min-w-0 flex-1 items-center gap-2 rounded-md border border-line-strong bg-sunken px-2.5 text-left text-[13px] hover:border-faint"
              >
                <FolderOpen size={14} className="shrink-0 text-muted" />
                {prefs.dir ? <span className="truncate font-mono text-xs">{prettyPath(prefs.dir)}</span> : <span className="text-faint">Choose a folder…</span>}
              </button>
              <Button onClick={chooseDir}>Choose…</Button>
            </div>
            {files.length > 0 && (
              <p className="flex flex-wrap items-center gap-1.5 text-[11px] text-faint">
                {files.map((f) => (
                  <span key={f} className="rounded bg-sunken px-1.5 py-0.5 font-mono text-muted">
                    {f}
                  </span>
                ))}
                {chosen.length > files.length && <span>and {chosen.length - files.length} more</span>}
              </p>
            )}
          </Section>
        </div>
      </div>
      <Footer
        summary={
          selected.size > 0 ? (
            <>
              <b className="font-medium text-fg">{plural(selected.size, 'collection')}</b>
              {docs > 0 && <> · about {plural(docs, 'document')}</>}
              {size > 0 && <> · {formatBytes(size)} uncompressed</>}
            </>
          ) : (
            'Select the collections to export.'
          )
        }
      >
        <Button variant="primary" onClick={start} disabled={starting || !session || selected.size === 0}>
          {starting ? <Spinner size={13} /> : <Download size={14} />}
          {!prefs.dir ? 'Choose folder…' : stats ? `Export ${plural(selected.size, 'collection')}` : 'Export'}
        </Button>
      </Footer>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Import setup

function ImportSetup({ tab }: { tab: Tab }) {
  const session = useStore((s) => s.sessions[tab.sessionId])
  const existing = useStore((s) => s.collections[keys.db(tab.sessionId, tab.database)]?.items)
  const preset = tab.docTransfer?.collections?.[0]
  const [files, setFiles] = useState<ImportFile[]>([])
  const [skipped, setSkipped] = useState<string[]>([])
  const [mode, setMode] = useState('insert')
  const [indexes, setIndexes] = useState(true)
  const [stopOnError, setStopOnError] = useState(false)
  const [picking, setPicking] = useState(false)
  const [starting, setStarting] = useState(false)
  const [error, setError] = useState('')
  const asked = useRef(false)
  const listId = `colls-${tab.id}`

  useEffect(() => {
    if (session && !existing) void useStore.getState().loadCollections(tab.sessionId, tab.database)
  }, [session, existing, tab.sessionId, tab.database])

  const add = (scan: ImportScan) => {
    const added = (scan.files ?? []).map((f) => (preset ? { ...f, collection: preset } : f))
    setFiles((cur) => [...cur, ...added.filter((f) => !cur.some((c) => c.path === f.path))])
    setSkipped(scan.skipped ?? [])
  }

  // Files dragged from Finder onto this tab.
  const dropId = `doc-import-${tab.id}`
  useEffect(
    () => Events.On('doc-import:dropped', (e: { data: FilesDropped }) => e.data.target === dropId && add(e.data.scan)),
    [dropId, preset],
  )

  const pick = async () => {
    setPicking(true)
    try {
      add(await Transfers.PickImportFiles())
    } catch (err) {
      toast.error('Could not open the file picker', { description: errorMessage(err) })
    } finally {
      setPicking(false)
    }
  }

  // Opening an import goes straight to choosing files.
  useEffect(() => {
    if (asked.current) return
    asked.current = true
    const t = setTimeout(() => void pick(), 120)
    return () => clearTimeout(t)
  }, [])

  const names = new Set((existing ?? []).map((c) => c.name))
  const views = new Set((existing ?? []).filter((c) => c.type === 'view').map((c) => c.name))
  const withIndexes = files.filter((f) => f.indexes > 0).length
  const totalBytes = files.reduce((n, f) => n + f.size, 0)
  const targets = [...new Set(files.map((f) => f.collection.trim()))]
  const invalid = files.some((f) => !f.collection.trim() || views.has(f.collection.trim()))

  const start = async () => {
    if (mode === 'replace') {
      const drop = targets.filter((t) => names.has(t))
      if (drop.length) {
        const ok = await confirmAction({
          title: `Drop ${plural(drop.length, 'collection')}?`,
          message: (
            <>
              {drop.slice(0, 6).map((d, i) => (
                <span key={d}>
                  {i > 0 && ', '}
                  <b className="text-fg">{d}</b>
                </span>
              ))}
              {drop.length > 6 && ` and ${drop.length - 6} more`} will be dropped in <b className="text-fg">{tab.database}</b> and replaced with the imported documents. This cannot be undone.
            </>
          ),
          confirmLabel: 'Drop and import',
          danger: true,
        })
        if (!ok) return
      }
    }
    setStarting(true)
    setError('')
    try {
      const job = await Transfers.StartImport({ sessionId: tab.sessionId, database: tab.database, files, mode, indexes, stopOnError })
      started(tab, job)
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setStarting(false)
    }
  }

  return (
    <div id={dropId} data-file-drop-target className="drop-zone flex h-full min-h-0 flex-col">
      <Header tab={tab} mode="import" />
      <div className="min-h-0 flex-1 overflow-auto">
        <div className="mx-auto flex max-w-3xl flex-col gap-6 px-6 py-5">
          {error && <div className="selectable rounded-md bg-danger-soft px-3 py-2 text-xs text-danger">{error}</div>}
          {session?.readOnly && <div className="rounded-md bg-sunken px-3 py-2 text-xs text-muted">This connection is read-only; importing is disabled.</div>}

          <Section
            title="Files"
            hint="Dumps from Quire, mongoexport or mongodump: .jsonl, .json or .bson, optionally gzip-compressed. Pick or drop files or a whole folder."
            aside={
              files.length > 0 && (
                <Button size="sm" onClick={pick} disabled={picking}>
                  <Plus size={13} /> Add files
                </Button>
              )
            }
          >
            {files.length === 0 ? (
              <button
                onClick={pick}
                disabled={picking}
                className="flex flex-col items-center gap-2 rounded-lg border border-dashed border-line-strong px-6 py-10 text-center hover:border-accent hover:bg-accent-soft/30"
              >
                <span className="flex size-10 items-center justify-center rounded-full bg-sunken text-muted">{picking ? <Spinner /> : <Upload size={18} />}</span>
                <span className="text-[13px] font-medium">Choose files or a folder</span>
                <span className="text-xs text-faint">Or drop them here. Each file becomes a collection named after it; you can change the target below.</span>
              </button>
            ) : (
              <div className="overflow-hidden rounded-lg border border-line">
                <div className="flex h-8 items-center gap-3 border-b border-line bg-sunken px-3 text-[11px] font-medium text-faint">
                  <span className="flex-1">File</span>
                  <span className="w-5" />
                  <span className="w-[42%] max-w-72 min-w-40 truncate">Target collection in {tab.database}</span>
                  <span className="w-7" />
                </div>
                {files.map((f, i) => {
                  const target = f.collection.trim()
                  return (
                    <div key={f.path} className="flex min-h-11 items-center gap-3 border-b border-line/60 px-3 py-1.5 last:border-b-0">
                      <FileJson size={15} className="shrink-0 text-muted" />
                      <span className="flex min-w-0 flex-1 flex-col">
                        <span className="truncate text-[13px]" title={f.path}>
                          {f.name}
                        </span>
                        <span className="flex items-center gap-1.5 overflow-hidden text-[11px] whitespace-nowrap text-faint">
                          <span className="tabular-nums">{formatBytes(f.size)}</span>
                          <span className="rounded bg-sunken px-1 font-mono uppercase">{f.format}</span>
                          {f.gzip && <span className="rounded bg-sunken px-1 font-mono">gzip</span>}
                          {f.indexes > 0 && <span className="rounded bg-sunken px-1">{plural(f.indexes, 'index', 'indexes')}</span>}
                        </span>
                      </span>
                      <ArrowRight size={13} className="shrink-0 text-faint" />
                      <span className="relative w-[42%] max-w-72 min-w-40 shrink-0">
                        <Input
                          mono
                          list={listId}
                          value={f.collection}
                          onChange={(e) => setFiles((cur) => cur.map((x, j) => (j === i ? { ...x, collection: e.target.value } : x)))}
                          className={cn('h-7 w-full pr-16 text-xs', (!target || views.has(target)) && 'border-danger')}
                        />
                        <span
                          className={cn(
                            'pointer-events-none absolute top-1/2 right-1.5 -translate-y-1/2 rounded px-1 text-[10px]',
                            views.has(target) ? 'bg-danger-soft text-danger' : names.has(target) ? 'bg-sunken text-muted' : 'bg-accent-soft text-accent',
                          )}
                        >
                          {!target ? '' : views.has(target) ? 'view' : names.has(target) ? 'existing' : 'new'}
                        </span>
                      </span>
                      <IconButton label="Remove" onClick={() => setFiles((cur) => cur.filter((_, j) => j !== i))}>
                        <X size={14} />
                      </IconButton>
                    </div>
                  )
                })}
                <datalist id={listId}>
                  {(existing ?? []).filter((c) => c.type !== 'view').map((c) => (
                    <option key={c.name} value={c.name} />
                  ))}
                </datalist>
              </div>
            )}
            {skipped.length > 0 && (
              <p className="flex items-start gap-1.5 text-[11px] text-warn">
                <AlertTriangle size={12} className="mt-px shrink-0" />
                <span>
                  Not a supported dump, skipped: {skipped.slice(0, 5).join(', ')}
                  {skipped.length > 5 && ` and ${skipped.length - 5} more`}
                </span>
              </p>
            )}
          </Section>

          <Section title="When documents already exist">
            <div role="radiogroup" className="grid grid-cols-3 gap-2">
              {IMPORT_MODES.map((m) => (
                <Choice key={m.value} selected={mode === m.value} onSelect={() => setMode(m.value)} title={m.label} hint={m.hint} tone={m.value === 'replace' ? 'danger' : 'accent'} />
              ))}
            </div>
          </Section>

          <Section title="Options">
            <div className="grid grid-cols-2 gap-x-6 gap-y-3">
              <Switch
                checked={indexes && withIndexes > 0}
                disabled={withIndexes === 0}
                onChange={setIndexes}
                label="Restore indexes"
                hint={withIndexes > 0 ? `From the .metadata.json of ${plural(withIndexes, 'file')}.` : 'No .metadata.json found next to the files.'}
              />
              <Switch checked={stopOnError} onChange={setStopOnError} label="Stop at the first rejected document" hint="Otherwise rejected documents are counted and the import goes on." />
            </div>
          </Section>
        </div>
      </div>
      <Footer
        summary={
          files.length > 0 ? (
            <>
              <b className="font-medium text-fg">{plural(files.length, 'file')}</b> · {formatBytes(totalBytes)} → {plural(targets.length, 'collection')} in <b className="font-medium text-fg">{tab.database}</b>
            </>
          ) : (
            'Add the files to import.'
          )
        }
      >
        <Button variant={mode === 'replace' ? 'danger' : 'primary'} onClick={start} disabled={starting || !session || session.readOnly || files.length === 0 || invalid}>
          {starting ? <Spinner size={13} /> : <Upload size={14} />}
          Import {files.length > 0 ? plural(files.length, 'file') : ''}
        </Button>
      </Footer>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Progress

function useNow(active: boolean) {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!active) return
    const t = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(t)
  }, [active])
  return now
}

type Filter = 'all' | 'running' | 'done' | 'problems'

function StatusIcon({ it }: { it: TransferItem }) {
  switch (it.state) {
    case 'running':
      return it.retry ? <RefreshCw size={14} className="animate-spin text-warn" /> : <Spinner size={14} className="text-accent" />
    case 'done':
      return it.warning ? (
        <AlertTriangle size={14} className="text-warn" />
      ) : (
        <span className="flex size-3.5 items-center justify-center rounded-full bg-ok text-white">
          <Check size={9} strokeWidth={3.5} />
        </span>
      )
    case 'failed':
      return <XCircle size={14} className="text-danger" />
    case 'cancelled':
      return <Ban size={14} className="text-faint" />
    default:
      return <Circle size={14} className="text-line-strong" />
  }
}

function Stat({ label, value, sub }: { label: string; value: ReactNode; sub?: ReactNode }) {
  return (
    <div className="flex min-w-0 flex-col gap-0.5">
      <span className="text-[11px] text-faint">{label}</span>
      <span className="truncate text-[15px] font-semibold tabular-nums">{value}</span>
      {sub && <span className="truncate text-[11px] text-muted">{sub}</span>}
    </div>
  )
}

function joinPath(dir: string, file: string) {
  const sep = dir.includes('\\') && !dir.includes('/') ? '\\' : '/'
  return dir.endsWith(sep) ? dir + file : dir + sep + file
}

function JobView({ tab, jobId }: { tab: Tab; jobId: string }) {
  const job = useStore((s) => s.docJobs[jobId])
  const running = job?.state === 'running'
  const now = useNow(running)
  const [filter, setFilter] = useState<Filter>('all')
  const [busy, setBusy] = useState(false)

  if (!job) {
    return (
      <div className="flex h-full flex-col">
        <Header tab={tab} mode={tab.docTransfer!.mode} />
        <div className="flex flex-1 flex-col items-center justify-center gap-2 text-center text-[13px] text-muted">
          <Info size={20} className="text-faint" />
          This transfer is no longer available.
        </div>
      </div>
    )
  }

  const mode = job.kind as TransferMode
  const items = job.items ?? []
  const elapsed = running ? job.elapsedMs + (now - job.receivedAt) : job.elapsedMs
  const fraction = jobFraction(job)
  const t = totals(job)
  const docsPerSec = rate(t.docs, elapsed)
  // Snapshots only arrive when something changes, so their age tells how long nothing has.
  const stalledMs = running ? now - job.receivedAt : 0
  const stalled = stalledMs >= STALL_MS && items.some((i) => i.state === 'running')
  const left = running && !stalled ? eta(fraction, elapsed) : null
  const problems = t.failedItems + t.cancelledItems + items.filter((i) => i.state === 'done' && i.warning).length
  const counts: Record<Filter, number> = {
    all: items.length,
    running: items.filter((i) => i.state === 'running' || i.state === 'pending').length,
    done: items.filter((i) => i.state === 'done').length,
    problems,
  }
  const shown = items
    .map((it, i) => ({ it, i }))
    .filter(({ it }) => {
      if (filter === 'running') return it.state === 'running' || it.state === 'pending'
      if (filter === 'done') return it.state === 'done'
      if (filter === 'problems') return it.state === 'failed' || it.state === 'cancelled' || (it.state === 'done' && !!it.warning)
      return true
    })
  const tone = job.state === 'failed' ? 'danger' : job.state === 'cancelled' ? 'muted' : 'accent'

  const act = async (fn: () => Promise<unknown>, what: string) => {
    setBusy(true)
    try {
      const res = await fn()
      if (res && typeof res === 'object' && 'id' in res) useStore.getState().setDocJob(res as TransferJob)
    } catch (err) {
      toast.error(what, { description: errorMessage(err) })
    } finally {
      setBusy(false)
    }
  }

  const reveal = (path: string) => act(() => Transfers.Reveal(path), 'Could not open the folder')
  const again = () =>
    useStore.getState().openDocTransferTab(mode, job.sessionId, job.database, { collections: mode === 'export' ? items.map((i) => i.collection) : undefined })

  return (
    <div className="flex h-full min-h-0 flex-col">
      <Header tab={tab} mode={mode}>
        {running ? (
          <Button size="sm" onClick={() => act(() => Transfers.Cancel(job.id), 'Could not cancel')} disabled={busy}>
            <Square size={11} fill="currentColor" /> Cancel
          </Button>
        ) : (
          <>
            {t.failedItems + t.cancelledItems > 0 && (
              <Button size="sm" onClick={() => act(() => Transfers.Retry(job.id), 'Could not retry')} disabled={busy || !useStore.getState().sessions[job.sessionId]}>
                <RotateCcw size={12} /> Retry {t.failedItems + t.cancelledItems === 1 ? 'failed' : `${t.failedItems + t.cancelledItems} unfinished`}
              </Button>
            )}
            {mode === 'export' && (
              <Button size="sm" onClick={() => reveal(job.dir)}>
                <FolderOpen size={12} /> {REVEAL}
              </Button>
            )}
            <Button size="sm" variant="ghost" onClick={again}>
              {mode === 'export' ? <Download size={12} /> : <Upload size={12} />} New {mode}
            </Button>
          </>
        )}
      </Header>

      <div className="@container min-h-0 flex-1 overflow-auto">
        <div className="mx-auto flex max-w-4xl flex-col gap-5 px-6 py-5">
          <div className="rounded-xl border border-line bg-panel p-5 shadow-xs">
            <div className="flex items-start gap-3">
              <span
                className={cn(
                  'flex size-10 shrink-0 items-center justify-center rounded-full',
                  job.state === 'running' && 'bg-accent-soft text-accent',
                  job.state === 'done' && 'bg-accent-soft text-ok',
                  job.state === 'failed' && 'bg-danger-soft text-danger',
                  job.state === 'cancelled' && 'bg-sunken text-faint',
                )}
              >
                {job.state === 'running' ? (
                  mode === 'export' ? <Download size={18} className="animate-pulse-soft" /> : <Upload size={18} className="animate-pulse-soft" />
                ) : job.state === 'done' ? (
                  <Check size={20} strokeWidth={2.5} />
                ) : job.state === 'failed' ? (
                  <AlertTriangle size={18} />
                ) : (
                  <Ban size={18} />
                )}
              </span>
              <div className="min-w-0 flex-1">
                <h2 className="text-[15px] font-semibold">{jobTitle(job)}</h2>
                <p className="mt-0.5 text-xs text-muted">
                  {mode === 'export' ? (
                    <>
                      from <b className="font-medium text-fg">{job.database}</b> as {FORMATS.find((f) => f.value === job.format)?.label} to
                      <button className="mt-0.5 block max-w-full truncate text-left font-mono hover:text-accent hover:underline" onClick={() => reveal(job.dir)} title={job.dir}>
                        {prettyPath(job.dir)}
                      </button>
                    </>
                  ) : (
                    <>
                      into <b className="font-medium text-fg">{job.database}</b> · {IMPORT_MODES.find((m) => m.value === job.mode)?.label}
                    </>
                  )}
                </p>
              </div>
              <span className="text-2xl font-semibold text-fg tabular-nums">{Math.floor(fraction * 100)}%</span>
            </div>
            <ProgressBar value={fraction} tone={tone} className="mt-4 h-2" />
            <div className="mt-4 grid grid-cols-2 gap-4 @xl:grid-cols-5">
              <Stat label="Collections" value={`${t.done} / ${items.length}`} sub={t.failedItems ? <span className="text-danger">{t.failedItems} failed</span> : counts.running && running ? `${counts.running} to go` : undefined} />
              <Stat
                label="Documents"
                value={t.total > 0 && mode === 'export' ? `${compact(t.docs)} / ${compact(t.total)}` : compact(t.docs)}
                sub={
                  t.skipped || t.failed ? (
                    <>
                      {t.skipped > 0 && `${compact(t.skipped)} skipped`}
                      {t.skipped > 0 && t.failed > 0 && ' · '}
                      {t.failed > 0 && <span className="text-danger">{compact(t.failed)} rejected</span>}
                    </>
                  ) : undefined
                }
              />
              <Stat label={mode === 'export' ? 'Written' : 'Read'} value={formatBytes(t.bytes)} />
              <Stat label="Speed" value={docsPerSec ? `${compact(docsPerSec)}/s` : '—'} sub={docsPerSec && elapsed ? `${formatBytes((t.bytes * 1000) / elapsed)}/s` : undefined} />
              <Stat
                label="Time"
                value={clock(elapsed)}
                sub={
                  stalled ? (
                    <span className="text-warn">waiting for the server…</span>
                  ) : left !== null ? (
                    `about ${clock(left)} left`
                  ) : job.startedAt ? (
                    `started ${new Date(job.startedAt).toLocaleTimeString()}`
                  ) : undefined
                }
              />
            </div>
          </div>

          <div className="flex flex-col gap-2">
            <div className="flex items-center gap-1">
              {(
                [
                  ['all', 'All'],
                  ['running', running ? 'In progress' : 'Not started'],
                  ['done', 'Done'],
                  ['problems', 'Problems'],
                ] as [Filter, string][]
              )
                .filter(([f]) => f === 'all' || counts[f] > 0 || filter === f)
                .map(([f, label]) => (
                  <button
                    key={f}
                    onClick={() => setFilter(f)}
                    className={cn(
                      'flex h-6.5 items-center gap-1.5 rounded-md px-2 text-xs transition-colors',
                      filter === f ? 'bg-active font-medium text-fg' : 'text-muted hover:bg-hover hover:text-fg',
                    )}
                  >
                    {label}
                    <span className={cn('tabular-nums', f === 'problems' ? 'text-danger' : 'text-faint')}>{counts[f]}</span>
                  </button>
                ))}
            </div>

            <div className="overflow-hidden rounded-lg border border-line">
              {shown.map(({ it, i }) => (
                <ItemRow key={i} it={it} job={job} now={now} receivedAt={job.receivedAt} stalledMs={stalled ? stalledMs : 0} onReveal={reveal} />
              ))}
              {shown.length === 0 && <div className="px-3 py-6 text-center text-xs text-faint">Nothing here.</div>}
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}

function ItemRow({
  it,
  job,
  now,
  receivedAt,
  stalledMs,
  onReveal,
}: {
  it: TransferItem
  job: TransferJob
  now: number
  receivedAt: number
  stalledMs: number
  onReveal: (p: string) => void
}) {
  const waiting = it.state === 'running' && stalledMs > 0 && !it.retry
  const f = itemFraction(it)
  const elapsed = it.state === 'running' ? it.elapsedMs + Math.max(0, now - receivedAt) : it.elapsedMs
  const mode = job.kind as TransferMode
  const docs = it.total > 0 && it.state !== 'done' ? `${compact(it.docs)} / ${compact(it.total)}` : compact(it.docs)
  return (
    <div className={cn('group border-b border-line/60 px-3 py-2 last:border-b-0', it.state === 'running' && 'bg-accent-soft/20')}>
      <div className="flex items-center gap-3">
        <span className="flex w-4 shrink-0 justify-center">
          <StatusIcon it={it} />
        </span>
        <span className="flex min-w-0 flex-1 flex-col">
          <span className={cn('truncate text-[13px]', it.state === 'pending' && 'text-muted')}>{it.collection}</span>
          <span className="truncate font-mono text-[11px] text-faint">{it.file}</span>
        </span>
        <span className="hidden w-36 @lg:block">
          {it.state === 'running' || it.state === 'done' ? (
            <ProgressBar value={f} tone={it.retry || waiting ? 'warn' : 'accent'} />
          ) : it.state === 'failed' ? (
            <ProgressBar value={f ?? 0} tone="danger" />
          ) : null}
        </span>
        <span className="w-28 text-right text-xs text-muted tabular-nums">{it.state === 'pending' ? '' : `${docs} docs`}</span>
        <span className="hidden w-18 text-right text-xs text-muted tabular-nums @xl:block">{it.bytes > 0 ? formatBytes(it.bytes) : ''}</span>
        <span className="w-12 text-right text-xs text-faint tabular-nums">{it.state === 'pending' ? '' : clock(elapsed)}</span>
        <span className="flex w-7 justify-end">
          {it.state === 'done' &&
            (mode === 'export' ? (
              <IconButton label={REVEAL} className="opacity-0 group-hover:opacity-100 focus-visible:opacity-100" onClick={() => onReveal(joinPath(job.dir, it.file))}>
                <FolderOpen size={14} />
              </IconButton>
            ) : (
              <IconButton label="Open collection" className="opacity-0 group-hover:opacity-100 focus-visible:opacity-100" onClick={() => openCollection(job.sessionId, job.database, it.collection)}>
                <Table2 size={14} />
              </IconButton>
            ))}
        </span>
      </div>
      {(waiting || it.retry || it.error || it.warning || it.info || it.skipped > 0) && (
        <div className="mt-1 flex flex-col gap-0.5 pl-7 text-[11px]">
          {waiting && (
            <span className="text-warn">
              No response from the server for {Math.round(stalledMs / 1000)}s. Waiting; the transfer resumes on its own when the connection comes back.
            </span>
          )}
          {it.retry && <span className="text-warn">{it.retry}</span>}
          {it.error && <span className="selectable break-words text-danger">{it.error}</span>}
          {it.warning && <span className="selectable break-words text-warn">{it.warning}</span>}
          {it.skipped > 0 && <span className="text-muted">{plural(it.skipped, 'document')} already existed and were skipped.</span>}
          {it.info && <span className="text-faint">{it.info}</span>}
        </div>
      )}
    </div>
  )
}
