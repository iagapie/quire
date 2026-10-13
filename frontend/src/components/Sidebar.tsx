import { useEffect, useMemo, useState, type ReactNode } from 'react'
import {
  Braces,
  Columns3,
  Copy,
  Database,
  Eye,
  FileCode2,
  FolderClosed,
  FunctionSquare,
  Hash,
  KeyRound,
  Layers,
  Package,
  Link2,
  ListTree,
  Pencil,
  Plug,
  Monitor,
  Moon,
  PanelLeftClose,
  Plus,
  Sun,
  RefreshCw,
  Search,
  Server,
  ShieldCheck,
  SquareTerminal,
  Table2,
  Trash2,
  Unplug,
  Workflow,
  X,
  Zap,
  Download,
  Upload,
} from 'lucide-react'
import { DndContext, PointerSensor, closestCenter, useSensor, useSensors, type DragEndEvent } from '@dnd-kit/core'
import { SortableContext, useSortable, verticalListSortingStrategy } from '@dnd-kit/sortable'
import { restrictToParentElement, restrictToVerticalAxis } from '@dnd-kit/modifiers'
import { CSS } from '@dnd-kit/utilities'
import { toast } from 'sonner'
import { Connections, errorMessage, type DbObject, type DriverInfo, type Profile } from '../lib/api'
import { keys, useStore } from '../lib/store'
import { generateSQL, notifyError, openDDL, openEditor, openExport, openImport, openIndex, openTable, toggleSidebar, useTrafficLights, useUI } from '../lib/ui'
import { jobFraction, jobTitle, type TransferMode } from '../lib/transfer'
import { ProgressRing } from './TransferTab'
import { jobFraction as docJobFraction, jobTitle as docJobTitle } from '../lib/shell/transfer'
import { ShellDatabaseNode } from './shell/ShellTree'
import { DriverIcon } from './DriverIcon'
import { cn, copyText, isMac, mod } from '../lib/utils'
import { qualifiedName } from '../lib/sql'
import { useTheme, type ThemePref } from '../lib/theme'
import { IconButton, Kbd, Spinner, Tip, type MenuEntry } from './ui'
import { Message, Row, markedIn, same, useSel } from './tree'
import { confirmAction } from './Confirm'

/** Tables of a schema in tree order. */
function tableOrder(sessionId: string, db: string, schema: string): string[] {
  return (useStore.getState().objects[keys.schema(sessionId, db, schema)]?.items ?? []).filter((o) => o.kind === 'table').map((o) => o.name)
}

function pickTable(sessionId: string, db: string, schema: string, table: string, k: string, e: React.MouseEvent) {
  const { marked, anchor, mark } = useSel.getState()
  const cur = same(marked, sessionId, db, schema) ? marked!.tables : null
  if (e.shiftKey && cur && anchor) {
    const order = tableOrder(sessionId, db, schema)
    const a = order.indexOf(anchor)
    const b = order.indexOf(table)
    if (a >= 0 && b >= 0) {
      mark({ sessionId, db, schema, tables: order.slice(Math.min(a, b), Math.max(a, b) + 1) }, anchor, k)
      return
    }
  }
  if (e.metaKey || e.ctrlKey) {
    const list = cur ?? []
    if (list.includes(table)) mark({ sessionId, db, schema, tables: list.filter((c) => c !== table) }, table, '')
    else mark({ sessionId, db, schema, tables: [...list, table] }, table, k)
    return
  }
  mark({ sessionId, db, schema, tables: [table] }, table, k)
}

async function markAll(sessionId: string, db: string, schema: string) {
  const s = useStore.getState()
  if (!s.objects[keys.schema(sessionId, db, schema)]?.items) await s.loadObjects(sessionId, db, schema)
  const tables = tableOrder(sessionId, db, schema)
  if (tables.length) useSel.getState().mark({ sessionId, db, schema, tables }, tables[0])
}

function HealthDot({ health, connecting }: { health?: string; connecting?: boolean }) {
  if (connecting) return <Spinner size={11} className="text-muted" />
  if (!health) return null
  return (
    <span
      title={health === 'online' ? 'Connected' : health === 'reconnecting' ? 'Reconnecting…' : health}
      className={cn(
        'size-1.5 shrink-0 rounded-full',
        health === 'online' && 'bg-ok',
        health === 'reconnecting' && 'animate-pulse-soft bg-warn',
        health === 'closed' && 'bg-faint',
      )}
    />
  )
}

