import { create } from 'zustand'
import { toast } from 'sonner'
import {
  Connections,
  Queries,
  Mode,
  errorMessage,
  type Column,
  type CompletionInfo,
  type DbObject,
  type DriverInfo,
  type Page,
  type Profile,
  type Result,
  type SessionInfo,
  type TableDetails,
  type TableInfo,
  type TransferJob,
  type TxState,
} from './api'
import {
  Shell,
  type Mode as ShellMode,
  type Collection as DocCollection,
  type Index as DocIndex,
  type Page as DocPage,
  type Query as DocQuery,
  type TransferJob as DocTransferJob,
} from './shell/api'
import type { TransferMode } from './transfer'
import { useLibrary } from './library'
import { readLocal, uid, writeLocal } from './utils'

/** Result views: table, record and JSON for rows; tree, table and JSON for documents. */
export type ViewMode = 'table' | 'record' | 'json' | 'tree'

export interface ResultState {
  id: string
  kind: 'rows' | 'message' | 'error'
  title: string
  line: number
  statement: string
  columns: Column[]
  page?: Page
  offset: number
  pageSize: number
  count?: number | null
  counting?: boolean
  message?: string
  rowsAffected: number
  error?: string
  table?: TableInfo
  loading?: boolean
  opId?: string
  retry?: string
  durationMs: number
}

/** A result of a mongo shell tab (MongoDB, DocumentDB). */
export interface DocResultState {
  id: string
  kind: 'find' | 'aggregate' | 'value' | 'error'
  title: string
  line: number
  statement: string
  /** Query currently applied to the grid. */
  query?: DocQuery
  page?: DocPage
  offset: number
  pageSize: number
  count?: number | null
  counting?: boolean
  value?: string
  error?: string
  loading?: boolean
  opId?: string
  retry?: string
  durationMs: number
}

/** The index a document index tab shows; `name` is empty for a new index. */
export interface DocIndexRef {
  collection: string
  name?: string
}

/** What a document export/import tab shows: its setup until started, then the job. */
export interface DocTransferRef {
  mode: TransferMode
  jobId?: string
  /** Export: collections to preselect. Import: a target collection for every file. */
  collections?: string[]
}

/** What a DDL tab shows. */
export interface DdlRef {
  database: string
  schema: string
  object: DbObject
}

/** The index an index tab edits; name is empty for a new index. */
export interface IndexRef {
  database: string
  schema: string
  table: string
  name: string
}

/** What a transfer tab shows: the setup of a new export or import, or a running job. */
export interface TransferRef {
  mode: TransferMode
  jobId?: string
  /** Tables preselected for an export, or the target table of an import. */
  tables?: string[]
}

/** Tab kinds of SQL connections. */
export type SqlTabKind = 'sql' | 'ddl' | 'index' | 'transfer'
/** Tab kinds of MongoDB and DocumentDB connections: the mongo shell, the index editor
 * and exports and imports of collections. */
export type ShellTabKind = 'shell' | 'docindex' | 'doctransfer'

export interface Tab {
  id: string
  /** SQL tabs run scripts; DDL tabs show an object definition; index tabs edit an
   * index; transfer tabs export or import. Shell tabs run mongo shell scripts. */
  kind?: SqlTabKind | ShellTabKind
  ddl?: DdlRef
  index?: IndexRef
  transfer?: TransferRef
  docIndex?: DocIndexRef
  docTransfer?: DocTransferRef
  sessionId: string
  database: string
  schema: string
  /** Partition key (JSON) the tab's queries are limited to (Cosmos DB). */
  partition?: string
  title?: string
  script: string
  filePath?: string
  dirty?: boolean
  running?: boolean
  runOpId?: string
  retry?: string
  results: ResultState[]
  /** Results of shell tabs. */
  docResults: DocResultState[]
  output: string[]
  activeResult: string
  lastDurationMs?: number
  view: ViewMode
  /** Editor height as a percentage of the tab; results take the rest. */
  editorSize?: number
  tx?: TxState
}

export interface Loadable<T> {
  loading?: boolean
  error?: string
  items?: T
}

