import { useEffect, useRef, useState } from 'react'
import { Group, Panel, Separator } from 'react-resizable-panels'
import { ChevronDown, ChevronRight, Database, FileDown, FolderOpen, Lock, Play, Save, Square, StepForward, WifiOff } from 'lucide-react'
import { toast } from 'sonner'
import { Files, Mode, errorMessage } from '../../lib/api'
import { keys, useStore, type Tab } from '../../lib/store'
import { cn, formatDuration, layoutKeys, mod, readLocal, shift, writeLocal } from '../../lib/utils'
import { Editor, type EditorHandle } from './ShellEditor'
import { Button, DropdownMenu, IconButton, Kbd, Tip } from '../ui'
import { ResultsPanel } from './results/ResultsPanel'

function useElapsed(running?: boolean) {
  const [ms, setMs] = useState(0)
  useEffect(() => {
    if (!running) return
    const start = Date.now()
    setMs(0)
    const t = setInterval(() => setMs(Date.now() - start), 100)
    return () => clearInterval(t)
  }, [running])
  return ms
}

export function ShellTab({ tab, active }: { tab: Tab; active: boolean }) {
  const editor = useRef<EditorHandle>(null)
  const session = useStore((s) => s.sessions[tab.sessionId])
  const profile = useStore((s) => s.profiles.find((p) => p.id === tab.sessionId))
  const databases = useStore((s) => s.databases[tab.sessionId]?.items)
  const connecting = useStore((s) => !!s.connecting[tab.sessionId])
  const collections = useStore((s) => s.collections[keys.db(tab.sessionId, tab.database)]?.items)
  const { run, cancel, updateTab, loadDatabases, loadCollections, connect, openShellTab } = useStore.getState()
  const elapsed = useElapsed(tab.running)

  useEffect(() => {
    if (session && !collections) void loadCollections(tab.sessionId, tab.database)
  }, [session, collections, tab.sessionId, tab.database, loadCollections])

  const exec = (all: boolean) => {
    const sel = editor.current?.selection() ?? { cursor: 0, from: 0, to: 0 }
    const mode = sel.to > sel.from ? Mode.ModeSelection : all ? Mode.ModeAll : Mode.ModeCurrent
    void run(tab.id, mode, sel.cursor, sel.from, sel.to)
  }

  const save = async (saveAs = false) => {
    try {
      const path = await Files.SaveScript('shell', tab.filePath ?? '', tab.script, saveAs)
      if (path) {
        updateTab(tab.id, { filePath: path, dirty: false, title: path.split(/[\\/]/).pop() })
        toast.success('Script saved', { description: path })
      }
    } catch (err) {
      toast.error('Could not save script', { description: errorMessage(err) })
    }
  }

  const load = async () => {
    try {
      const f = await Files.OpenScript('shell')
      if (!f.path) return
      if (tab.dirty && tab.script.trim()) {
        const id = openShellTab(tab.sessionId, tab.database, f.content, { title: f.name })
        updateTab(id, { filePath: f.path })
      } else {
        updateTab(tab.id, { script: f.content, filePath: f.path, title: f.name, dirty: false })
      }
    } catch (err) {
      toast.error('Could not load script', { description: errorMessage(err) })
    }
  }

  const health = session?.health
  const host = session?.host ?? profile?.host ?? ''

  return (
    <div className="flex h-full min-h-0 flex-col">
      {/* Row 1: toolbar + connection info */}
      <div className="relative flex h-11 shrink-0 items-center gap-1 border-b border-line bg-panel px-2">
        <Tip label={<span>Run script <Kbd>{shift}{mod}↵</Kbd> · <Kbd>F5</Kbd></span>}>
          <Button variant="primary" size="sm" className="h-7 px-2.5" onClick={() => exec(true)} disabled={tab.running}>
            <Play size={13} fill="currentColor" />
            Run
          </Button>
        </Tip>
        <IconButton label={<span>Run statement at cursor <Kbd>{mod}↵</Kbd> · <Kbd>F6</Kbd></span>} onClick={() => exec(false)} disabled={tab.running}>
          <StepForward size={15} />
        </IconButton>
        {tab.running && (
          <Button variant="ghost" size="sm" className="h-7 text-danger hover:text-danger" onClick={() => cancel(tab.id)}>
            <Square size={11} fill="currentColor" />
            Stop
            <span className="tabular-nums text-faint">{formatDuration(elapsed)}</span>
          </Button>
        )}
        <div className="mx-1 h-4 w-px bg-line" />
        <IconButton label={<span>Load script <Kbd>{mod}O</Kbd></span>} onClick={load}>
          <FolderOpen size={15} />
        </IconButton>
        <IconButton label={<span>Save script <Kbd>{mod}S</Kbd></span>} onClick={() => save(false)}>
          <Save size={15} />
        </IconButton>
        <IconButton label={<span>Save as… <Kbd>{shift}{mod}S</Kbd></span>} onClick={() => save(true)}>
          <FileDown size={15} />
        </IconButton>
        {tab.retry && (
          <span className="ml-2 flex min-w-0 items-center gap-1.5 truncate text-xs text-warn" title={tab.retry}>
            <WifiOff size={13} className="shrink-0" /> Connection interrupted — retrying automatically…
          </span>
        )}

        <div className="ml-auto flex min-w-0 items-center gap-1 text-xs text-muted">
          {session?.readOnly && (
            <span className="mr-1 flex items-center gap-1 rounded-md bg-sunken px-1.5 py-0.5 text-[11px]">
              <Lock size={11} /> Read-only
            </span>
          )}
          {session?.user && <span className="truncate">{session.user}</span>}
          {session?.user && <ChevronRight size={12} className="shrink-0 text-faint" />}
          {!session && (
            <span className="mr-1 flex items-center gap-1.5 rounded-md bg-sunken py-0.5 pr-0.5 pl-2 text-[11px] text-muted">
              Not connected
              <button
                className="rounded px-1.5 py-0.5 font-medium text-accent hover:bg-accent-soft disabled:opacity-50"
                disabled={connecting}
                onClick={() => connect(tab.sessionId)}
              >
                {connecting ? 'Connecting…' : 'Connect'}
              </button>
            </span>
          )}
          <span className="flex min-w-0 items-center gap-1.5">
            {session && (
              <span
                title={health === 'reconnecting' ? 'Reconnecting…' : 'Connected'}
                className={cn(
                  'size-1.5 shrink-0 rounded-full',
                  health === 'online' && 'bg-ok',
                  health === 'reconnecting' && 'animate-pulse-soft bg-warn',
                )}
              />
            )}
            {profile?.color && <span className="h-3 w-0.5 rounded" style={{ background: profile.color }} />}
            <span className="truncate font-medium text-fg" title={host}>
              {profile?.name ?? 'Unknown connection'}
            </span>
            <span className="hidden max-w-60 truncate text-faint xl:inline" title={host}>
              {host}
            </span>
          </span>
          <ChevronRight size={12} className="shrink-0 text-faint" />
          <DropdownMenu
            align="end"
            items={(databases ?? [tab.database]).map((db) => ({
              label: db,
              icon: db === tab.database ? <Database size={13} /> : undefined,
              onSelect: () => updateTab(tab.id, { database: db }),
            }))}
          >
            <button
              className="flex h-7 items-center gap-1.5 rounded-md px-2 text-fg hover:bg-hover"
              onPointerDown={() => {
                if (session && !databases) void loadDatabases(tab.sessionId)
              }}
            >
              <Database size={13} className="text-muted" />
              <span className="font-medium">{tab.database}</span>
              <ChevronDown size={12} className="text-faint" />
            </button>
          </DropdownMenu>
        </div>
        {tab.running && <div className="progress-bar absolute inset-x-0 -bottom-px h-0.5 overflow-hidden" />}
      </div>

      {/* Rows 2 & 3: editor and results, resizable */}
      <Group
        orientation="vertical"
        id={`shell-${tab.id}`}
        className="min-h-0 flex-1"
        // Hidden tabs share the same geometry; only the visible one may react to a drag.
        disabled={!active}
        onLayoutChanged={(layout, meta) => {
          if (!meta.isUserInteraction || layout.editor == null) return
          // Saved per tab; new tabs start from the last size used.
          updateTab(tab.id, { editorSize: layout.editor })
          writeLocal(layoutKeys.editorPercent, layout.editor)
        }}
      >
        <Panel id="editor" defaultSize={`${tab.editorSize ?? readLocal(layoutKeys.editorPercent, 38)}%`} minSize="60px">
          <Editor
            ref={editor}
            className="h-full bg-sunken"
            value={tab.script}
            autoFocus
            placeholder={'// Write a query and press ' + mod + '↵\ndb.getCollection("orders").find({ status: "open" })'}
            onChange={(script) => updateTab(tab.id, { script, dirty: true })}
            completions={() => {
              const s = useStore.getState()
              return {
                collections: (s.collections[keys.db(tab.sessionId, tab.database)]?.items ?? []).map((c) => c.name),
                databases: s.databases[tab.sessionId]?.items ?? [],
              }
            }}
            keys={[
              { key: 'Mod-Enter', run: () => exec(false) },
              { key: 'Shift-Mod-Enter', run: () => exec(true) },
              { key: 'F5', run: () => exec(true) },
              { key: 'F6', run: () => exec(false) },
              { key: 'Mod-s', run: () => void save(false) },
              { key: 'Shift-Mod-s', run: () => void save(true) },
              { key: 'Mod-o', run: () => void load() },
            ]}
          />
        </Panel>
        <Separator className="resize-handle is-row" />
        <Panel id="results" minSize="120px">
          <ResultsPanel tab={tab} />
        </Panel>
      </Group>
    </div>
  )
}
