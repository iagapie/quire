import { useStore } from '../store'

/** The database a new shell starts in. */
function defaultDb(profileId: string): string {
  const s = useStore.getState()
  const session = s.sessions[profileId]
  const profile = s.profiles.find((p) => p.id === profileId)
  return session?.database || profile?.database || s.databases[profileId]?.items?.find((d) => d !== 'admin') || 'test'
}

/** Opens a new mongo shell tab, connecting first when needed. */
export async function openShell(profileId: string, db?: string, script = '', run = false, title?: string) {
  const s = useStore.getState()
  if (!(await s.connect(profileId))) return
  s.openShellTab(profileId, db || defaultDb(profileId), script, { run, title })
}

export function collectionScript(coll: string) {
  return `db.getCollection(${JSON.stringify(coll)}).find({})\n`
}

/** Opens a collection in a new shell tab and shows its documents. */
export async function openCollection(profileId: string, db: string, coll: string) {
  await openShell(profileId, db, collectionScript(coll), true, coll)
}

/** Opens the index editor of a collection: the index name, or a new index. */
export async function openDocIndex(profileId: string, db: string, coll: string, name?: string) {
  const s = useStore.getState()
  if (!(await s.connect(profileId))) return
  s.openDocIndexTab(profileId, db, coll, name)
}

/** Opens the export setup for collections of a database (all of them when none are given). */
export async function openDocExport(profileId: string, db: string, collections?: string[]) {
  const s = useStore.getState()
  if (!(await s.connect(profileId))) return
  s.openDocTransferTab('export', profileId, db, { collections })
}

/** Opens the import setup for a database; a collection makes it the target of every file. */
export async function openDocImport(profileId: string, db: string, collection?: string) {
  const s = useStore.getState()
  if (!(await s.connect(profileId))) return
  s.openDocTransferTab('import', profileId, db, { collections: collection ? [collection] : undefined })
}
