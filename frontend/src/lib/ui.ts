import { create } from 'zustand'
import { toast } from 'sonner'
import { Connections, errorMessage, type DbObject } from './api'
import { openShell } from './shell/ui'
import { useStore } from './store'
import { isMac, readLocal, writeLocal } from './utils'

interface UIState {
  connectionDialog: { open: boolean; profileId?: string }
  openConnectionDialog(profileId?: string): void
  closeConnectionDialog(): void
}

export const useUI = create<UIState>()((set) => ({
  connectionDialog: { open: false },
  openConnectionDialog: (profileId) => set({ connectionDialog: { open: true, profileId } }),
  closeConnectionDialog: () => set({ connectionDialog: { open: false } }),
}))

const SIDEBAR_COLLAPSED = 'quire.layout.sidebarCollapsed'

interface LayoutState {
  sidebarCollapsed: boolean
  /** In full screen macOS hides the window buttons, so their space is not reserved. */
  fullscreen: boolean
  /** Set by the layout once the sidebar panel exists; collapses or expands it. */
  setSidebar: (collapsed: boolean) => void
  /** Called by the layout when the panel collapses or expands (also by dragging). */
  sidebarChanged(collapsed: boolean): void
}

export const useLayout = create<LayoutState>()((set) => ({
  sidebarCollapsed: readLocal(SIDEBAR_COLLAPSED, false),
  fullscreen: false,
  setSidebar: () => {},
  sidebarChanged: (collapsed) =>
    set((s) => {
      if (s.sidebarCollapsed === collapsed) return {}
      writeLocal(SIDEBAR_COLLAPSED, collapsed)
      return { sidebarCollapsed: collapsed }
    }),
}))

/** Whether the macOS window buttons sit over the top-left corner (hidden-inset title bar, not full screen). */
export const useTrafficLights = () => useLayout((s) => isMac && !s.fullscreen)

let lastToggle = 0

/** Shows or hides the sidebar. The menu accelerator and the keyboard handler may both fire for one press. */
export function toggleSidebar() {
  const now = Date.now()
  if (now - lastToggle < 250) return
  lastToggle = now
  const l = useLayout.getState()
  l.setSidebar(!l.sidebarCollapsed)
}

/** The database and schema a new editor starts in. */
function defaults(profileId: string): { database: string; schema: string } {
  const s = useStore.getState()
  const session = s.sessions[profileId]
  const profile = s.profiles.find((p) => p.id === profileId)
  return {
    database: session?.database || profile?.database || s.databases[profileId]?.items?.[0] || '',
    schema: session?.schema || '',
  }
}

/** Whether a connection runs mongo shell scripts instead of SQL (MongoDB, DocumentDB). */
export function isShellProfile(profileId: string): boolean {
  const s = useStore.getState()
  const driver = s.profiles.find((p) => p.id === profileId)?.driver ?? ''
  return !!s.drivers[driver]?.shell
}

/** Opens a new editor tab, connecting first when needed: a SQL editor, or a mongo shell
 * for MongoDB connections. Returns the tab id, or nothing when the connection failed. */
export async function openEditor(profileId: string, database?: string, schema?: string, script = '', run = false, title?: string) {
  if (isShellProfile(profileId)) return openShell(profileId, database, script, run, title)
  const s = useStore.getState()
  const session = await s.connect(profileId)
  if (!session) return
  const d = defaults(profileId)
  return s.openTab(profileId, database ?? d.database, schema ?? d.schema, script, { run, title })
}

/** Opens a table in a new tab and shows its rows. */
export async function openTable(profileId: string, database: string, schema: string, table: string) {
  const s = useStore.getState()
  if (!(await s.connect(profileId))) return
  try {
    const sql = await Connections.TableQuery(profileId, database, schema, table)
    s.openTab(profileId, database, schema, sql, { run: true, title: table })
  } catch (err) {
    notifyError('Could not open the table', err)
  }
}

/** Opens a tab with the definition of an object. */
export async function openDDL(profileId: string, database: string, schema: string, object: DbObject) {
  const s = useStore.getState()
  if (!(await s.connect(profileId))) return
  try {
    const ddl = await Connections.GetDDL(profileId, database, schema, object)
    s.openDdlTab(profileId, { database, schema, object }, ddl)
  } catch (err) {
    notifyError('Could not read the definition', err)
  }
}

/** Opens an editor with a generated statement for a table. */
export async function generateSQL(profileId: string, database: string, schema: string, table: string, kind: 'select' | 'insert' | 'update' | 'delete') {
  const s = useStore.getState()
  if (!(await s.connect(profileId))) return
  try {
    const sql = await Connections.GenerateSQL(profileId, database, schema, table, kind)
    s.openTab(profileId, database, schema, sql, { title: `${kind.toUpperCase()} ${table}` })
  } catch (err) {
    notifyError('Could not generate the statement', err)
  }
}

export function notifyError(title: string, err: unknown) {
  toast.error(title, { description: errorMessage(err) })
}

/** Opens the index editor of a table: the index name, or a new index. */
export async function openIndex(profileId: string, database: string, schema: string, table: string, name = '') {
  const s = useStore.getState()
  if (!(await s.connect(profileId))) return
  s.openIndexTab(profileId, { database, schema, table, name })
}

/** Opens the export setup of a schema (all its tables when none are given). */
export async function openExport(profileId: string, database: string, schema: string, tables?: string[]) {
  const s = useStore.getState()
  if (!(await s.connect(profileId))) return
  s.openTransferTab('export', profileId, database, schema, { tables })
}

/** Opens the import setup of a schema; a table becomes the target of every file. */
export async function openImport(profileId: string, database: string, schema: string, table?: string) {
  const s = useStore.getState()
  if (!(await s.connect(profileId))) return
  s.openTransferTab('import', profileId, database, schema, { tables: table ? [table] : undefined })
}
