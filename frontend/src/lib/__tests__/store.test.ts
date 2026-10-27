import { beforeEach, describe, expect, it } from 'vitest'
import { isShellKind, useStore } from '../store'
import { savedTitle, useLibrary } from '../library'

const ids = () => useStore.getState().tabs.map((t) => t.title)

describe('moveTab', () => {
  beforeEach(() => {
    useStore.setState({ tabs: [], activeTab: '' })
    for (const title of ['a', 'b', 'c', 'd']) useStore.getState().openTab('s', 'db', '', '', { title })
  })

  const idOf = (title: string) => useStore.getState().tabs.find((t) => t.title === title)!.id

  it('moves a tab right', () => {
    useStore.getState().moveTab(idOf('a'), idOf('c'))
    expect(ids()).toEqual(['b', 'c', 'a', 'd'])
  })

  it('moves a tab left', () => {
    useStore.getState().moveTab(idOf('d'), idOf('a'))
    expect(ids()).toEqual(['d', 'a', 'b', 'c'])
  })

  it('keeps the active tab and ignores unknown ids', () => {
    const active = useStore.getState().activeTab
    useStore.getState().moveTab(idOf('b'), 'missing')
    useStore.getState().moveTab(idOf('b'), idOf('b'))
    expect(ids()).toEqual(['a', 'b', 'c', 'd'])
    useStore.getState().moveTab(idOf('a'), idOf('b'))
    expect(useStore.getState().activeTab).toBe(active)
  })
})

describe('shell tabs', () => {
  beforeEach(() => useStore.setState({ tabs: [], activeTab: '' }))

  it('opens mongo shell tabs with document results', () => {
    const id = useStore.getState().openShellTab('s', 'shop', 'db.orders.find()', { title: 'orders' })
    const tab = useStore.getState().tabs.find((t) => t.id === id)!
    expect(tab.kind).toBe('shell')
    expect(tab.view).toBe('tree')
    expect(tab.docResults).toEqual([])
    expect(isShellKind(tab.kind)).toBe(true)
    expect(isShellKind('sql')).toBe(false)
  })

  it('focuses an index tab that is already open', () => {
    const s = useStore.getState()
    const a = s.openDocIndexTab('s', 'shop', 'orders', 'sku_1')
    s.openShellTab('s', 'shop')
    expect(useStore.getState().openDocIndexTab('s', 'shop', 'orders', 'sku_1')).toBe(a)
    expect(useStore.getState().activeTab).toBe(a)
    // A new index always gets its own tab.
    expect(useStore.getState().openDocIndexTab('s', 'shop', 'orders')).not.toBe(a)
  })

  it('focuses the tab of a running collection export', () => {
    const s = useStore.getState()
    const a = s.openDocTransferTab('export', 's', 'shop', { jobId: 'doc-export-1' })
    expect(useStore.getState().openDocTransferTab('export', 's', 'shop', { jobId: 'doc-export-1' })).toBe(a)
    expect(useStore.getState().tabs.find((t) => t.id === a)?.docTransfer).toEqual({ mode: 'export', jobId: 'doc-export-1', collections: undefined })
  })
})

describe('closed tab history', () => {
  beforeEach(() => {
    useStore.setState({ tabs: [], activeTab: '' })
    useLibrary.setState({ history: [], favorites: [] })
  })

  it('keeps scripts of closed editors, newest first, without repeats', () => {
    const s = useStore.getState()
    const a = s.openShellTab('s', 'shop', 'db.a.find()')
    const b = s.openShellTab('s', 'shop', 'db.b.find()')
    const again = s.openShellTab('s', 'shop', 'db.a.find()')
    const empty = s.openShellTab('s', 'shop', '  ')
    const index = s.openDocIndexTab('s', 'shop', 'orders')
    for (const id of [a, b, again, empty, index]) useStore.getState().closeTab(id)
    expect(useLibrary.getState().history.map((h) => [h.script, h.shell])).toEqual([
      ['db.a.find()', true],
      ['db.b.find()', true],
    ])
  })

  it('saves favorites with an optional name', () => {
    const id = useStore.getState().openShellTab('s', 'shop', 'db.a.find()', { title: 'a' })
    const tab = useStore.getState().tabs.find((t) => t.id === id)!
    useLibrary.getState().addFavorite(tab, '  ')
    useLibrary.getState().addFavorite(tab, 'Orders')
    const [named, unnamed] = useLibrary.getState().favorites
    expect([named.name, savedTitle(named), unnamed.name, savedTitle(unnamed)]).toEqual(['Orders', 'Orders', undefined, 'a'])
  })
})