export function Sidebar() {
  const profiles = useStore((s) => s.profiles)
  const [filter, setFilter] = useState('')
  const openDialog = useUI((s) => s.openConnectionDialog)
  const trafficLights = useTrafficLights()
  // A few pixels of movement start a drag, so clicks and double-clicks still work.
  const sensors = useSensors(useSensor(PointerSensor, { activationConstraint: { distance: 6 } }))
  const onDragEnd = ({ active, over }: DragEndEvent) => {
    if (over && active.id !== over.id) void useStore.getState().moveProfile(String(active.id), String(over.id))
  }

  return (
    <div className="flex h-full flex-col bg-panel">
      <div className={cn('drag flex h-[38px] shrink-0 items-center justify-end gap-0.5 pr-2', trafficLights ? 'pl-21' : 'pl-2')}>
        <IconButton label={<span>Hide sidebar <Kbd>{mod}B</Kbd></span>} className="no-drag" onClick={toggleSidebar}>
          <PanelLeftClose size={16} />
        </IconButton>
        <IconButton label="New connection" className="no-drag" onClick={() => openDialog()}>
          <Plus size={16} />
        </IconButton>
      </div>
      <div className="px-2 py-2">
        <div className="relative">
          <Search size={13} className="pointer-events-none absolute top-1/2 left-2 -translate-y-1/2 text-faint" />
          <input
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            onKeyDown={(e) => e.key === 'Escape' && setFilter('')}
            placeholder="Filter databases & tables"
            spellCheck={false}
            className="h-7 w-full rounded-md border border-transparent bg-sunken pr-7 pl-7 text-xs text-fg placeholder:text-faint focus:border-accent focus:outline-none"
          />
          {filter && (
            <button className="absolute top-1/2 right-1.5 -translate-y-1/2 text-faint hover:text-fg" onClick={() => setFilter('')} aria-label="Clear filter">
              <X size={13} />
            </button>
          )}
        </div>
      </div>
      <div role="tree" className="min-h-0 flex-1 overflow-auto px-1.5 pb-3">
        {profiles.length === 0 && (
          <div className="flex flex-col items-center gap-3 px-4 pt-10 text-center">
            <Server size={28} className="text-faint" />
            <p className="text-xs text-muted">No saved connections yet.</p>
            <button className="text-xs font-medium text-accent hover:underline" onClick={() => openDialog()}>
              Add a connection
            </button>
          </div>
        )}
        <DndContext sensors={sensors} collisionDetection={closestCenter} modifiers={[restrictToVerticalAxis, restrictToParentElement]} onDragEnd={onDragEnd}>
          <SortableContext items={profiles.map((p) => p.id)} strategy={verticalListSortingStrategy}>
            {profiles.map((p) => (
              <ConnectionNode key={p.id} profile={p} filter={filter.trim().toLowerCase()} />
            ))}
          </SortableContext>
        </DndContext>
      </div>
      <Activity />
      <div className="flex h-10 shrink-0 items-center justify-between border-t border-line px-2.5">
        <span className="text-[11px] text-faint">Appearance</span>
        <ThemeSwitch />
      </div>
    </div>
  )
}

/** Running exports and imports; a click opens the job's tab. */
function Activity() {
  const jobs = useStore((s) => s.jobs)
  const docJobs = useStore((s) => s.docJobs)
  const running = Object.values(jobs).filter((j) => j.state === 'running')
  const docRunning = Object.values(docJobs).filter((j) => j.state === 'running')
  if (running.length === 0 && docRunning.length === 0) return null
  return (
    <div className="flex shrink-0 flex-col gap-0.5 border-t border-line p-1.5">
      {docRunning.map((j) => {
        const f = docJobFraction(j)
        return (
          <button
            key={j.id}
            onClick={() => useStore.getState().openDocTransferTab(j.kind as TransferMode, j.sessionId, j.database, { jobId: j.id })}
            className="flex h-8 items-center gap-2 rounded-md px-2 text-left text-xs hover:bg-hover"
          >
            <ProgressRing value={f} size={16} className="text-accent" />
            <span className="min-w-0 flex-1 truncate">
              {docJobTitle(j)}
              <span className="text-faint"> · {j.database}</span>
            </span>
            <span className="text-faint tabular-nums">{Math.floor(f * 100)}%</span>
          </button>
        )
      })}
      {running.map((j) => {
        const f = jobFraction(j)
        return (
          <button
            key={j.id}
            onClick={() => useStore.getState().openTransferTab(j.kind as TransferMode, j.sessionId, j.database, j.schema, { jobId: j.id })}
            className="flex h-8 items-center gap-2 rounded-md px-2 text-left text-xs hover:bg-hover"
          >
            <ProgressRing value={f} size={16} className="text-accent" />
            <span className="min-w-0 flex-1 truncate">
              {jobTitle(j)}
              <span className="text-faint"> · {j.schema || j.database}</span>
            </span>
            <span className="text-faint tabular-nums">{Math.floor(f * 100)}%</span>
          </button>
        )
      })}
    </div>
  )
}

