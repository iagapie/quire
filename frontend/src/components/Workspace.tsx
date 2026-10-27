import { useEffect } from 'react'
import { DndContext, KeyboardSensor, PointerSensor, closestCenter, useSensor, useSensors, type DragEndEvent } from '@dnd-kit/core'
import { SortableContext, horizontalListSortingStrategy, sortableKeyboardCoordinates, useSortable } from '@dnd-kit/sortable'
import { restrictToHorizontalAxis } from '@dnd-kit/modifiers'
import { CSS } from '@dnd-kit/utilities'
import { toast } from 'sonner'
import { Transfers, type Profile } from '../lib/api'
import { jobFraction } from '../lib/transfer'
import { Transfers as DocTransfers } from '../lib/shell/api'
import { jobFraction as docJobFraction } from '../lib/shell/transfer'
import { useScrollX } from '../lib/useScrollX'
import { Download, FileCode2, History, KeyRound, PanelLeftOpen, Plus, SquareTerminal, Star, Upload, X } from 'lucide-react'
import { isShellKind, useStore, type Tab } from '../lib/store'
import { openEditor, toggleSidebar, useLayout, useTrafficLights, useUI } from '../lib/ui'
import { cn, mod } from '../lib/utils'
import { confirmAction } from './Confirm'
import { openLibrary, reopen, saveFavorite } from './Library'
import { useLibrary } from '../lib/library'
import { ContextMenu, IconButton, Kbd } from './ui'
import { IndexTab } from './IndexTab'
import { SqlTab } from './SqlTab'
import { ProgressRing, TransferTab } from './TransferTab'
import { DriverIcon } from './DriverIcon'
import { ShellTab } from './shell/ShellTab'
import { ShellIndexTab } from './shell/ShellIndexTab'
import { ShellTransferTab } from './shell/ShellTransferTab'

function tabTitle(t: Tab) {
  return t.title || [t.database, t.schema].filter(Boolean).join('.') || (t.kind === 'shell' ? 'Shell' : 'SQL')
}

export async function closeWithConfirm(t: Tab) {
  if (t.tx?.open) {
    const ok = await confirmAction({
      title: 'Roll back the open transaction?',
      message: `${tabTitle(t)} has a transaction in progress${t.tx.statements ? ` (${t.tx.statements} statements)` : ''}. Closing the tab rolls it back.`,
      confirmLabel: 'Roll back and close',
      danger: true,
    })
    if (!ok) return
  } else if (t.dirty && t.script.trim() && t.filePath) {
    const ok = await confirmAction({
      title: 'Close without saving?',
      message: `Changes to ${tabTitle(t)} have not been saved to ${t.filePath}.`,
      confirmLabel: 'Close tab',
      danger: true,
    })
    if (!ok) return
  }
  const jobId = t.transfer?.jobId ?? t.docTransfer?.jobId
  if (jobId) {
    const s = useStore.getState()
    const doc = t.kind === 'doctransfer'
    const mode = t.transfer?.mode ?? t.docTransfer?.mode
    // A running export or import goes on in the background; the sidebar keeps track of it.
    if ((doc ? s.docJobs[jobId] : s.jobs[jobId])?.state === 'running') toast(`${mode === 'export' ? 'Export' : 'Import'} continues in the background`)
    else void (doc ? DocTransfers.Remove(jobId) : Transfers.Remove(jobId)).catch(() => {})
  }
  useStore.getState().closeTab(t.id)
}

