import { useEffect, useMemo } from 'react'
import { Copy, Database, Download, Eye, FolderClosed, KeyRound, ListTree, Pencil, Plus, RefreshCw, SquareTerminal, Table2, Trash2, Upload, X } from 'lucide-react'
import { toast } from 'sonner'
import { Shell, errorMessage } from '../../lib/shell/api'
import { keys, useStore } from '../../lib/store'
import { collectionScript, openCollection, openDocExport, openDocImport, openDocIndex, openShell } from '../../lib/shell/ui'
import { cn, copyText, isMac } from '../../lib/utils'
import { confirmAction } from '../Confirm'
import { Spinner, type MenuEntry } from '../ui'
import { Message, Row, markedIn, same, useSel } from '../tree'

// The tree of a MongoDB or DocumentDB connection: databases, their collections and
// views, and the indexes of each collection. Selections use the schema "".

/** Collections of a database in tree order: collections first, then views. */
function treeOrder(sessionId: string, db: string): string[] {
  const all = useStore.getState().collections[keys.db(sessionId, db)]?.items ?? []
  return [...all.filter((c) => c.type !== 'view'), ...all.filter((c) => c.type === 'view')].map((c) => c.name)
}

function pickCollection(sessionId: string, db: string, coll: string, k: string, e: React.MouseEvent) {
  const { marked, anchor, mark } = useSel.getState()
  const cur = same(marked, sessionId, db, '') ? marked!.tables : null
  if (e.shiftKey && cur && anchor) {
    const order = treeOrder(sessionId, db)
    const a = order.indexOf(anchor)
    const b = order.indexOf(coll)
    if (a >= 0 && b >= 0) {
      mark({ sessionId, db, schema: '', tables: order.slice(Math.min(a, b), Math.max(a, b) + 1) }, anchor, k)
      return
    }
  }
  if (e.metaKey || e.ctrlKey) {
    const list = cur ?? []
    if (list.includes(coll)) mark({ sessionId, db, schema: '', tables: list.filter((c) => c !== coll) }, coll, '')
    else mark({ sessionId, db, schema: '', tables: [...list, coll] }, coll, k)
    return
  }
  mark({ sessionId, db, schema: '', tables: [coll] }, coll, k)
}

async function markAll(sessionId: string, db: string) {
  const s = useStore.getState()
  if (!s.collections[keys.db(sessionId, db)]?.items) await s.loadCollections(sessionId, db)
  s.toggle(`d:${sessionId}:${db}`, true)
  const colls = treeOrder(sessionId, db).filter((c) => !c.startsWith('system.'))
  if (colls.length) useSel.getState().mark({ sessionId, db, schema: '', tables: colls }, colls[0])
}

export function ShellDatabaseNode({ sessionId, db, filter }: { sessionId: string; db: string; filter: string }) {
  const k = `d:${sessionId}:${db}`
  const node = useStore((s) => s.collections[keys.db(sessionId, db)])
  const open = useStore((s) => !!s.expanded[k]) || (!!filter && !!node?.items)
  const current = useStore((s) => s.sessions[sessionId]?.database === db)
  const readOnly = useStore((s) => s.sessions[sessionId]?.readOnly)
  const { toggle, loadCollections } = useStore.getState()

  useEffect(() => {
    if (open && !node) void loadCollections(sessionId, db)
  }, [open, node, sessionId, db, loadCollections])

  const colls = useMemo(() => {
    const all = node?.items ?? []
    return filter ? all.filter((c) => c.name.toLowerCase().includes(filter)) : all
  }, [node, filter])

  if (filter && !db.toLowerCase().includes(filter) && colls.length === 0) return null
  const collections = colls.filter((c) => c.type !== 'view')
  const views = colls.filter((c) => c.type === 'view')

  return (
    <div role="group">
      <Row
        depth={1}
        expandable
        open={open}
        onToggle={() => toggle(k)}
        k={k}
        icon={<Database size={14} className={cn(current && 'text-accent')} />}
        label={db}
        onSelectAll={() => markAll(sessionId, db)}
        menu={[
          { label: 'Open shell', icon: <SquareTerminal size={14} />, onSelect: () => openShell(sessionId, db) },
          { label: 'Refresh', icon: <RefreshCw size={14} />, onSelect: () => loadCollections(sessionId, db) },
          { separator: true },
          {
            label: 'Export collections…',
            icon: <Download size={14} />,
            onSelect: () => openDocExport(sessionId, db, markedIn(useSel.getState().marked, sessionId, db, '') ?? undefined),
          },
          { label: 'Import collections…', icon: <Upload size={14} />, hidden: readOnly, onSelect: () => openDocImport(sessionId, db) },
          { separator: true },
          { label: 'Copy name', icon: <Copy size={14} />, onSelect: () => copyText(db) },
        ]}
      />
      {open && (
        <>
          {node?.loading && <Message depth={2}>Loading collections…</Message>}
          {node?.error && (
            <Message depth={2} tone="error" onRetry={() => loadCollections(sessionId, db)}>
              {node.error}
            </Message>
          )}
          {node?.items && (
            <>
              <Folder sessionId={sessionId} db={db} kind="collections" label="Collections" items={collections} filter={filter} />
              {views.length > 0 && <Folder sessionId={sessionId} db={db} kind="views" label="Views" items={views} filter={filter} />}
            </>
          )}
        </>
      )}
    </div>
  )
}