const THEMES: { value: ThemePref; label: string; icon: ReactNode }[] = [
  { value: 'system', label: 'System', icon: <Monitor size={13} /> },
  { value: 'light', label: 'Light', icon: <Sun size={13} /> },
  { value: 'dark', label: 'Dark', icon: <Moon size={13} /> },
]

function ThemeSwitch() {
  const pref = useTheme((s) => s.pref)
  const setPref = useTheme((s) => s.setPref)
  return (
    <div role="radiogroup" aria-label="Appearance" className="flex h-6.5 items-center rounded-md border border-line bg-sunken p-0.5">
      {THEMES.map((t) => (
        <Tip key={t.value} label={t.label} side="top">
          <button
            role="radio"
            aria-checked={pref === t.value}
            aria-label={t.label}
            onClick={() => setPref(t.value)}
            className={cn(
              'flex h-full w-7 items-center justify-center rounded transition-colors',
              pref === t.value ? 'bg-raised text-fg shadow-xs' : 'text-faint hover:text-fg',
            )}
          >
            {t.icon}
          </button>
        </Tip>
      ))}
    </div>
  )
}

function ConnectionNode({ profile, filter }: { profile: Profile; filter: string }) {
  const id = profile.id
  const k = `c:${id}`
  const info = useStore((s) => s.drivers[profile.driver])
  const session = useStore((s) => s.sessions[id])
  const connecting = useStore((s) => !!s.connecting[id])
  const node = useStore((s) => s.databases[id])
  const open = useStore((s) => !!s.expanded[k])
  const { toggle, connect, disconnect, loadDatabases, loadProfiles, invalidate } = useStore.getState()
  const openDialog = useUI((s) => s.openConnectionDialog)
  // The connection row is the drag handle; the whole group (with its databases) moves.
  const { listeners, setNodeRef, transform, transition, isDragging } = useSortable({ id })

  // Connections open lazily, on expand only.
  const onToggle = async () => {
    if (!open && !session) {
      toggle(k, true)
      await connect(id)
      return
    }
    toggle(k)
  }

  const menu: MenuEntry[] = [
    { label: session ? 'Disconnect' : 'Connect', icon: session ? <Unplug size={14} /> : <Plug size={14} />, onSelect: () => (session ? disconnect(id) : connect(id).then(() => toggle(k, true))) },
    { label: info?.shell ? 'Open shell' : 'Open SQL editor', icon: <SquareTerminal size={14} />, onSelect: () => openEditor(id) },
    {
      label: 'Refresh',
      icon: <RefreshCw size={14} />,
      disabled: !session,
      onSelect: () => {
        invalidate(id)
        void loadDatabases(id)
      },
    },
    { separator: true },
    { label: 'Edit connection…', icon: <Pencil size={14} />, onSelect: () => openDialog(id) },
    {
      label: 'Duplicate',
      icon: <Copy size={14} />,
      onSelect: async () => {
        try {
          await Connections.DuplicateProfile(id)
          await loadProfiles()
        } catch (err) {
          notifyError('Could not duplicate', err)
        }
      },
    },
    { separator: true },
    {
      label: 'Delete connection',
      icon: <Trash2 size={14} />,
      danger: true,
      onSelect: async () => {
        const ok = await confirmAction({
          title: 'Delete connection?',
          message: (
            <>
              <b className="text-fg">{profile.name}</b> and its stored credentials will be removed. Open tabs keep their scripts.
            </>
          ),
          confirmLabel: 'Delete',
          danger: true,
        })
        if (!ok) return
        await Connections.DeleteProfile(id)
        await disconnect(id).catch(() => {})
        await loadProfiles()
      },
    },
  ]

  const dbs = node?.items ?? []
  const host = profile.driver === 'sqlite' ? profile.database || ':memory:' : profile.host || (profile.hasUri ? 'connection string' : 'localhost')

  return (
    <div
      role="group"
      ref={setNodeRef}
      style={{ transform: CSS.Translate.toString(transform), transition }}
      className={cn('relative', isDragging && 'z-10 rounded-md bg-raised opacity-90 shadow-lg')}
    >
      <div {...listeners} className={cn(isDragging && 'cursor-grabbing')}>
        <Row
          depth={0}
          expandable
          open={open}
          onToggle={onToggle}
          k={k}
          icon={
            <span className="flex items-center gap-1.5">
              <span className="h-3.5 w-[3px] rounded-full" style={{ background: profile.color || 'transparent' }} />
              <DriverIcon driver={profile.driver} className={cn(!session && 'opacity-60')} />
            </span>
          }
          label={<span className={cn('font-medium', !session && 'text-muted')}>{profile.name}</span>}
          title={`${info?.label ?? profile.driver} · ${host}`}
          trailing={
            <span className="flex items-center gap-1.5">
              {profile.readOnly && <span className="rounded bg-sunken px-1 text-[10px] text-faint">RO</span>}
              {profile.ssh.enabled && <KeyRound size={11} className="text-faint" aria-label="SSH tunnel" />}
              <HealthDot health={session?.health} connecting={connecting} />
            </span>
          }
          menu={menu}
        />
      </div>
      {open && (
        <>
          {!session && !connecting && !node?.error && (
            <Message depth={1} onRetry={() => connect(id)} action="Connect">
              Not connected
            </Message>
          )}
          {node?.loading && <Message depth={1}>Loading…</Message>}
          {node?.error && (
            <Message depth={1} tone="error" onRetry={() => (session ? loadDatabases(id) : connect(id))}>
              {node.error}
            </Message>
          )}
          {session && info && !info.databases && <SchemaList sessionId={id} db="" info={info} depth={1} filter={filter} />}
          {session && info?.databases && !node?.loading && !node?.error && dbs.length === 0 && (
            <Message depth={1}>No databases visible to this user.{info.shell && ' Set a default database in the connection settings.'}</Message>
          )}
          {session && info?.shell && dbs.map((db) => <ShellDatabaseNode key={db} sessionId={id} db={db} filter={filter} />)}
          {session && info?.databases && !info.shell && dbs.map((db) => <DatabaseNode key={db} sessionId={id} db={db} info={info} filter={filter} />)}
        </>
      )}
    </div>
  )
}

