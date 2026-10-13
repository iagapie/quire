// Column widths the user set by hand, remembered per table (connection / database /
// schema / table) and keyed by column name, so they survive reruns and restarts.
import { readLocal, writeLocal } from './utils'

const KEY = 'quire.colWidths'
/** Tables remembered; the least recently changed are dropped first. */
const MAX_TABLES = 300

type Store = Record<string, Record<string, number>>

let cache: Store | null = null
const load = (): Store => (cache ??= readLocal<Store>(KEY, {}))

/** The key widths are stored under, or '' when the result is not a single table. */
export function widthKey(connection: string, database: string, schema: string, table: string): string {
  return table ? [connection, database, schema, table].join('\u0000') : ''
}

export function savedWidths(key: string): Record<string, number> {
  return (key && load()[key]) || {}
}

/** Sets (or, with null, forgets) the width of a column. */
export function saveWidth(key: string, column: string, width: number | null) {
  if (!key) return
  const all = load()
  const cur = { ...all[key] }
  if (width === null) delete cur[column]
  else cur[column] = Math.round(width)
  // Re-inserting keeps the most recently changed tables last.
  delete all[key]
  if (Object.keys(cur).length > 0) all[key] = cur
  const keys = Object.keys(all)
  for (const k of keys.slice(0, Math.max(0, keys.length - MAX_TABLES))) delete all[k]
  writeLocal(KEY, all)
}

/** Test hook: forget the in-memory copy. */
export function resetWidthCache() {
  cache = null
}

const VIEW_KEY = 'quire.viewWidths'

/** Widths of the fixed columns of a view (the tree's key column…), the same for every table. */
export function viewWidth(name: string): number | undefined {
  return readLocal<Record<string, number>>(VIEW_KEY, {})[name]
}

export function saveViewWidth(name: string, width: number | null) {
  const all = readLocal<Record<string, number>>(VIEW_KEY, {})
  if (width === null) delete all[name]
  else all[name] = Math.round(width)
  writeLocal(VIEW_KEY, all)
}