function Folder({ sessionId, db, kind, label, items, filter }: { sessionId: string; db: string; kind: string; label: string; items: { name: string; type: string }[]; filter: string }) {
  const k = `f:${sessionId}:${db}::${kind}`
  const stored = useStore((s) => s.expanded[k])
  const open = (stored ?? true) || !!filter
  const toggle = useStore((s) => s.toggle)
  return (
    <div role="group">
      <Row
        depth={2}
        expandable
        open={open}
        onToggle={() => toggle(k, !open)}
        k={k}
        icon={<FolderClosed size={14} />}
        label={label}
        onSelectAll={() => markAll(sessionId, db)}
        muted
        trailing={<span className="text-[11px] text-faint tabular-nums">{items.length}</span>}
      />
      {open && items.map((c) => <CollectionNode key={c.name} sessionId={sessionId} db={db} coll={c.name} view={c.type === 'view'} />)}
    </div>
  )
}

function CollectionNode({ sessionId, db, coll, view }: { sessionId: string; db: string; coll: string; view: boolean }) {
  const k = `k:${sessionId}:${db}:${coll}`
  const open = useStore((s) => !!s.expanded[k])
  const idx = useStore((s) => s.docIndexes[keys.coll(sessionId, db, coll)])
  const readOnly = useStore((s) => s.sessions[sessionId]?.readOnly)
  const { toggle, loadDocIndexes, loadCollections } = useStore.getState()

  useEffect(() => {
    if (open && !idx && !view) void loadDocIndexes(sessionId, db, coll)
  }, [open, idx, view, sessionId, db, coll, loadDocIndexes])

  const ik = `i:${sessionId}:${db}:${coll}`
  const indexesOpen = useStore((s) => s.expanded[ik] ?? false)
  const multi = useSel((s) => markedIn(s.marked, sessionId, db, ''))
  const isMarked = !!multi?.includes(coll)

  const drop = async () => {
    const ok = await confirmAction({
      title: view ? 'Drop view?' : 'Drop collection?',
      message: (
        <>
          <b className="text-fg">
            {db}.{coll}
          </b>{' '}
          {view ? 'will be dropped.' : 'and all its documents and indexes will be dropped.'} It cannot be undone.
        </>
      ),
      confirmLabel: 'Drop',
      danger: true,
    })
    if (!ok) return
    try {
      await Shell.DropCollection(sessionId, db, coll)
      toast.success(`${coll} dropped`)
      void loadCollections(sessionId, db)
    } catch (err) {
      toast.error('Drop failed', { description: errorMessage(err) })
    }
  }

  const multiMenu: MenuEntry[] = [
    { label: `Export ${multi?.length} collections…`, icon: <Download size={14} />, onSelect: () => openDocExport(sessionId, db, multi ?? [coll]) },
    { separator: true },
    { label: 'Copy names', icon: <Copy size={14} />, onSelect: () => copyText((multi ?? []).join('\n')) },
    { label: 'Clear selection', icon: <X size={14} />, onSelect: () => useSel.getState().mark(null) },
  ]

  return (
    <div role="group">
      <Row
        depth={3}
        expandable={!view}
        open={open}
        onToggle={() => toggle(k)}
        onOpen={() => openCollection(sessionId, db, coll)}
        k={k}
        icon={view ? <Eye size={14} /> : <Table2 size={14} />}
        label={coll}
        title={`${db}.${coll} — double-click to open, ${isMac ? '⌘' : 'Ctrl'}-click to select several`}
        marked={isMarked}
        onPick={(e) => pickCollection(sessionId, db, coll, k, e)}
        onSelectAll={() => markAll(sessionId, db)}
        onContext={() => {
          if (!isMarked) useSel.getState().mark({ sessionId, db, schema: '', tables: [coll] }, coll, k)
        }}
        menu={
          isMarked && multi
            ? multiMenu
            : [
                { label: 'Open in new tab', icon: <SquareTerminal size={14} />, shortcut: '↵', onSelect: () => openCollection(sessionId, db, coll) },
                { label: 'Open shell with query', icon: <Pencil size={14} />, onSelect: () => openShell(sessionId, db, collectionScript(coll)) },
                { label: 'Refresh indexes', icon: <RefreshCw size={14} />, hidden: view, onSelect: () => loadDocIndexes(sessionId, db, coll) },
                { label: 'New index…', icon: <KeyRound size={14} />, hidden: view || readOnly, onSelect: () => openDocIndex(sessionId, db, coll) },
                { separator: true },
                { label: 'Export collection…', icon: <Download size={14} />, onSelect: () => openDocExport(sessionId, db, [coll]) },
                { label: 'Import into collection…', icon: <Upload size={14} />, hidden: view || readOnly, onSelect: () => openDocImport(sessionId, db, coll) },
                { separator: true },
                { label: 'Copy name', icon: <Copy size={14} />, onSelect: () => copyText(coll) },
                { label: 'Copy namespace', icon: <Copy size={14} />, onSelect: () => copyText(`${db}.${coll}`) },
                { hidden: readOnly, separator: true },
                {
                  hidden: readOnly || view,
                  label: 'Insert document…',
                  icon: <Plus size={14} />,
                  onSelect: () => openShell(sessionId, db, `db.getCollection(${JSON.stringify(coll)}).insertOne({\n  \n})\n`),
                },
                { label: 'Drop…', icon: <Trash2 size={14} />, danger: true, hidden: readOnly, onSelect: drop },
              ]
        }
      />
      {open && !view && (
        <div role="group">
          <Row
            depth={4}
            expandable
            open={indexesOpen}
            onToggle={() => toggle(ik)}
            k={ik}
            icon={<ListTree size={14} />}
            label="Indexes"
            muted
            menu={[
              { label: 'New index…', icon: <Plus size={14} />, hidden: readOnly, onSelect: () => openDocIndex(sessionId, db, coll) },
              { label: 'Refresh', icon: <RefreshCw size={14} />, onSelect: () => loadDocIndexes(sessionId, db, coll) },
            ]}
            trailing={idx?.loading ? <Spinner size={11} /> : <span className="text-[11px] text-faint tabular-nums">{idx?.items?.length ?? ''}</span>}
          />
          {indexesOpen && idx?.error && (
            <Message depth={5} tone="error" onRetry={() => loadDocIndexes(sessionId, db, coll)}>
              {idx.error}
            </Message>
          )}
          {indexesOpen &&
            idx?.items?.map((ix) => (
              <Row
                key={ix.name}
                depth={5}
                onOpen={() => openDocIndex(sessionId, db, coll, ix.name)}
                k={`x:${sessionId}:${db}:${coll}:${ix.name}`}
                icon={<KeyRound size={13} className={cn(ix.name === '_id_' && 'text-warn')} />}
                label={
                  <span>
                    {ix.name}
                    <span className="ml-2 font-mono text-[11px] text-faint">{ix.keys}</span>
                  </span>
                }
                title={`${ix.keys}${ix.unique ? ' · unique' : ''}${ix.sparse ? ' · sparse' : ''}${ix.ttl != null ? ` · TTL ${ix.ttl}s` : ''}`}
                trailing={ix.unique ? <span className="rounded bg-sunken px-1 text-[10px] text-faint">unique</span> : undefined}
                menu={[
                  {
                    label: readOnly || ix.name === '_id_' ? 'View index' : 'Edit index…',
                    icon: <Pencil size={14} />,
                    shortcut: '↵',
                    onSelect: () => openDocIndex(sessionId, db, coll, ix.name),
                  },
                  { separator: true },
                  { label: 'Copy definition', icon: <Copy size={14} />, onSelect: () => copyText(ix.keys) },
                  {
                    label: 'Copy createIndex()',
                    icon: <Copy size={14} />,
                    onSelect: () =>
                      copyText(`db.getCollection(${JSON.stringify(coll)}).createIndex(${ix.keys}, { name: ${JSON.stringify(ix.name)}${ix.unique ? ', unique: true' : ''} })`),
                  },
                  { hidden: readOnly || ix.name === '_id_', separator: true },
                  {
                    hidden: readOnly || ix.name === '_id_',
                    label: 'Drop index…',
                    icon: <Trash2 size={14} />,
                    danger: true,
                    onSelect: async () => {
                      const ok = await confirmAction({
                        title: 'Drop index?',
                        message: (
                          <>
                            Index <b className="text-fg">{ix.name}</b> on{' '}
                            <b className="text-fg">
                              {db}.{coll}
                            </b>{' '}
                            will be dropped. Queries relying on it may become slow.
                          </>
                        ),
                        confirmLabel: 'Drop index',
                        danger: true,
                      })
                      if (!ok) return
                      try {
                        await Shell.DropIndex(sessionId, db, coll, ix.name)
                        toast.success(`Index ${ix.name} dropped`)
                        void loadDocIndexes(sessionId, db, coll)
                      } catch (err) {
                        toast.error('Could not drop index', { description: errorMessage(err) })
                      }
                    },
                  },
                ]}
              />
            ))}
        </div>
      )}
    </div>
  )
}