function DatabaseNode({ sessionId, db, info, filter }: { sessionId: string; db: string; info: DriverInfo; filter: string }) {
  const readOnly = useStore((s) => s.sessions[sessionId]?.readOnly)
  const k = `d:${sessionId}:${db}`
  const open = useStore((s) => !!s.expanded[k]) || !!filter
  const current = useStore((s) => s.sessions[sessionId]?.database === db)
  const { toggle, loadDatabases, invalidate } = useStore.getState()
  const objects = useStore((s) => s.objects[keys.schema(sessionId, db, '')])
  if (filter && !db.toLowerCase().includes(filter) && !info.schemas && !(objects?.items ?? []).some((o) => o.name.toLowerCase().includes(filter))) {
    return null
  }
  return (
    <div role="group">
      <Row
        depth={1}
        expandable
        open={open}
        onToggle={() => toggle(k, !open)}
        k={k}
        icon={<Database size={14} className={cn(current && 'text-accent')} />}
        label={db}
        onSelectAll={info.schemas ? undefined : () => markAll(sessionId, db, '')}
        menu={[
          { label: 'Open SQL editor', icon: <SquareTerminal size={14} />, onSelect: () => openEditor(sessionId, db, info.schemas ? undefined : '') },
          {
            label: 'Refresh',
            icon: <RefreshCw size={14} />,
            onSelect: () => {
              invalidate(sessionId, db)
              void loadDatabases(sessionId)
            },
          },
          { separator: true, hidden: info.schemas },
          {
            label: info.document ? 'Export containers…' : 'Export tables…',
            icon: <Download size={14} />,
            hidden: info.schemas,
            onSelect: () => openExport(sessionId, db, '', markedIn(useSel.getState().marked, sessionId, db, '') ?? undefined),
          },
          { label: 'Import…', icon: <Upload size={14} />, hidden: info.schemas || readOnly || info.queryOnly, onSelect: () => openImport(sessionId, db, '') },
          { separator: true },
          { label: 'Copy name', icon: <Copy size={14} />, onSelect: () => copyText(db) },
        ]}
      />
      {open && (info.schemas ? <SchemaList sessionId={sessionId} db={db} info={info} depth={2} filter={filter} /> : <Folders sessionId={sessionId} db={db} schema="" info={info} depth={2} filter={filter} />)}
    </div>
  )
}