export function Workspace() {
  const tabs = useStore((s) => s.tabs)
  const active = useStore((s) => s.activeTab)
  const profiles = useStore((s) => s.profiles)
  const setActive = useStore((s) => s.setActiveTab)
  const activeTab = tabs.find((t) => t.id === active) ?? tabs[0]
  const strip = useScrollX<HTMLDivElement>(activeTab?.id)
  const sidebarCollapsed = useLayout((s) => s.sidebarCollapsed)
  const trafficLights = useTrafficLights()
  // A few pixels of movement start a drag, so clicks, middle-clicks and the context menu still work.
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 6 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  )
  const onDragEnd = ({ active, over }: DragEndEvent) => {
    if (over && active.id !== over.id) useStore.getState().moveTab(String(active.id), String(over.id))
  }

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const m = e.metaKey || e.ctrlKey
      if (m && e.shiftKey && e.key.toLowerCase() === 't') {
        // Reopen the last closed tab, like a browser.
        e.preventDefault()
        const last = useLibrary.getState().history[0]
        if (last) {
          // Taken off the list so pressing again walks further back; it returns when closed.
          useLibrary.getState().remove('history', last.id)
          void reopen(last)
        }
      } else if (m && e.key === 't') {
        e.preventDefault()
        if (activeTab) void openEditor(activeTab.sessionId, activeTab.database, activeTab.schema)
      } else if (m && e.key === 'w') {
        e.preventDefault()
        if (activeTab) void closeWithConfirm(activeTab)
      } else if (m && e.shiftKey && (e.key === ']' || e.key === '[')) {
        e.preventDefault()
        const i = tabs.findIndex((t) => t.id === activeTab?.id)
        const next = tabs[(i + (e.key === ']' ? 1 : -1) + tabs.length) % tabs.length]
        if (next) setActive(next.id)
      } else if (m && /^[1-9]$/.test(e.key)) {
        const t = tabs[Number(e.key) - 1]
        if (t) {
          e.preventDefault()
          setActive(t.id)
        }
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [tabs, activeTab, setActive])

  return (
    <div className="flex h-full min-w-0 flex-col bg-bg">
      <div ref={strip} className={cn('drag scroll-x flex h-[38px] shrink-0 items-end gap-px border-b border-line bg-panel pr-2', sidebarCollapsed && trafficLights ? 'pl-21' : sidebarCollapsed ? 'pl-2' : 'pl-1')} role="tablist">
        {sidebarCollapsed && (
          // Sits right of the window buttons while the sidebar is hidden.
          <IconButton label={<span>Show sidebar <Kbd>{mod}B</Kbd></span>} className="no-drag sticky left-0 z-20 mr-1 mb-0.5 bg-panel" onClick={toggleSidebar}>
            <PanelLeftOpen size={16} />
          </IconButton>
        )}
        <DndContext sensors={sensors} collisionDetection={closestCenter} modifiers={[restrictToHorizontalAxis]} onDragEnd={onDragEnd}>
          <SortableContext items={tabs.map((t) => t.id)} strategy={horizontalListSortingStrategy}>
            {tabs.map((t) => (
              <TabItem
                key={t.id}
                tab={t}
                active={t.id === activeTab?.id}
                profile={profiles.find((p) => p.id === t.sessionId)}
                onSelect={() => setActive(t.id)}
                onCloseOthers={async () => {
                  for (const x of tabs.filter((x) => x.id !== t.id)) await closeWithConfirm(x)
                }}
              />
            ))}
          </SortableContext>
        </DndContext>
        {activeTab && (
          <IconButton label={<span>{isShellKind(activeTab.kind) ? 'New shell' : 'New SQL editor'} <Kbd>{mod}T</Kbd></span>} className="no-drag ml-1 self-center" onClick={() => openEditor(activeTab.sessionId, activeTab.database, activeTab.schema)}>
            <Plus size={15} />
          </IconButton>
        )}
      </div>
      <div className="relative min-h-0 flex-1">
        {tabs.map((t) => (
          <div key={t.id} className={cn('absolute inset-0', t.id !== activeTab?.id && 'invisible')}>
            <TabBody tab={t} active={t.id === activeTab?.id} />
          </div>
        ))}
        {tabs.length === 0 && <EmptyState />}
      </div>
    </div>
  )
}

function TabBody({ tab: t, active }: { tab: Tab; active: boolean }) {
  switch (t.kind) {
    case 'index':
      return <IndexTab tab={t} />
    case 'transfer':
      return <TransferTab tab={t} />
    case 'shell':
      return <ShellTab tab={t} active={active} />
    case 'docindex':
      return <ShellIndexTab tab={t} />
    case 'doctransfer':
      return <ShellTransferTab tab={t} />
  }
  return <SqlTab tab={t} active={active} />
}

