import { useEffect, useMemo, useRef, useState } from 'react'
import { Group, Panel, Separator } from 'react-resizable-panels'
import {
  ChevronDown,
  ChevronRight,
  Database,
  FileDown,
  FolderOpen,
  GitCommitHorizontal,
  KeyRound,
  Layers,
  ListTree,
  Lock,
  Play,
  Save,
  Sparkles,
  Square,
  StepForward,
  Undo2,
  WifiOff,
} from 'lucide-react'
import { toast } from 'sonner'
import { Files, Mode, errorMessage } from '../lib/api'
import { keys, useStore, type Tab } from '../lib/store'
import { prettySQL } from '../lib/sql'
import { cn, formatDuration, layoutKeys, mod, readLocal, shift, writeLocal } from '../lib/utils'
import { Editor, type EditorHandle } from './Editor'
import { Button, DropdownMenu, IconButton, Kbd, Tip } from './ui'
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

export function SqlTab({ tab, active }: { tab: Tab; active: boolean }) {
  const editor = useRef<EditorHandle>(null)
  const session = useStore((s) => s.sessions[tab.sessionId])
  const profile = useStore((s) => s.profiles.find((p) => p.id === tab.sessionId))
  const info = useStore((s) => (profile ? s.drivers[profile.driver] : undefined))
  const databases = useStore((s) => s.databases[tab.sessionId]?.items)
  const schemas = useStore((s) => s.schemas[keys.db(tab.sessionId, tab.database)]?.items)
  const connecting = useStore((s) => !!s.connecting[tab.sessionId])
  const schema = tab.schema || session?.schema || ''
  const completionKey = keys.schema(tab.sessionId, tab.database, schema)
  const { run, cancel, updateTab, loadDatabases, loadSchemas, loadCompletions, connect, openTab, setManual, commit, rollback } = useStore.getState()
  const elapsed = useElapsed(tab.running)

  useEffect(() => {
    if (session && active) void loadCompletions(tab.sessionId, tab.database, schema)
  }, [session, active, tab.sessionId, tab.database, schema, loadCompletions])

  const exec = (all: boolean, explain = false) => {
    const sel = editor.current?.selection() ?? { cursor: 0, from: 0, to: 0 }
    const mode = sel.to > sel.from ? Mode.ModeSelection : all ? Mode.ModeAll : Mode.ModeCurrent
    void run(tab.id, mode, sel.cursor, sel.from, sel.to, explain)
  }

  const save = async (saveAs = false) => {
    try {
      const path = await Files.SaveScript('sql', tab.filePath ?? '', tab.script, saveAs)
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
      const f = await Files.OpenScript('sql')
      if (!f.path) return
      if (tab.dirty && tab.script.trim()) {
        const id = openTab(tab.sessionId, tab.database, tab.schema, f.content, { title: f.name })
        updateTab(id, { filePath: f.path })
      } else {
        updateTab(tab.id, { script: f.content, filePath: f.path, title: f.name, dirty: false })
      }
    } catch (err) {
      toast.error('Could not load script', { description: errorMessage(err) })
    }
  }

  const format = () => {
    const v = editor.current?.view
    if (!v) return
    const sel = v.state.selection.main
    const dialect = info?.editor ?? 'StandardSQL'
    if (sel.empty) editor.current?.replace(prettySQL(v.state.doc.toString(), dialect))
    else v.dispatch({ changes: { from: sel.from, to: sel.to, insert: prettySQL(v.state.sliceDoc(sel.from, sel.to), dialect) } })
  }

  const sqlSupport = useMemo(
    () => ({
      dialect: info?.editor ?? 'StandardSQL',
      split: info?.split ?? undefined,
      functions: info?.functions ?? [],
      snippets: info?.snippets ?? [],
      completion: () => useStore.getState().completions[completionKey]?.items,
      schema: () => schema,
    }),
    [info, completionKey, schema],
  )

  const health = session?.health
  const host = session?.host ?? profile?.host ?? ''
  const tx = tab.tx
  const canTx = !!info?.transactions && !session?.readOnly

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="@container relative flex h-11 shrink-0 items-center gap-1 overflow-hidden border-b border-line bg-panel px-2">
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
        <span className="hidden items-center gap-1 @2xl:flex">
          <IconButton label={<span>Load script <Kbd>{mod}O</Kbd></span>} onClick={load}>
            <FolderOpen size={15} />
          </IconButton>
          <IconButton label={<span>Save script <Kbd>{mod}S</Kbd></span>} onClick={() => save(false)}>
            <Save size={15} />
          </IconButton>
          <IconButton label={<span>Save as… <Kbd>{shift}{mod}S</Kbd></span>} onClick={() => save(true)}>
            <FileDown size={15} />
          </IconButton>
        </span>
        {/* Narrow windows: the file actions share one menu. */}
        <span className="@2xl:hidden">
          <DropdownMenu
            items={[
              { label: 'Load script…', icon: <FolderOpen size={14} />, shortcut: `${mod}O`, onSelect: load },
              { label: 'Save', icon: <Save size={14} />, shortcut: `${mod}S`, onSelect: () => save(false) },
              { label: 'Save as…', icon: <FileDown size={14} />, shortcut: `${shift}${mod}S`, onSelect: () => save(true) },
            ]}
          >
            <button aria-label="File" className="inline-flex size-7 items-center justify-center rounded-md text-muted hover:bg-hover hover:text-fg">
              <FolderOpen size={15} />
            </button>
          </DropdownMenu>
        </span>
        <div className="mx-1 h-4 w-px bg-line" />
        {info?.explain && (
          <IconButton label={<span>{info.explain} the statement at the cursor <Kbd>{shift}{mod}E</Kbd></span>} onClick={() => exec(false, true)} disabled={tab.running}>
            <ListTree size={15} />
          </IconButton>
        )}
        <IconButton label={<span>Format SQL <Kbd>{shift}{mod}F</Kbd></span>} onClick={format}>
          <Sparkles size={15} />
        </IconButton>
        {canTx && (
          <>
            <div className="mx-1 h-4 w-px bg-line" />
            <Tip label={tx?.manual ? 'Manual transaction: changes wait for Commit. Click for auto-commit.' : 'Auto-commit: every statement is saved at once. Click for a manual transaction.'}>
              <button
                onClick={() => setManual(tab.id, !tx?.manual)}
                disabled={tab.running || (tx?.manual && tx.open)}
                className={cn(
                  'flex h-7 items-center gap-1.5 rounded-md px-2 text-xs transition-colors disabled:opacity-60',
                  tx?.manual ? 'bg-warn/15 font-medium text-warn' : 'text-muted hover:bg-hover hover:text-fg',
                )}
              >
                <GitCommitHorizontal size={14} />
                <span className="hidden @3xl:inline">{tx?.manual ? 'Manual' : 'Auto-commit'}</span>
              </button>
            </Tip>
          </>
        )}
        {tx?.open && (
          <span className="flex items-center gap-1">
            <span className="hidden text-[11px] whitespace-nowrap text-warn @4xl:inline">{tx.statements ? `${tx.statements} pending` : 'transaction open'}</span>
            <Button size="sm" variant="secondary" className="h-6.5" onClick={() => commit(tab.id)} disabled={tab.running}>
              Commit
            </Button>
            <Tip label="Roll back">
              <Button size="sm" variant="ghost" className="h-6.5 px-1.5" onClick={() => rollback(tab.id)} disabled={tab.running} aria-label="Roll back">
                <Undo2 size={13} />
              </Button>
            </Tip>
          </span>
        )}
        {tab.retry && (
          <span className="ml-2 flex min-w-0 items-center gap-1.5 truncate text-xs text-warn" title={tab.retry}>
            <WifiOff size={13} className="shrink-0" /> Network problem, retrying…
          </span>
        )}

        <div className="ml-auto flex min-w-0 shrink items-center gap-1 text-xs text-muted">
          {session?.readOnly && (
            <span className="mr-1 flex items-center gap-1 rounded-md bg-sunken px-1.5 py-0.5 text-[11px]">
              <Lock size={11} /> <span className="hidden @2xl:inline">Read-only</span>
            </span>
          )}
          {!session && (
            <span className="mr-1 flex shrink-0 items-center gap-1.5 rounded-md bg-sunken py-0.5 pr-0.5 pl-2 text-[11px] whitespace-nowrap text-muted">
              <span className="hidden @3xl:inline">Not connected</span>
              <button className="rounded px-1.5 py-0.5 font-medium text-accent hover:bg-accent-soft disabled:opacity-50" disabled={connecting} onClick={() => connect(tab.sessionId)}>
                {connecting ? 'Connecting…' : 'Connect'}
              </button>
            </span>
          )}
          <span className="flex min-w-0 items-center gap-1.5">
            {session && (
              <span
                title={health === 'reconnecting' ? 'Reconnecting…' : 'Connected'}
                className={cn('size-1.5 shrink-0 rounded-full', health === 'online' && 'bg-ok', health === 'reconnecting' && 'animate-pulse-soft bg-warn')}
              />
            )}
            {profile?.color && <span className="h-3 w-0.5 rounded" style={{ background: profile.color }} />}
            <span className="hidden truncate font-medium text-fg @2xl:inline" title={host}>
              {profile?.name ?? 'Unknown connection'}
            </span>
            <span className="hidden max-w-60 truncate text-faint @5xl:inline" title={host}>
              {host}
            </span>
          </span>
          {info?.databases && (
            <>
              <ChevronRight size={12} className="hidden shrink-0 text-faint @2xl:block" />
              <DropdownMenu
                align="end"
                items={(databases ?? [tab.database]).filter(Boolean).map((db) => ({
                  label: db,
                  icon: db === tab.database ? <Database size={13} /> : undefined,
                  onSelect: () => updateTab(tab.id, { database: db, schema: '' }),
                }))}
              >
                <button
                  title={`${profile?.name ?? ''} · ${tab.database || session?.database || ''}`}
                  className="flex h-7 max-w-44 shrink-0 items-center gap-1.5 rounded-md px-2 text-fg hover:bg-hover"
                  onPointerDown={() => {
                    if (session && !databases) void loadDatabases(tab.sessionId)
                  }}
                >
                  <Database size={13} className="shrink-0 text-muted" />
                  <span className="truncate font-medium">{tab.database || session?.database || 'default'}</span>
                  <ChevronDown size={12} className="shrink-0 text-faint" />
                </button>
              </DropdownMenu>
            </>
          )}
          {info?.partitionKey && (
            <>
              <ChevronRight size={12} className="shrink-0 text-faint" />
              <Tip label={<span>Partition key of the queries: "text", 12, true or ["a", 1]. Empty: every partition.</span>}>
                <label className="flex h-7 shrink-0 items-center gap-1.5 rounded-md px-2 hover:bg-hover">
                  <KeyRound size={13} className="shrink-0 text-muted" />
                  <input
                    value={tab.partition ?? ''}
                    onChange={(e) => updateTab(tab.id, { partition: e.target.value })}
                    placeholder="all partitions"
                    spellCheck={false}
                    aria-label="Partition key"
                    className="w-24 bg-transparent font-mono text-xs text-fg outline-none placeholder:text-faint @4xl:w-32"
                  />
                </label>
              </Tip>
            </>
          )}
          {info?.schemas && (
            <>
              <ChevronRight size={12} className="shrink-0 text-faint" />
              <DropdownMenu
                align="end"
                items={(schemas ?? [schema]).filter(Boolean).map((sc) => ({
                  label: sc,
                  icon: sc === schema ? <Layers size={13} /> : undefined,
                  onSelect: () => updateTab(tab.id, { schema: sc }),
                }))}
              >
                <button
                  className="flex h-7 max-w-44 shrink-0 items-center gap-1.5 rounded-md px-2 text-fg hover:bg-hover"
                  onPointerDown={() => {
                    if (session && !schemas) void loadSchemas(tab.sessionId, tab.database)
                  }}
                >
                  <Layers size={13} className="shrink-0 text-muted" />
                  <span className="truncate font-medium">{schema || 'default'}</span>
                  <ChevronDown size={12} className="shrink-0 text-faint" />
                </button>
              </DropdownMenu>
            </>
          )}
        </div>
        {tab.running && <div className="progress-bar absolute inset-x-0 -bottom-px h-0.5 overflow-hidden" />}
      </div>

      <Group
        orientation="vertical"
        id={`sql-${tab.id}`}
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
            autoFocus={active}
            placeholder={`-- Write SQL and press ${mod}↵ to run the statement at the cursor\nSELECT * FROM orders WHERE status = 'open';`}
            onChange={(script) => updateTab(tab.id, { script, dirty: true })}
            sql={sqlSupport}
            keys={[
              { key: 'Mod-Enter', run: () => exec(false) },
              { key: 'Shift-Mod-Enter', run: () => exec(true) },
              { key: 'F5', run: () => exec(true) },
              { key: 'F6', run: () => exec(false) },
              { key: 'Shift-Mod-e', run: () => exec(false, true) },
              { key: 'Shift-Mod-f', run: () => format() },
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