function SchemaList({ sessionId, db, info, depth, filter }: { sessionId: string; db: string; info: DriverInfo; depth: number; filter: string }) {
  const node = useStore((s) => s.schemas[keys.db(sessionId, db)])
  const loadSchemas = useStore((s) => s.loadSchemas)
  useEffect(() => {
    if (!node) void loadSchemas(sessionId, db)
  }, [node, sessionId, db, loadSchemas])
  return (
    <>
      {node?.loading && <Message depth={depth}>Loading {info.schemaLabel.toLowerCase()}s…</Message>}
      {node?.error && (
        <Message depth={depth} tone="error" onRetry={() => loadSchemas(sessionId, db)}>
          {node.error}
        </Message>
      )}
      {node?.items?.map((schema) => <SchemaNode key={schema} sessionId={sessionId} db={db} schema={schema} info={info} depth={depth} filter={filter} />)}
    </>
  )
}

function SchemaNode({ sessionId, db, schema, info, depth, filter }: { sessionId: string; db: string; schema: string; info: DriverInfo; depth: number; filter: string }) {
  const readOnly = useStore((s) => s.sessions[sessionId]?.readOnly)
  const k = `s:${sessionId}:${db}:${schema}`
  const objects = useStore((s) => s.objects[keys.schema(sessionId, db, schema)])
  const stored = useStore((s) => s.expanded[k])
  const current = useStore((s) => s.sessions[sessionId]?.schema === schema)
  const open = !!stored || (!!filter && !!objects?.items)
  const { toggle, loadObjects, invalidate } = useStore.getState()
  if (filter && !schema.toLowerCase().includes(filter) && !(objects?.items ?? []).some((o) => o.name.toLowerCase().includes(filter))) return null
  const system = ['pg_catalog', 'information_schema', 'sys', 'INFORMATION_SCHEMA'].includes(schema)
  return (
    <div role="group">
      <Row
        depth={depth}
        expandable
        open={open}
        onToggle={() => toggle(k, !open)}
        k={k}
        icon={<Layers size={14} className={cn(current && 'text-accent')} />}
        label={schema}
        muted={system}
        onSelectAll={() => markAll(sessionId, db, schema)}
        menu={[
          { label: 'Open SQL editor', icon: <SquareTerminal size={14} />, onSelect: () => openEditor(sessionId, db || undefined, schema) },
          {
            label: 'Refresh',
            icon: <RefreshCw size={14} />,
            onSelect: () => {
              invalidate(sessionId, db, schema)
              void loadObjects(sessionId, db, schema)
            },
          },
          { separator: true },
          { label: 'Export tables…', icon: <Download size={14} />, onSelect: () => openExport(sessionId, db, schema, markedIn(useSel.getState().marked, sessionId, db, schema) ?? undefined) },
          { label: 'Import…', icon: <Upload size={14} />, hidden: readOnly, onSelect: () => openImport(sessionId, db, schema) },
          { separator: true },
          { label: 'Copy name', icon: <Copy size={14} />, onSelect: () => copyText(schema) },
        ]}
      />
      {open && <Folders sessionId={sessionId} db={db} schema={schema} info={info} depth={depth + 1} filter={filter} />}
    </div>
  )
}

const FOLDER_LABELS: Record<string, string> = {
  table: 'Tables',
  view: 'Views',
  matview: 'Materialized views',
  function: 'Functions',
  procedure: 'Procedures',
  sequence: 'Sequences',
  trigger: 'Triggers',
  package: 'Packages',
  container: 'Containers',
}

function objectIcon(kind: string, size = 14) {
  switch (kind) {
    case 'view':
      return <Eye size={size} />
    case 'matview':
      return <Layers size={size} />
    case 'function':
      return <FunctionSquare size={size} />
    case 'procedure':
      return <Workflow size={size} />
    case 'sequence':
      return <Hash size={size} />
    case 'trigger':
      return <Zap size={size} />
    case 'container':
      return <Braces size={size} />
    case 'package':
      return <Package size={size} />
  }
  return <Table2 size={size} />
}

function Folders({ sessionId, db, schema, info, depth, filter }: { sessionId: string; db: string; schema: string; info: DriverInfo; depth: number; filter: string }) {
  const node = useStore((s) => s.objects[keys.schema(sessionId, db, schema)])
  const loadObjects = useStore((s) => s.loadObjects)
  useEffect(() => {
    if (!node) void loadObjects(sessionId, db, schema)
  }, [node, sessionId, db, schema, loadObjects])
  const items = useMemo(() => {
    const all = node?.items ?? []
    return filter ? all.filter((o) => o.name.toLowerCase().includes(filter)) : all
  }, [node, filter])
  if (node?.loading) return <Message depth={depth}>Loading…</Message>
  if (node?.error) {
    return (
      <Message depth={depth} tone="error" onRetry={() => loadObjects(sessionId, db, schema)}>
        {node.error}
      </Message>
    )
  }
  if (!node?.items) return null
  return (
    <>
      {(info.folders ?? []).map((kind) => {
        const list = items.filter((o) => o.kind === kind)
        if (list.length === 0 && (kind !== 'table' || filter)) return null
        return <Folder key={kind} sessionId={sessionId} db={db} schema={schema} info={info} kind={kind} items={list} depth={depth} filter={filter} />
      })}
    </>
  )
}