interface State {
  drivers: Record<string, DriverInfo>
  driverList: DriverInfo[]
  profiles: Profile[]
  sessions: Record<string, SessionInfo>
  connecting: Record<string, boolean>
  databases: Record<string, Loadable<string[]>>
  schemas: Record<string, Loadable<string[]>>
  objects: Record<string, Loadable<DbObject[]>>
  tables: Record<string, Loadable<TableDetails>>
  completions: Record<string, Loadable<CompletionInfo>>
  /** Collections and views of MongoDB databases, by keys.db. */
  collections: Record<string, Loadable<DocCollection[]>>
  /** Indexes of MongoDB collections, by keys.coll. */
  docIndexes: Record<string, Loadable<DocIndex[]>>
  expanded: Record<string, boolean>
  tabs: Tab[]
  activeTab: string
  pageSize: number
  /** Export/import jobs by id, with when the snapshot arrived (for a smooth clock). */
  jobs: Record<string, TransferJob & { receivedAt: number }>
  /** Export/import jobs of MongoDB connections. */
  docJobs: Record<string, DocTransferJob & { receivedAt: number }>

  loadDrivers(): Promise<void>
  loadProfiles(): Promise<void>
  /** Moves connection `id` to the position of `overId` and saves the order. */
  moveProfile(id: string, overId: string): Promise<void>
  connect(profileId: string): Promise<SessionInfo | null>
  disconnect(profileId: string): Promise<void>
  setHealth(sessionId: string, health: string): void
  toggle(key: string, open?: boolean): void
  loadDatabases(sessionId: string): Promise<void>
  loadSchemas(sessionId: string, db: string): Promise<void>
  loadObjects(sessionId: string, db: string, schema: string): Promise<void>
  loadTable(sessionId: string, db: string, schema: string, table: string, view: boolean): Promise<void>
  loadCompletions(sessionId: string, db: string, schema: string, force?: boolean): Promise<void>
  /** Forgets cached metadata of a connection (Refresh). */
  invalidate(sessionId: string, db?: string, schema?: string): void
  loadCollections(sessionId: string, db: string): Promise<void>
  loadDocIndexes(sessionId: string, db: string, coll: string): Promise<void>

  openTab(sessionId: string, database: string, schema: string, script?: string, opts?: { run?: boolean; title?: string }): string
  openDdlTab(sessionId: string, ref: DdlRef, script: string): string
  openIndexTab(sessionId: string, ref: IndexRef): string
  openTransferTab(mode: TransferMode, sessionId: string, database: string, schema: string, opts?: { tables?: string[]; jobId?: string }): string
  setJob(job: TransferJob): void
  /** Opens a mongo shell tab. */
  openShellTab(sessionId: string, database: string, script?: string, opts?: { run?: boolean; title?: string }): string
  /** Opens the editor of a collection index, or focuses it when open; no name means a new index. */
  openDocIndexTab(sessionId: string, database: string, collection: string, name?: string): string
  /** Opens a collection export or import tab; a job id shows that job (focusing its tab if open). */
  openDocTransferTab(mode: TransferMode, sessionId: string, database: string, opts?: { collections?: string[]; jobId?: string }): string
  setDocJob(job: DocTransferJob): void
  closeTab(id: string): void
  moveTab(id: string, overId: string): void
  setActiveTab(id: string): void
  updateTab(id: string, patch: Partial<Tab>): void
  run(tabId: string, mode: Mode, cursor: number, selStart: number, selEnd: number, explain?: boolean): Promise<void>
  cancel(tabId: string): void
  fetchPage(tabId: string, resultId: string, offset: number, pageSize?: number): Promise<void>
  count(tabId: string, resultId: string): Promise<void>
  requery(tabId: string, resultId: string, where: string, orderBy: string, limit: number): Promise<void>
  cancelResult(tabId: string, resultId: string): void
  /** Pages a shell result with a (possibly edited) query. */
  fetchDocs(tabId: string, resultId: string, query: DocQuery, offset: number, pageSize?: number): Promise<void>
  countDocs(tabId: string, resultId: string): Promise<void>
  onRetry(opId: string, attempt: number, error: string): void
  setManual(tabId: string, manual: boolean): Promise<void>
  commit(tabId: string): Promise<void>
  rollback(tabId: string): Promise<void>
}

const OUTPUT_ID = 'output'
const k2 = (s: string, db: string) => `${s}\u0000${db}`
const k3 = (s: string, db: string, schema: string) => `${s}\u0000${db}\u0000${schema}`
const k4 = (s: string, db: string, schema: string, t: string) => `${s}\u0000${db}\u0000${schema}\u0000${t}`
export const keys = { db: k2, schema: k3, table: k4, coll: k3 }

/** Whether tabs of this kind belong to MongoDB connections. */
export const isShellKind = (kind?: string) => kind === 'shell' || kind === 'docindex' || kind === 'doctransfer'