function TabItem({
  tab: t,
  active: isActive,
  profile,
  onSelect,
  onCloseOthers,
}: {
  tab: Tab
  active: boolean
  profile?: Profile
  onSelect: () => void
  onCloseOthers: () => void
}) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({ id: t.id })
  const job = useStore((s) => (t.transfer?.jobId ? s.jobs[t.transfer.jobId] : undefined))
  const docJob = useStore((s) => (t.docTransfer?.jobId ? s.docJobs[t.docTransfer.jobId] : undefined))
  const jobState = job?.state ?? docJob?.state
  const jobRunning = jobState === 'running'
  const fraction = job ? jobFraction(job) : docJob ? docJobFraction(docJob) : 0
  const transferMode = t.transfer?.mode ?? t.docTransfer?.mode
  const special = t.kind !== 'sql' && t.kind !== 'ddl' && t.kind !== 'shell' && t.kind !== undefined
  const duplicate = () => {
    const s = useStore.getState()
    if (t.kind === 'shell') s.openShellTab(t.sessionId, t.database, t.script, { title: t.title })
    else s.openTab(t.sessionId, t.database, t.schema, t.script, { title: t.title })
  }
  return (
    <ContextMenu
      items={[
        { label: 'Close', shortcut: `${mod}W`, onSelect: () => closeWithConfirm(t) },
        { label: 'Close other tabs', onSelect: onCloseOthers },
        { separator: true, hidden: special },
        { label: 'Duplicate', hidden: special, onSelect: duplicate },
        { label: 'Add to favorites…', hidden: special || !t.script.trim(), onSelect: () => saveFavorite(t) },
      ]}
    >
      <div
        ref={setNodeRef}
        {...attributes}
        {...listeners}
        role="tab"
        aria-roledescription="draggable tab"
        aria-selected={isActive}
        tabIndex={0}
        // Activate on press, like browser tabs: instant, and unaffected by the click a drag swallows.
        onPointerDown={(e) => {
          listeners?.onPointerDown?.(e)
          if (e.button === 0) onSelect()
        }}
        onKeyDown={(e) => {
          listeners?.onKeyDown?.(e)
          if (e.key === 'Enter') onSelect()
        }}
        onAuxClick={(e) => e.button === 1 && closeWithConfirm(t)}
        title={`${profile?.name ?? ''} · ${[t.database, t.schema || t.docIndex?.collection].filter(Boolean).join('.')}${t.filePath ? ` · ${t.filePath}` : ''}${t.tx?.open ? ' · transaction open' : ''}`}
        style={{ transform: CSS.Translate.toString(transform), transition }}
        className={cn(
          'no-drag group relative flex h-8 max-w-60 min-w-28 shrink-0 items-center gap-2 rounded-t-lg pr-1.5 pl-3 text-xs',
          isActive ? 'bg-bg text-fg shadow-[0_0_0_1px_var(--line)]' : 'text-muted hover:bg-hover hover:text-fg',
          isDragging && 'z-10 cursor-grabbing bg-raised opacity-90 shadow-lg',
        )}
      >
        {isActive && !isDragging && <span className="absolute inset-x-0 -bottom-px h-px bg-bg" />}
        <span className="absolute inset-x-2 top-0 h-0.5 rounded-b" style={{ background: profile?.color || (isActive ? 'var(--accent)' : 'transparent') }} />
        {t.kind === 'ddl' ? (
          <FileCode2 size={12} className="shrink-0 opacity-70" />
        ) : t.kind === 'index' || t.kind === 'docindex' ? (
          <KeyRound size={12} className="shrink-0 opacity-70" />
        ) : t.kind === 'transfer' || t.kind === 'doctransfer' ? (
          transferMode === 'export' ? (
            <Download size={12} className={cn('shrink-0 opacity-70', jobState === 'failed' && 'text-danger opacity-100')} />
          ) : (
            <Upload size={12} className={cn('shrink-0 opacity-70', jobState === 'failed' && 'text-danger opacity-100')} />
          )
        ) : (
          <SquareTerminal size={12} className="shrink-0 opacity-70" />
        )}
        <span className="min-w-0 flex-1 truncate">
          {tabTitle(t)}
          <span className="ml-1.5 text-faint">{profile?.name}</span>
        </span>
        {jobRunning ? (
          <span className="relative flex size-4.5 items-center justify-center">
            <ProgressRing value={fraction} className="text-accent group-hover:hidden" />
            <button
              aria-label="Close tab"
              onPointerDown={(e) => e.stopPropagation()}
              onClick={(e) => {
                e.stopPropagation()
                void closeWithConfirm(t)
              }}
              className="hidden rounded p-0.5 text-faint group-hover:block hover:bg-hover hover:text-fg"
            >
              <X size={12} />
            </button>
          </span>
        ) : t.tx?.open ? (
          <span title="Transaction open" className="size-1.5 rounded-full bg-warn" />
        ) : t.running ? (
          <span className="size-1.5 animate-pulse-soft rounded-full bg-accent" />
        ) : (
          <button
            aria-label="Close tab"
            onPointerDown={(e) => e.stopPropagation()}
            onClick={(e) => {
              e.stopPropagation()
              void closeWithConfirm(t)
            }}
            className={cn('rounded p-0.5 text-faint hover:bg-hover hover:text-fg', !isActive && 'opacity-0 group-hover:opacity-100', t.dirty && t.filePath && 'opacity-100')}
          >
            {t.dirty && t.filePath ? <span className="block size-2 rounded-full bg-muted group-hover:hidden" /> : null}
            <X size={12} className={cn(t.dirty && t.filePath && 'hidden group-hover:block')} />
          </button>
        )}
      </div>
    </ContextMenu>
  )
}