function Folder({ sessionId, db, schema, info, kind, items, depth, filter }: { sessionId: string; db: string; schema: string; info: DriverInfo; kind: string; items: DbObject[]; depth: number; filter: string }) {
  const k = `f:${sessionId}:${db}:${schema}:${kind}`
  const stored = useStore((s) => s.expanded[k])
  const open = (stored ?? (kind === 'table' || kind === 'container')) || !!filter
  const toggle = useStore((s) => s.toggle)
  return (
    <div role="group">
      <Row
        depth={depth}
        expandable
        open={open}
        onToggle={() => toggle(k, !open)}
        k={k}
        icon={<FolderClosed size={14} />}
        label={FOLDER_LABELS[kind] ?? kind}
        onSelectAll={kind === 'table' ? () => markAll(sessionId, db, schema) : undefined}
        muted
        trailing={<span className="text-[11px] text-faint tabular-nums">{items.length}</span>}
      />
      {open && items.map((o) => <ObjectNode key={`${o.name}:${o.id}`} sessionId={sessionId} db={db} schema={schema} info={info} obj={o} depth={depth + 1} />)}
    </div>
  )
}

function ObjectNode({ sessionId, db, schema, info, obj, depth }: { sessionId: string; db: string; schema: string; info: DriverInfo; obj: DbObject; depth: number }) {
  const k = `o:${sessionId}:${db}:${schema}:${obj.kind}:${obj.name}:${obj.id}`
  const isTable = obj.kind === 'table'
  const isContainer = obj.kind === 'container'
  const hasRows = isTable || isContainer || obj.kind === 'view' || obj.kind === 'matview'
  const open = useStore((s) => !!s.expanded[k])
  const readOnly = useStore((s) => s.sessions[sessionId]?.readOnly)
  const { toggle, loadObjects, invalidate } = useStore.getState()
  const multi = useSel((s) => markedIn(s.marked, sessionId, db, schema))
  const isMarked = isTable && !!multi?.includes(obj.name)
  const name = qualifiedName(info.editor, info.databases && !info.schemas ? db : '', schema, obj.name)

  const drop = async () => {
    let stmt = ''
    try {
      stmt = await Connections.DropStatement(sessionId, db, schema, obj)
    } catch (err) {
      return notifyError('Cannot drop this object', err)
    }
    const ok = await confirmAction({
      title: `Drop ${FOLDER_LABELS[obj.kind]?.replace(/s$/, '').toLowerCase() ?? obj.kind}?`,
      message: (
        <>
          This runs <code className="selectable rounded bg-sunken px-1 font-mono text-[12px] text-fg">{stmt}</code>. It cannot be undone.
        </>
      ),
      confirmLabel: 'Drop',
      danger: true,
    })
    if (!ok) return
    try {
      await Connections.DropObject(sessionId, db, schema, obj)
      toast.success(`${obj.name} dropped`)
      invalidate(sessionId, db, schema)
      void loadObjects(sessionId, db, schema)
    } catch (err) {
      toast.error('Drop failed', { description: errorMessage(err) })
    }
  }

  const multiMenu: MenuEntry[] = [
    { label: `Export ${multi?.length ?? 0} tables…`, icon: <Download size={14} />, onSelect: () => openExport(sessionId, db, schema, multi ?? [obj.name]) },
    { separator: true },
    { label: 'Copy names', icon: <Copy size={14} />, onSelect: () => copyText((multi ?? []).join('\n')) },
    { label: 'Clear selection', icon: <X size={14} />, onSelect: () => useSel.getState().mark(null) },
  ]
  const menu: MenuEntry[] = [
    { label: 'Open', icon: <Table2 size={14} />, shortcut: '↵', hidden: !hasRows, onSelect: () => openTable(sessionId, db, schema, obj.name) },
    { label: 'Open SQL editor', icon: <SquareTerminal size={14} />, onSelect: () => openEditor(sessionId, db || undefined, schema) },
    { label: isContainer ? 'Show properties' : 'Show DDL', icon: <FileCode2 size={14} />, shortcut: hasRows ? undefined : '↵', onSelect: () => openDDL(sessionId, db, schema, obj) },
    { separator: true, hidden: !hasRows },
    { label: 'Generate SELECT', icon: <SquareTerminal size={14} />, hidden: !hasRows, onSelect: () => generateSQL(sessionId, db, schema, obj.name, 'select') },
    { label: 'Generate INSERT', icon: <SquareTerminal size={14} />, hidden: !isTable, onSelect: () => generateSQL(sessionId, db, schema, obj.name, 'insert') },
    { label: 'Generate UPDATE', icon: <SquareTerminal size={14} />, hidden: !isTable, onSelect: () => generateSQL(sessionId, db, schema, obj.name, 'update') },
    { label: 'Generate DELETE', icon: <SquareTerminal size={14} />, hidden: !isTable, onSelect: () => generateSQL(sessionId, db, schema, obj.name, 'delete') },
    { separator: true, hidden: !isTable && !isContainer },
    { label: isContainer ? 'Export container…' : 'Export table…', icon: <Download size={14} />, hidden: !isTable && !isContainer, onSelect: () => openExport(sessionId, db, schema, [obj.name]) },
    { label: 'Import into table…', icon: <Upload size={14} />, hidden: !isTable || readOnly || info.queryOnly, onSelect: () => openImport(sessionId, db, schema, obj.name) },
    { label: 'New index…', icon: <KeyRound size={14} />, hidden: !isTable || readOnly || !info.index.editable, onSelect: () => openIndex(sessionId, db, schema, obj.name) },
    { separator: true },
    { label: 'Copy name', icon: <Copy size={14} />, onSelect: () => copyText(obj.name) },
    { label: 'Copy qualified name', icon: <Copy size={14} />, onSelect: () => copyText(name) },
    { label: 'Refresh', icon: <RefreshCw size={14} />, hidden: !hasRows, onSelect: () => useStore.getState().loadTable(sessionId, db, schema, obj.name, !isTable && !isContainer) },
    { separator: true, hidden: readOnly || info.queryOnly },
    { label: 'Drop…', icon: <Trash2 size={14} />, danger: true, hidden: readOnly || info.queryOnly, onSelect: drop },
  ]

  return (
    <div role="group">
      <Row
        depth={depth}
        expandable={hasRows}
        open={open}
        onToggle={() => toggle(k)}
        onOpen={() => (hasRows ? openTable(sessionId, db, schema, obj.name) : openDDL(sessionId, db, schema, obj))}
        k={k}
        icon={objectIcon(obj.kind)}
        label={
          <span>
            {obj.name}
            {obj.detail && <span className="ml-1.5 font-mono text-[11px] text-faint">{obj.kind === 'trigger' ? `on ${obj.detail}` : isContainer ? obj.detail : `(${obj.detail})`}</span>}
          </span>
        }
        title={`${name}${obj.comment ? ` — ${obj.comment}` : ''}${isTable ? ` · double-click to open, ${isMac ? '⌘' : 'Ctrl'}-click to select several` : ''}`}
        marked={isMarked}
        onPick={isTable ? (e) => pickTable(sessionId, db, schema, obj.name, k, e) : undefined}
        onSelectAll={isTable ? () => markAll(sessionId, db, schema) : undefined}
        onContext={() => {
          if (isTable && !isMarked) useSel.getState().mark({ sessionId, db, schema, tables: [obj.name] }, obj.name, k)
        }}
        menu={isMarked && multi ? multiMenu : menu}
      />
      {open && hasRows && <TableChildren sessionId={sessionId} db={db} schema={schema} table={obj.name} view={!isTable && !isContainer} fieldsLabel={isContainer ? 'Fields' : undefined} indexEditable={info.index.editable} depth={depth + 1} />}
    </div>
  )
}