interface PersistedTab {
  id: string
  kind?: SqlTabKind | ShellTabKind
  ddl?: DdlRef
  index?: IndexRef
  docIndex?: DocIndexRef
  sessionId: string
  database: string
  schema: string
  partition?: string
  title?: string
  script: string
  filePath?: string
  dirty?: boolean
  view: ViewMode
  editorSize?: number
}

const persisted = readLocal<{ tabs: PersistedTab[]; activeTab: string }>('quire.tabs', { tabs: [], activeTab: '' })

function hydrate(t: PersistedTab): Tab {
  return { ...t, schema: t.schema ?? '', results: [], docResults: [], output: [], activeResult: OUTPUT_ID }
}

function toResult(r: Result, pageSize: number): ResultState {
  return {
    id: r.id,
    kind: r.kind as ResultState['kind'],
    title: r.title,
    line: r.line,
    statement: r.statement,
    columns: r.columns ?? [],
    page: r.page ?? undefined,
    offset: 0,
    pageSize,
    count: r.page && !r.page.hasMore ? r.page.offset + (r.page.rows?.length ?? 0) : undefined,
    message: r.message,
    rowsAffected: r.rowsAffected,
    error: r.error,
    table: r.table ?? undefined,
    durationMs: r.durationMs,
  }
}