function EmptyState() {
  const profiles = useStore((s) => s.profiles)
  const sessions = useStore((s) => s.sessions)
  const drivers = useStore((s) => s.drivers)
  const openDialog = useUI((s) => s.openConnectionDialog)
  return (
    <div className="flex h-full flex-col items-center justify-center gap-6 p-8">
      <div className="text-center">
        <h1 className="text-lg font-semibold">Open an editor to start querying</h1>
        <p className="mt-1 text-[13px] text-muted">Double-click a table or collection in the sidebar, or pick a connection below.</p>
      </div>
      <div className="grid w-full max-w-md grid-cols-[minmax(0,1fr)] gap-1.5">
        {profiles.slice(0, 8).map((p) => (
          <button
            key={p.id}
            onClick={() => openEditor(p.id)}
            className="flex items-center gap-3 rounded-lg border border-line bg-panel px-3 py-2.5 text-left hover:border-line-strong hover:bg-hover"
          >
            <DriverIcon driver={p.driver} size={16} />
            <span className="min-w-0 flex-1">
              <span className="block truncate text-[13px] font-medium">{p.name}</span>
              <span className="block truncate text-xs text-faint">
                {drivers[p.driver]?.label ?? p.driver} · {p.driver === 'sqlite' ? p.database || ':memory:' : p.host || 'localhost'}
              </span>
            </span>
            {sessions[p.id] && <span className="size-1.5 rounded-full bg-ok" />}
          </button>
        ))}
        <button
          onClick={() => openDialog()}
          className="flex items-center justify-center gap-2 rounded-lg border border-dashed border-line-strong px-3 py-2.5 text-[13px] text-muted hover:border-accent hover:text-accent"
        >
          <Plus size={15} /> New connection
        </button>
      </div>
      <div className="flex gap-4 text-xs text-faint">
        <span>
          <Kbd>{mod}↵</Kbd> run statement
        </span>
        <span>
          <Kbd>⇧{mod}↵</Kbd> run script
        </span>
        <span>
          <Kbd>{mod}T</Kbd> new tab
        </span>
        <span>
          <Kbd>⇧{mod}T</Kbd> reopen closed tab
        </span>
      </div>
      <div className="flex gap-3 text-xs">
        <button className="flex items-center gap-1.5 text-muted hover:text-accent" onClick={() => openLibrary('favorites')}>
          <Star size={13} /> Favorites
        </button>
        <button className="flex items-center gap-1.5 text-muted hover:text-accent" onClick={() => openLibrary('history')}>
          <History size={13} /> Closed tabs
        </button>
      </div>
    </div>
  )
}