function TableChildren({
  sessionId,
  db,
  schema,
  table,
  view,
  fieldsLabel,
  indexEditable,
  depth,
}: {
  sessionId: string
  db: string
  schema: string
  table: string
  view: boolean
  /** Names the column group (the inferred fields of a container). */
  fieldsLabel?: string
  /** Opens indexes in the index editor. */
  indexEditable?: boolean
  depth: number
}) {
  const node = useStore((s) => s.tables[keys.table(sessionId, db, schema, table)])
  const loadTable = useStore((s) => s.loadTable)
  useEffect(() => {
    if (!node) void loadTable(sessionId, db, schema, table, view)
  }, [node, sessionId, db, schema, table, view, loadTable])
  if (node?.loading) return <Message depth={depth}>Loading…</Message>
  if (node?.error) {
    return (
      <Message depth={depth} tone="error" onRetry={() => loadTable(sessionId, db, schema, table, view)}>
        {node.error}
      </Message>
    )
  }
  const d = node?.items
  if (!d) return null
  const base = `t:${sessionId}:${db}:${schema}:${table}`
  const editable = !!indexEditable
  return (
    <>
      <Group k={`${base}:cols`} depth={depth} icon={<Columns3 size={14} />} label={fieldsLabel ?? 'Columns'} count={d.columns?.length ?? 0} defaultOpen>
        {(d.columns ?? []).map((c) => (
          <Row
            key={c.name}
            depth={depth + 1}
            k={`${base}:col:${c.name}`}
            icon={c.pk ? <KeyRound size={13} className="text-warn" /> : <span className="size-3.5" />}
            label={
              <span>
                {c.name}
                <span className="ml-2 font-mono text-[11px] text-faint">
                  {c.type.toLowerCase()}
                  {!c.nullable && ' not null'}
                </span>
              </span>
            }
            title={`${c.name} ${c.type}${c.nullable ? '' : ' NOT NULL'}${c.default ? ` DEFAULT ${c.default}` : ''}${c.comment ? ` — ${c.comment}` : ''}`}
            menu={[{ label: 'Copy name', icon: <Copy size={14} />, onSelect: () => copyText(c.name) }]}
          />
        ))}
      </Group>
      {!view && (
        <Group k={`${base}:idx`} depth={depth} icon={<ListTree size={14} />} label="Indexes" count={d.indexes?.length ?? 0}>
          {(d.indexes ?? []).map((ix) => {
            const cols = (ix.columns ?? []).map((c) => (c.expression ? `(${c.expression})` : c.name) + (c.desc ? ' desc' : '')).join(', ')
            return (
              <Row
                key={ix.name}
                depth={depth + 1}
                k={`${base}:ix:${ix.name}`}
                icon={<KeyRound size={13} className={cn(ix.primary && 'text-warn')} />}
                label={
                  <span>
                    {ix.name}
                    <span className="ml-2 font-mono text-[11px] text-faint">{cols}</span>
                  </span>
                }
                title={ix.definition || `${ix.name} (${cols})`}
                trailing={ix.unique && !ix.primary ? <span className="rounded bg-sunken px-1 text-[10px] text-faint">unique</span> : undefined}
                onOpen={editable ? () => openIndex(sessionId, db, schema, table, ix.name) : undefined}
                menu={[
                  { label: 'Edit index…', icon: <KeyRound size={14} />, shortcut: '↵', hidden: !editable, onSelect: () => openIndex(sessionId, db, schema, table, ix.name) },
                  { separator: true, hidden: !editable },
                  { label: 'Copy name', icon: <Copy size={14} />, onSelect: () => copyText(ix.name) },
                  { label: 'Copy definition', icon: <Copy size={14} />, hidden: !ix.definition, onSelect: () => copyText(ix.definition ?? '') },
                ]}
              />
            )
          })}
        </Group>
      )}
      {!view && (
        <Group k={`${base}:con`} depth={depth} icon={<ShieldCheck size={14} />} label="Constraints" count={d.constraints?.length ?? 0}>
          {(d.constraints ?? []).map((c) => (
            <Row
              key={c.name}
              depth={depth + 1}
              k={`${base}:con:${c.name}`}
              icon={c.type === 'FOREIGN KEY' ? <Link2 size={13} /> : c.type === 'PRIMARY KEY' ? <KeyRound size={13} className="text-warn" /> : <ShieldCheck size={13} />}
              label={
                <span>
                  {c.name}
                  <span className="ml-2 font-mono text-[11px] text-faint">
                    {c.type.toLowerCase()} ({(c.columns ?? []).join(', ')}){c.refTable && ` → ${c.refTable}`}
                  </span>
                </span>
              }
              title={c.definition || c.type}
              menu={[
                { label: 'Copy name', icon: <Copy size={14} />, onSelect: () => copyText(c.name) },
                { label: 'Copy definition', icon: <Copy size={14} />, hidden: !c.definition, onSelect: () => copyText(c.definition ?? '') },
              ]}
            />
          ))}
        </Group>
      )}
    </>
  )
}

function Group({ k, depth, icon, label, count, defaultOpen, children }: { k: string; depth: number; icon: ReactNode; label: string; count: number; defaultOpen?: boolean; children: ReactNode }) {
  const stored = useStore((s) => s.expanded[k])
  const open = stored ?? !!defaultOpen
  const toggle = useStore((s) => s.toggle)
  return (
    <div role="group">
      <Row depth={depth} expandable={count > 0} open={open} onToggle={() => toggle(k, !open)} k={k} icon={icon} label={label} muted trailing={<span className="text-[11px] text-faint tabular-nums">{count}</span>} />
      {open && children}
    </div>
  )
}
