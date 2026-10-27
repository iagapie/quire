import { create } from 'zustand'
import type { Tab } from './store'
import { readLocal, uid, writeLocal } from './utils'

/** A script kept after its tab closed (history) or saved on purpose (favorites). */
export interface SavedScript {
  id: string
  /** Favorites only: the name the user gave it. */
  name?: string
  shell: boolean
  sessionId: string
  database: string
  schema: string
  title?: string
  script: string
  /** When it was closed or saved (ms). */
  at: number
}

const HISTORY_MAX = 100

interface LibraryState {
  history: SavedScript[]
  favorites: SavedScript[]
  /** Keeps the script of a closing editor tab; tabs without a script are not kept. */
  remember(t: Tab): void
  /** Saves a favorite and returns its id. */
  addFavorite(t: Tab | SavedScript, name?: string): string
  /** Saves edits of open tabs back to the favorites they were opened from. */
  sync(tabs: Tab[]): void
  rename(id: string, name: string): void
  remove(list: 'history' | 'favorites', id: string): void
  clearHistory(): void
}

/** Editor tabs only: DDL, index and transfer tabs reopen from the sidebar. */
const savable = (t: Tab) => t.kind === undefined || t.kind === 'sql' || t.kind === 'shell'

function fromTab(t: Tab | SavedScript): SavedScript {
  return {
    id: uid('saved'),
    shell: 'shell' in t ? t.shell : t.kind === 'shell',
    sessionId: t.sessionId,
    database: t.database,
    schema: t.schema,
    title: t.title,
    script: t.script,
    at: Date.now(),
  }
}

export const useLibrary = create<LibraryState>()((set, get) => ({
  history: readLocal<SavedScript[]>('quire.history', []),
  favorites: readLocal<SavedScript[]>('quire.favorites', []),

  remember(t) {
    if (!savable(t) || !t.script.trim()) return
    const entry = fromTab(t)
    set((s) => ({
      // The same script closed again moves to the top instead of repeating.
      history: [entry, ...s.history.filter((h) => !(h.sessionId === entry.sessionId && h.database === entry.database && h.script === entry.script))].slice(0, HISTORY_MAX),
    }))
  },

  addFavorite(t, name) {
    const fav = { ...fromTab(t), name: name?.trim() || undefined }
    set((s) => ({ favorites: [fav, ...s.favorites] }))
    return fav.id
  },

  sync(tabs) {
    const open = new Map(tabs.filter((t) => t.favoriteId).map((t) => [t.favoriteId, t]))
    if (open.size === 0) return
    let changed = false
    const favorites = get().favorites.map((f) => {
      const t = open.get(f.id)
      if (!t || (t.script === f.script && t.database === f.database && t.schema === f.schema)) return f
      changed = true
      return { ...f, script: t.script, database: t.database, schema: t.schema, at: Date.now() }
    })
    if (changed) set({ favorites })
  },

  rename(id, name) {
    set((s) => ({ favorites: s.favorites.map((f) => (f.id === id ? { ...f, name: name.trim() || undefined } : f)) }))
  },

  remove(list, id) {
    set((s) => ({ [list]: s[list].filter((x) => x.id !== id) }) as Partial<LibraryState>)
  },

  clearHistory() {
    set({ history: [] })
  },
}))

useLibrary.subscribe((s, prev) => {
  if (s.history !== prev.history) writeLocal('quire.history', s.history)
  if (s.favorites !== prev.favorites) writeLocal('quire.favorites', s.favorites)
})

/** The name shown for a saved script. */
export function savedTitle(x: SavedScript) {
  return x.name || x.title || x.script.trim().split('\n')[0].slice(0, 80)
}
