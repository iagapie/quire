import { useState } from 'react'
import { create } from 'zustand'
import { History, Pencil, Star, Trash2 } from 'lucide-react'
import { savedTitle, useLibrary, type SavedScript } from '../lib/library'
import { useStore, type Tab } from '../lib/store'
import { openEditor } from '../lib/ui'
import { cn } from '../lib/utils'
import { Button, IconButton, Input, Modal } from './ui'
import { DriverIcon } from './DriverIcon'

type View = 'favorites' | 'history'

const useLibraryUI = create<{
  view: View | null
  /** The favorite being named: a new one from a tab or history entry, or a rename. */
  naming: { from?: Tab | SavedScript; renameId?: string; name: string } | null
}>(() => ({ view: null, naming: null }))

export const openLibrary = (view: View) => useLibraryUI.setState({ view })

/** Asks for an optional name, then saves the script as a favorite. */
export const saveFavorite = (from: Tab | SavedScript) => useLibraryUI.setState({ naming: { from, name: from.title ?? '' } })

/** Reopens a saved script in a new editor tab. A favorite's tab keeps it up to date, so
 * a favorite that is already open is focused instead. */
export async function reopen(x: SavedScript, favorite = false) {
  const s = useStore.getState()
  const open = favorite && s.tabs.find((t) => t.favoriteId === x.id)
  if (open) return s.setActiveTab(open.id)
  const id = await openEditor(x.sessionId, x.database, x.shell ? undefined : x.schema, x.script, false, x.name || x.title)
  if (id && favorite) s.updateTab(id, { favoriteId: x.id })
}

/** The favorite an open tab edits, if it still exists. */
export const useFavoriteOf = (t: Tab) => useLibrary((s) => (t.favoriteId ? s.favorites.find((f) => f.id === t.favoriteId) : undefined))

/** Removes a tab's favorite; the tab stays open as a plain editor. */
export function unfavorite(t: Tab) {
  if (t.favoriteId) useLibrary.getState().remove('favorites', t.favoriteId)
  useStore.getState().updateTab(t.id, { favoriteId: undefined })
}

export function LibraryHost() {
  const { view, naming } = useLibraryUI()
  const lib = useLibrary()
  const profiles = useStore((s) => s.profiles)
  const [filter, setFilter] = useState('')
  const items = (view === 'history' ? lib.history : lib.favorites).filter((x) => {
    const q = filter.trim().toLowerCase()
    return !q || savedTitle(x).toLowerCase().includes(q) || x.script.toLowerCase().includes(q)
  })
  const closeNaming = () => useLibraryUI.setState({ naming: null })
  const submitName = () => {
    if (!naming) return
    if (naming.renameId) lib.rename(naming.renameId, naming.name)
    else if (naming.from) {
      const id = lib.addFavorite(naming.from, naming.name)
      // A tab saved as a favorite keeps editing it.
      if ('results' in naming.from) useStore.getState().updateTab(naming.from.id, { favoriteId: id })
    }
    closeNaming()
  }

  return (
    <>
      <Modal
        open={!!view}
        onOpenChange={(o) => !o && useLibraryUI.setState({ view: null })}
        title={
          <span className="flex gap-1">
            {(['favorites', 'history'] as const).map((v) => (
              <button
                key={v}
                onClick={() => useLibraryUI.setState({ view: v })}
                className={cn('flex items-center gap-1.5 rounded-md px-2 py-0.5', v === view ? 'bg-active text-fg' : 'font-normal text-muted hover:text-fg')}
              >
                {v === 'favorites' ? <Star size={13} /> : <History size={13} />}
                {v === 'favorites' ? 'Favorites' : 'Closed tabs'}
              </button>
            ))}
          </span>
        }
        width="max-w-2xl"
        footer={
          view === 'history' &&
          lib.history.length > 0 && (
            <Button variant="ghost" onClick={lib.clearHistory}>
              Clear history
            </Button>
          )
        }
      >
        <div className="flex flex-col gap-2 p-3">
          <Input data-autofocus value={filter} onChange={(e) => setFilter(e.target.value)} placeholder="Filter by name or script" />
          {items.length === 0 && (
            <p className="py-8 text-center text-[13px] text-muted">
              {filter ? 'Nothing matches.' : view === 'history' ? 'Closed editor tabs show up here.' : 'Right-click a tab and choose Add to favorites.'}
            </p>
          )}
          {items.map((x) => {
            const profile = profiles.find((p) => p.id === x.sessionId)
            return (
              <div key={x.id} className="group flex items-center gap-2 rounded-md hover:bg-hover">
                <button
                  className="flex min-w-0 flex-1 items-center gap-3 px-2 py-1.5 text-left"
                  onClick={() => {
                    useLibraryUI.setState({ view: null })
                    void reopen(x, view === 'favorites')
                  }}
                >
                  {profile ? <DriverIcon driver={profile.driver} size={14} /> : <span className="size-3.5" />}
                  <span className="min-w-0 flex-1">
                    <span className="block truncate text-[13px]">{savedTitle(x)}</span>
                    <span className="block truncate font-mono text-[11px] text-faint">{x.script.trim().replace(/\s+/g, ' ')}</span>
                  </span>
                  <span className="shrink-0 text-right text-[11px] text-faint">
                    <span className="block">{profile?.name ?? 'Deleted connection'}{x.database && ` · ${x.database}`}</span>
                    <span className="block">{new Date(x.at).toLocaleString()}</span>
                  </span>
                </button>
                <span className="flex shrink-0 pr-1 opacity-0 group-hover:opacity-100 focus-within:opacity-100">
                  {view === 'history' ? (
                    <IconButton label="Add to favorites" onClick={() => saveFavorite(x)}>
                      <Star size={13} />
                    </IconButton>
                  ) : (
                    <IconButton label="Rename" onClick={() => useLibraryUI.setState({ naming: { renameId: x.id, name: x.name ?? '' } })}>
                      <Pencil size={13} />
                    </IconButton>
                  )}
                  <IconButton label="Remove" tone="danger" onClick={() => lib.remove(view ?? 'history', x.id)}>
                    <Trash2 size={13} />
                  </IconButton>
                </span>
              </div>
            )
          })}
        </div>
      </Modal>
      <Modal
        open={!!naming}
        onOpenChange={(o) => !o && closeNaming()}
        title={naming?.renameId ? 'Rename favorite' : 'Add to favorites'}
        width="max-w-md"
        footer={
          <>
            <Button onClick={closeNaming}>Cancel</Button>
            <Button variant="primary" onClick={submitName}>
              Save
            </Button>
          </>
        }
      >
        <form
          className="px-5 py-4"
          onSubmit={(e) => {
            e.preventDefault()
            submitName()
          }}
        >
          <Input data-autofocus onFocus={(e) => e.currentTarget.select()} value={naming?.name ?? ''} onChange={(e) => naming && useLibraryUI.setState({ naming: { ...naming, name: e.target.value } })} placeholder="Name (optional)" />
        </form>
      </Modal>
    </>
  )
}