export const useStore = create<State>()((set, get) => {
  const patchTab = (id: string, fn: (t: Tab) => Partial<Tab>) =>
    set((s) => ({ tabs: s.tabs.map((t) => (t.id === id ? { ...t, ...fn(t) } : t)) }))
  const patchResult = (tabId: string, resultId: string, fn: (r: ResultState) => Partial<ResultState>) =>
    patchTab(tabId, (t) => ({ results: t.results.map((r) => (r.id === resultId ? { ...r, ...fn(r) } : r)) }))
  const patchDocResult = (tabId: string, resultId: string, fn: (r: DocResultState) => Partial<DocResultState>) =>
    patchTab(tabId, (t) => ({ docResults: t.docResults.map((r) => (r.id === resultId ? { ...r, ...fn(r) } : r)) }))
  const load = async <T,>(slot: 'databases' | 'schemas' | 'objects' | 'tables' | 'completions' | 'collections' | 'docIndexes', key: string, fetch: () => Promise<T>) => {
    set((s) => ({ [slot]: { ...s[slot], [key]: { ...s[slot][key], loading: true, error: undefined } } }) as Partial<State>)
    try {
      const items = await fetch()
      set((s) => ({ [slot]: { ...s[slot], [key]: { items } } }) as Partial<State>)
    } catch (err) {
      set((s) => ({ [slot]: { ...s[slot], [key]: { ...s[slot][key], loading: false, error: errorMessage(err) } } }) as Partial<State>)
    }
  }

  /** Runs a mongo shell script (or part of it) in a shell tab. */
  const runShell = async (tab: Tab, mode: Mode, cursor: number, selStart: number, selEnd: number) => {
    const tabId = tab.id
    const opId = uid('run')
    const pageSize = get().pageSize
    patchTab(tabId, () => ({ running: true, runOpId: opId, retry: undefined }))
    try {
      const res = await Shell.Run(tab.sessionId, tab.database, opId, { script: tab.script, mode: mode as string as ShellMode, cursor, selStart, selEnd, pageSize })
      const results: DocResultState[] = (res.results ?? []).map((r) => ({
        id: uid('res'),
        kind: r.kind as DocResultState['kind'],
        title: r.title,
        line: r.line,
        statement: r.statement,
        query: r.query ?? undefined,
        page: r.page ?? undefined,
        offset: 0,
        pageSize,
        value: r.value,
        error: r.error,
        durationMs: r.durationMs,
      }))
      const output = res.output ?? []
      const failed = results.find((r) => r.kind === 'error')
      const firstData = results.find((r) => r.kind !== 'error') ?? results[0]
      patchTab(tabId, (t) => ({
        docResults: results,
        output,
        // use <db> in the script switches the tab's database.
        database: res.database || t.database,
        activeResult: failed?.id ?? (firstData && (firstData.kind !== 'value' || output.length === 0) ? firstData.id : OUTPUT_ID),
        lastDurationMs: res.durationMs,
      }))
      if (results.length === 0 && output.length === 0) toast.success('Script executed', { description: 'No results returned.' })
    } catch (err) {
      const id = uid('res')
      patchTab(tabId, () => ({
        docResults: [{ id, kind: 'error', title: 'Error', line: 0, statement: '', error: errorMessage(err), offset: 0, pageSize: 0, durationMs: 0 }],
        activeResult: id,
      }))
    } finally {
      patchTab(tabId, () => ({ running: false, runOpId: undefined, retry: undefined }))
    }
  }

  return {
    drivers: {},
    driverList: [],
    profiles: [],
    sessions: {},
    connecting: {},
    databases: {},
    schemas: {},
    objects: {},
    tables: {},
    completions: {},
    collections: {},
    docIndexes: {},
    // Connections always start collapsed and closed; the user picks what to open.
    expanded: Object.fromEntries(Object.entries(readLocal<Record<string, boolean>>('quire.expanded', {})).filter(([k]) => !k.startsWith('c:'))),
    tabs: persisted.tabs.map(hydrate),
    activeTab: persisted.activeTab,
    pageSize: readLocal('quire.pageSize', 100),
    jobs: {},
    docJobs: {},

    async loadDrivers() {
      const list = (await Connections.Drivers()) ?? []
      set({ driverList: list, drivers: Object.fromEntries(list.map((d) => [d.id, d])) })
    },

    async loadProfiles() {
      const [profiles, sessions] = await Promise.all([Connections.ListProfiles(), Connections.Sessions()])
      set({ profiles: profiles ?? [], sessions: Object.fromEntries((sessions ?? []).map((s) => [s.id, s])) })
    },

    async moveProfile(id, overId) {
      const profiles = [...get().profiles]
      const from = profiles.findIndex((p) => p.id === id)
      const to = profiles.findIndex((p) => p.id === overId)
      if (from < 0 || to < 0 || from === to) return
      const [moved] = profiles.splice(from, 1)
      profiles.splice(to, 0, moved)
      set({ profiles })
      try {
        await Connections.ReorderProfiles(profiles.map((p) => p.id))
      } catch (err) {
        toast.error('Could not save the order', { description: errorMessage(err) })
        await get().loadProfiles()
      }
    },

    async connect(profileId) {
      const existing = get().sessions[profileId]
      if (existing) return existing
      if (get().connecting[profileId]) return null
      set((s) => ({ connecting: { ...s.connecting, [profileId]: true }, databases: { ...s.databases, [profileId]: { loading: true } } }))
      try {
        const info = await Connections.Connect(profileId)
        set((s) => ({ sessions: { ...s.sessions, [profileId]: info } }))
        void get().loadDatabases(profileId)
        return info
      } catch (err) {
        const msg = errorMessage(err)
        set((s) => ({ databases: { ...s.databases, [profileId]: { error: msg } } }))
        toast.error('Could not connect', { description: msg })
        return null
      } finally {
        set((s) => ({ connecting: { ...s.connecting, [profileId]: false } }))
      }
    },

    async disconnect(profileId) {
      await Connections.Disconnect(profileId)
      get().invalidate(profileId)
      set((s) => {
        const sessions = { ...s.sessions }
        delete sessions[profileId]
        const databases = { ...s.databases }
        delete databases[profileId]
        return {
          sessions,
          databases,
          expanded: { ...s.expanded, [`c:${profileId}`]: false },
          // Transactions end with the connection.
          tabs: s.tabs.map((t) => (t.sessionId === profileId ? { ...t, tx: t.tx ? { ...t.tx, open: false, statements: 0 } : t.tx } : t)),
        }
      })
    },

    setHealth(sessionId, health) {
      set((s) => (s.sessions[sessionId] ? { sessions: { ...s.sessions, [sessionId]: { ...s.sessions[sessionId], health } } } : {}))
    },

    toggle(key, open) {
      set((s) => {
        const expanded = { ...s.expanded, [key]: open ?? !s.expanded[key] }
        writeLocal('quire.expanded', expanded)
        return { expanded }
      })
    },

    loadDatabases: (sessionId) => load('databases', sessionId, async () => (await Connections.ListDatabases(sessionId)) ?? []),
    loadSchemas: (sessionId, db) => load('schemas', k2(sessionId, db), async () => (await Connections.ListSchemas(sessionId, db)) ?? []),
    loadObjects: (sessionId, db, schema) => load('objects', k3(sessionId, db, schema), async () => (await Connections.ListObjects(sessionId, db, schema)) ?? []),
    loadTable: (sessionId, db, schema, table, view) =>
      load('tables', k4(sessionId, db, schema, table), () => Connections.GetTable(sessionId, db, schema, table, view)),
    async loadCompletions(sessionId, db, schema, force) {
      const key = k3(sessionId, db, schema)
      const cur = get().completions[key]
      if (!force && (cur?.loading || cur?.items)) return
      await load('completions', key, () => Connections.Completions(sessionId, db, schema))
    },

    invalidate(sessionId, db, schema) {
      const prefix = db === undefined ? `${sessionId}\u0000` : schema === undefined ? k2(sessionId, db) : k3(sessionId, db, schema)
      const drop = <T,>(m: Record<string, T>) => Object.fromEntries(Object.entries(m).filter(([k]) => !k.startsWith(prefix)))
      set((s) => ({
        schemas: drop(s.schemas),
        objects: drop(s.objects),
        tables: drop(s.tables),
        completions: drop(s.completions),
        collections: drop(s.collections),
        docIndexes: drop(s.docIndexes),
      }))
    },

    loadCollections: (sessionId, db) => load('collections', k2(sessionId, db), async () => (await Shell.ListCollections(sessionId, db)) ?? []),
    loadDocIndexes: (sessionId, db, coll) => load('docIndexes', k3(sessionId, db, coll), async () => (await Shell.ListIndexes(sessionId, db, coll)) ?? []),

    openTab(sessionId, database, schema, script = '', opts = {}) {
      const id = uid('tab')
      const tab: Tab = { id, kind: 'sql', sessionId, database, schema, title: opts.title, script, results: [], docResults: [], output: [], activeResult: OUTPUT_ID, view: 'table' }
      set((s) => ({ tabs: [...s.tabs, tab], activeTab: id }))
      if (opts.run) void get().run(id, Mode.ModeAll, 0, 0, 0)
      return id
    },

    openDdlTab(sessionId, ref, script) {
      const existing = get().tabs.find(
        (t) => t.kind === 'ddl' && t.sessionId === sessionId && t.ddl?.database === ref.database && t.ddl?.schema === ref.schema && t.ddl?.object.name === ref.object.name && t.ddl?.object.kind === ref.object.kind,
      )
      if (existing) {
        set((s) => ({ activeTab: existing.id, tabs: s.tabs.map((t) => (t.id === existing.id ? { ...t, script } : t)) }))
        return existing.id
      }
      const id = uid('tab')
      const tab: Tab = {
        id,
        kind: 'ddl',
        ddl: ref,
        sessionId,
        database: ref.database,
        schema: ref.schema,
        title: ref.object.name,
        script,
        results: [],
        docResults: [],
        output: [],
        activeResult: OUTPUT_ID,
        view: 'table',
      }
      set((s) => ({ tabs: [...s.tabs, tab], activeTab: id }))
      return id
    },

    openIndexTab(sessionId, ref) {
      if (ref.name) {
        const existing = get().tabs.find(
          (t) => t.kind === 'index' && t.sessionId === sessionId && t.index?.database === ref.database && t.index?.schema === ref.schema && t.index?.table === ref.table && t.index?.name === ref.name,
        )
        if (existing) {
          set({ activeTab: existing.id })
          return existing.id
        }
      }
      const id = uid('tab')
      const tab: Tab = {
        id,
        kind: 'index',
        index: ref,
        sessionId,
        database: ref.database,
        schema: ref.schema,
        title: ref.name || `New index · ${ref.table}`,
        script: '',
        results: [],
        docResults: [],
        output: [],
        activeResult: OUTPUT_ID,
        view: 'table',
      }
      set((s) => ({ tabs: [...s.tabs, tab], activeTab: id }))
      return id
    },

    openTransferTab(mode, sessionId, database, schema, opts = {}) {
      if (opts.jobId) {
        const existing = get().tabs.find((t) => t.transfer?.jobId === opts.jobId)
        if (existing) {
          set({ activeTab: existing.id })
          return existing.id
        }
      }
      const id = uid('tab')
      const tab: Tab = {
        id,
        kind: 'transfer',
        transfer: { mode, jobId: opts.jobId, tables: opts.tables },
        sessionId,
        database,
        schema,
        title: mode === 'export' ? 'Export' : 'Import',
        script: '',
        results: [],
        docResults: [],
        output: [],
        activeResult: OUTPUT_ID,
        view: 'table',
      }
      set((s) => ({ tabs: [...s.tabs, tab], activeTab: id }))
      return id
    },

    setJob(job) {
      set((s) => ({ jobs: { ...s.jobs, [job.id]: { ...job, receivedAt: Date.now() } } }))
    },

    openShellTab(sessionId, database, script = '', opts = {}) {
      const id = uid('tab')
      const tab: Tab = { id, kind: 'shell', sessionId, database, schema: '', title: opts.title, script, results: [], docResults: [], output: [], activeResult: OUTPUT_ID, view: 'tree' }
      set((s) => ({ tabs: [...s.tabs, tab], activeTab: id }))
      if (opts.run) void get().run(id, Mode.ModeAll, 0, 0, 0)
      return id
    },

    openDocIndexTab(sessionId, database, collection, name) {
      const existing = name
        ? get().tabs.find((t) => t.kind === 'docindex' && t.sessionId === sessionId && t.database === database && t.docIndex?.collection === collection && t.docIndex.name === name)
        : undefined
      if (existing) {
        set({ activeTab: existing.id })
        return existing.id
      }
      const id = uid('tab')
      const tab: Tab = {
        id,
        kind: 'docindex',
        docIndex: { collection, name },
        sessionId,
        database,
        schema: '',
        title: name || `New index · ${collection}`,
        script: '',
        results: [],
        docResults: [],
        output: [],
        activeResult: OUTPUT_ID,
        view: 'tree',
      }
      set((s) => ({ tabs: [...s.tabs, tab], activeTab: id }))
      return id
    },

    openDocTransferTab(mode, sessionId, database, opts = {}) {
      if (opts.jobId) {
        const existing = get().tabs.find((t) => t.docTransfer?.jobId === opts.jobId)
        if (existing) {
          set({ activeTab: existing.id })
          return existing.id
        }
      }
      const id = uid('tab')
      const tab: Tab = {
        id,
        kind: 'doctransfer',
        docTransfer: { mode, jobId: opts.jobId, collections: opts.collections },
        sessionId,
        database,
        schema: '',
        title: mode === 'export' ? 'Export' : 'Import',
        script: '',
        results: [],
        docResults: [],
        output: [],
        activeResult: OUTPUT_ID,
        view: 'tree',
      }
      set((s) => ({ tabs: [...s.tabs, tab], activeTab: id }))
      return id
    },

    setDocJob(job) {
      set((s) => ({ docJobs: { ...s.docJobs, [job.id]: { ...job, receivedAt: Date.now() } } }))
    },

    closeTab(id) {
      const t = get().tabs.find((x) => x.id === id)
      if (t?.running) get().cancel(id)
      if (!isShellKind(t?.kind)) void Queries.CloseTab(id).catch(() => {})
      if (t) useLibrary.getState().remember(t)
      set((s) => {
        const idx = s.tabs.findIndex((x) => x.id === id)
        const tabs = s.tabs.filter((x) => x.id !== id)
        let activeTab = s.activeTab
        if (activeTab === id) activeTab = tabs[Math.min(idx, tabs.length - 1)]?.id ?? ''
        return { tabs, activeTab }
      })
    },

    moveTab(id, overId) {
      set((s) => {
        const from = s.tabs.findIndex((t) => t.id === id)
        const to = s.tabs.findIndex((t) => t.id === overId)
        if (from < 0 || to < 0 || from === to) return {}
        const tabs = [...s.tabs]
        const [moved] = tabs.splice(from, 1)
        tabs.splice(to, 0, moved)
        return { tabs }
      })
    },

    setActiveTab(id) {
      set({ activeTab: id })
    },

    updateTab(id, patch) {
      patchTab(id, () => patch)
    },

    async run(tabId, mode, cursor, selStart, selEnd, explain = false) {
      const tab = get().tabs.find((t) => t.id === tabId)
      if (!tab || tab.running) return
      let session: SessionInfo | null = get().sessions[tab.sessionId] ?? null
      if (!session) session = await get().connect(tab.sessionId)
      if (!session) return
      if (tab.kind === 'shell') return runShell(tab, mode, cursor, selStart, selEnd)
      const opId = uid('run')
      const pageSize = get().pageSize
      patchTab(tabId, () => ({ running: true, runOpId: opId, retry: undefined }))
      try {
        const res = await Queries.Run(tab.sessionId, tabId, opId, {
          script: tab.script,
          mode,
          cursor,
          selStart,
          selEnd,
          database: tab.database,
          schema: tab.schema,
          partition: tab.partition ?? '',
          pageSize,
          explain,
        })
        const results = (res.results ?? []).map((r) => toResult(r, pageSize))
        const output = res.output ?? []
        const failed = results.find((r) => r.kind === 'error')
        const data = results.findLast((r) => r.kind === 'rows')
        patchTab(tabId, () => ({
          results,
          output,
          tx: res.tx,
          activeResult: failed?.id ?? data?.id ?? (results.length === 1 ? results[0].id : OUTPUT_ID),
          lastDurationMs: res.durationMs,
        }))
        if (results.length === 0 && output.length === 0) toast.success('Script executed', { description: 'Nothing to show.' })
        // Statements that change the schema refresh the metadata the editor completes.
        if (results.some((r) => /^(create|alter|drop|rename|comment)/i.test(r.statement))) {
          void get().loadCompletions(tab.sessionId, tab.database, tab.schema, true)
        }
      } catch (err) {
        const id = uid('res')
        patchTab(tabId, () => ({
          results: [{ id, kind: 'error', title: 'Error', line: 0, statement: '', columns: [], error: errorMessage(err), offset: 0, pageSize, rowsAffected: -1, durationMs: 0 }],
          activeResult: id,
        }))
      } finally {
        patchTab(tabId, () => ({ running: false, runOpId: undefined, retry: undefined }))
      }
    },

    cancel(tabId) {
      const tab = get().tabs.find((t) => t.id === tabId)
      if (!tab?.runOpId) return
      if (tab.kind === 'shell') void Shell.Cancel(tab.sessionId, tab.runOpId)
      else void Queries.Cancel(tab.sessionId, tab.runOpId)
    },

    async fetchPage(tabId, resultId, offset, pageSize) {
      const tab = get().tabs.find((t) => t.id === tabId)
      const r = tab?.results.find((x) => x.id === resultId)
      if (!tab || !r) return
      if (!get().sessions[tab.sessionId] && !(await get().connect(tab.sessionId))) return
      if (r.opId) void Queries.Cancel(tab.sessionId, r.opId)
      const size = pageSize ?? r.pageSize
      const opId = uid('page')
      patchResult(tabId, resultId, () => ({ loading: true, opId, error: undefined, retry: undefined }))
      try {
        const page = await Queries.FetchPage(tab.sessionId, tabId, resultId, opId, offset, size)
        patchResult(tabId, resultId, (cur) => ({
          page,
          offset,
          pageSize: size,
          count: page.total >= 0 ? page.total : cur.count,
          durationMs: page.durationMs,
        }))
      } catch (err) {
        patchResult(tabId, resultId, () => ({ error: errorMessage(err) }))
      } finally {
        patchResult(tabId, resultId, (cur) => (cur.opId === opId ? { loading: false, opId: undefined, retry: undefined } : {}))
      }
    },

    async count(tabId, resultId) {
      const tab = get().tabs.find((t) => t.id === tabId)
      if (!tab) return
      patchResult(tabId, resultId, () => ({ counting: true }))
      try {
        const n = await Queries.Count(tab.sessionId, tabId, resultId, uid('count'))
        patchResult(tabId, resultId, () => ({ count: n }))
      } catch (err) {
        toast.error('Count failed', { description: errorMessage(err) })
      } finally {
        patchResult(tabId, resultId, () => ({ counting: false }))
      }
    },

    async requery(tabId, resultId, where, orderBy, limit) {
      const tab = get().tabs.find((t) => t.id === tabId)
      const r = tab?.results.find((x) => x.id === resultId)
      if (!tab || !r) return
      if (!get().sessions[tab.sessionId] && !(await get().connect(tab.sessionId))) return
      const opId = uid('query')
      patchResult(tabId, resultId, () => ({ loading: true, opId, error: undefined, retry: undefined }))
      try {
        const res = await Queries.Requery(tab.sessionId, tabId, resultId, opId, where, orderBy, limit, r.pageSize)
        patchResult(tabId, resultId, () => ({ ...toResult(res, r.pageSize), title: r.title, line: r.line }))
      } catch (err) {
        patchResult(tabId, resultId, () => ({ error: errorMessage(err) }))
      } finally {
        patchResult(tabId, resultId, (cur) => (cur.opId === opId ? { loading: false, opId: undefined, retry: undefined } : {}))
      }
    },

    cancelResult(tabId, resultId) {
      const tab = get().tabs.find((t) => t.id === tabId)
      if (tab?.kind === 'shell') {
        const r = tab.docResults.find((x) => x.id === resultId)
        if (r?.opId) void Shell.Cancel(tab.sessionId, r.opId)
        return
      }
      const r = tab?.results.find((x) => x.id === resultId)
      if (tab && r?.opId) void Queries.Cancel(tab.sessionId, r.opId)
    },

    async fetchDocs(tabId, resultId, query, offset, pageSize) {
      const tab = get().tabs.find((t) => t.id === tabId)
      const r = tab?.docResults.find((x) => x.id === resultId)
      if (!tab || !r) return
      if (!get().sessions[tab.sessionId] && !(await get().connect(tab.sessionId))) return
      if (r.opId) void Shell.Cancel(tab.sessionId, r.opId)
      const size = pageSize ?? r.pageSize
      const opId = uid('page')
      const queryChanged = JSON.stringify(query) !== JSON.stringify(r.query)
      patchDocResult(tabId, resultId, () => ({ loading: true, opId, error: undefined, retry: undefined }))
      try {
        const page = await Shell.FetchPage(tab.sessionId, opId, query, offset, size)
        patchDocResult(tabId, resultId, (cur) => ({
          page,
          query,
          offset,
          pageSize: size,
          count: queryChanged ? undefined : cur.count,
          durationMs: page.durationMs,
        }))
      } catch (err) {
        patchDocResult(tabId, resultId, () => ({ error: errorMessage(err) }))
      } finally {
        patchDocResult(tabId, resultId, (cur) => (cur.opId === opId ? { loading: false, opId: undefined, retry: undefined } : {}))
      }
    },

    async countDocs(tabId, resultId) {
      const tab = get().tabs.find((t) => t.id === tabId)
      const r = tab?.docResults.find((x) => x.id === resultId)
      if (!tab || !r?.query) return
      patchDocResult(tabId, resultId, () => ({ counting: true }))
      try {
        const n = await Shell.Count(tab.sessionId, uid('count'), r.query)
        patchDocResult(tabId, resultId, () => ({ count: n }))
      } catch (err) {
        toast.error('Count failed', { description: errorMessage(err) })
      } finally {
        patchDocResult(tabId, resultId, () => ({ counting: false }))
      }
    },

    onRetry(opId, attempt, error) {
      const msg = `Network problem, retrying (${attempt})… ${error}`
      set((s) => ({
        tabs: s.tabs.map((t) =>
          t.runOpId === opId
            ? { ...t, retry: msg }
            : t.results.some((r) => r.opId === opId)
              ? { ...t, results: t.results.map((r) => (r.opId === opId ? { ...r, retry: msg } : r)) }
              : t.docResults.some((r) => r.opId === opId)
                ? { ...t, docResults: t.docResults.map((r) => (r.opId === opId ? { ...r, retry: msg } : r)) }
                : t,
        ),
      }))
    },

    async setManual(tabId, manual) {
      const tab = get().tabs.find((t) => t.id === tabId)
      if (!tab) return
      if (!get().sessions[tab.sessionId] && !(await get().connect(tab.sessionId))) return
      try {
        const tx = await Queries.SetManualTransaction(tab.sessionId, tabId, manual)
        patchTab(tabId, () => ({ tx }))
      } catch (err) {
        toast.error('Could not change the transaction mode', { description: errorMessage(err) })
      }
    },

    async commit(tabId) {
      const tab = get().tabs.find((t) => t.id === tabId)
      if (!tab) return
      try {
        const tx = await Queries.Commit(tab.sessionId, tabId)
        patchTab(tabId, () => ({ tx }))
        toast.success('Transaction committed')
      } catch (err) {
        patchTab(tabId, (t) => ({ tx: t.tx ? { ...t.tx, open: false, statements: 0 } : t.tx }))
        toast.error('Commit failed', { description: errorMessage(err) })
      }
    },

    async rollback(tabId) {
      const tab = get().tabs.find((t) => t.id === tabId)
      if (!tab) return
      try {
        const tx = await Queries.Rollback(tab.sessionId, tabId)
        patchTab(tabId, () => ({ tx }))
        toast('Transaction rolled back')
      } catch (err) {
        patchTab(tabId, (t) => ({ tx: t.tx ? { ...t.tx, open: false, statements: 0 } : t.tx }))
        toast.error('Rollback failed', { description: errorMessage(err) })
      }
    },
  }
})

export const OUTPUT_RESULT = OUTPUT_ID

// Persist open tabs (scripts only, results are transient).
let saveTimer: ReturnType<typeof setTimeout> | undefined
useStore.subscribe((s, prev) => {
  if (s.tabs === prev.tabs && s.activeTab === prev.activeTab && s.pageSize === prev.pageSize) return
  clearTimeout(saveTimer)
  saveTimer = setTimeout(() => {
    writeLocal('quire.tabs', {
      activeTab: s.activeTab,
      // Transfer tabs follow background jobs, which do not outlive the app.
      tabs: s.tabs.filter((t) => t.kind !== 'transfer' && t.kind !== 'doctransfer').map(({ id, kind, ddl, index, docIndex, sessionId, database, schema, partition, title, script, filePath, dirty, view, editorSize }) => ({
        id,
        kind,
        ddl,
        index,
        docIndex,
        sessionId,
        database,
        schema,
        partition,
        title,
        script,
        filePath,
        dirty,
        view,
        editorSize,
      })),
    })
    writeLocal('quire.pageSize', s.pageSize)
  }, 400)
})
