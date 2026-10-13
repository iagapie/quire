import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { ArrowDown, ArrowUp, ChevronRight, Copy, Info, KeyRound, Lock, Plus, RefreshCw, RotateCcw, Save, Trash2, X } from 'lucide-react'
import { toast } from 'sonner'
import { Shell, errorMessage, type IndexDetails } from '../../lib/shell/api'
import { parseEJSON } from '../../lib/shell/ejson'
import {
  KEY_TYPES,
  commandText,
  emptyForm,
  formatBytes,
  fromSpec,
  hasType,
  needsRebuild,
  specText,
  type IndexForm,
} from '../../lib/shell/indexForm'
import { keys as storeKeys, useStore, type Tab } from '../../lib/store'
import { cn, copyText, formatNumber } from '../../lib/utils'
import { confirmAction } from '../Confirm'
import { Editor } from './ShellEditor'
import { Button, IconButton, Input, Select, Spinner, Switch } from '../ui'

function Section({ title, hint, children }: { title: string; hint?: ReactNode; children: ReactNode }) {
  return (
    <section className="flex flex-col gap-3 border-b border-line pb-5">
      <div>
        <h3 className="text-[13px] font-semibold">{title}</h3>
        {hint && <p className="mt-0.5 text-xs text-faint">{hint}</p>}
      </div>
      {children}
    </section>
  )
}

function DocInput({ value, onChange, placeholder, readOnly }: { value: string; onChange: (v: string) => void; placeholder: string; readOnly?: boolean }) {
  return (
    <div className="rounded-md border border-line-strong bg-sunken focus-within:border-accent">
      <Editor value={value} onChange={onChange} lineNumbers={false} placeholder={placeholder} readOnly={readOnly} className="max-h-48 min-h-14 overflow-auto" />
    </div>
  )
}

export function ShellIndexTab({ tab }: { tab: Tab }) {
  const ref = tab.docIndex!
  const isNew = !ref.name
  const session = useStore((s) => s.sessions[tab.sessionId])
  const profile = useStore((s) => s.profiles.find((p) => p.id === tab.sessionId))
  const readOnly = !!session?.readOnly || ref.name === '_id_'
  const [original, setOriginal] = useState<IndexForm>(emptyForm)
  const [form, setForm] = useState<IndexForm>(emptyForm)
  const [details, setDetails] = useState<IndexDetails | null>(null)
  const [loading, setLoading] = useState(!isNew)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [invalid, setInvalid] = useState('')
  const firstKey = useRef<HTMLInputElement>(null)

  // A new index starts in the first key field. Wait a moment: the context menu that
  // opened this tab returns focus to the tree when it closes.
  useEffect(() => {
    if (!isNew) return
    const t = setTimeout(() => firstKey.current?.focus(), 80)
    return () => clearTimeout(t)
  }, [isNew])

  const load = useCallback(async () => {
    if (!ref.name) return
    setLoading(true)
    setError('')
    try {
      const d = await Shell.GetIndex(tab.sessionId, tab.database, ref.collection, ref.name)
      const f = fromSpec(parseEJSON(d.spec))
      setDetails(d)
      setOriginal(f)
      setForm(f)
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setLoading(false)
    }
  }, [tab.sessionId, tab.database, ref.collection, ref.name])

  useEffect(() => {
    if (session) void load()
  }, [session, load])

  const spec = useMemo(() => specText(form), [form])
  const command = useMemo(() => commandText(ref.collection, form), [ref.collection, form])
  const dirty = JSON.stringify(form) !== JSON.stringify(original)
  const hasKey = form.keys.some((k) => k.field.trim())

  // Validate the shell-syntax parts as the user types.
  useEffect(() => {
    const t = setTimeout(() => {
      Shell.ValidateDocument(spec)
        .then(setInvalid)
        .catch(() => {})
    }, 300)
    return () => clearTimeout(t)
  }, [spec])

  const set = (patch: Partial<IndexForm>) => setForm((f) => ({ ...f, ...patch }))
  const setKey = (i: number, patch: Partial<IndexForm['keys'][number]>) =>
    setForm((f) => ({ ...f, keys: f.keys.map((k, j) => (j === i ? { ...k, ...patch } : k)) }))
  const moveKey = (i: number, d: number) =>
    setForm((f) => {
      const keys = [...f.keys]
      const [k] = keys.splice(i, 1)
      keys.splice(i + d, 0, k)
      return { ...f, keys }
    })

  const refreshTree = () => void useStore.getState().loadDocIndexes(tab.sessionId, tab.database, ref.collection)

  const save = async () => {
    if (!isNew && needsRebuild(original, form)) {
      const ok = await confirmAction({
        title: 'Rebuild index?',
        message: (
          <>
            These changes can only be applied by dropping <b className="text-fg">{ref.name}</b> and creating it again. Queries cannot use the index while it is
            rebuilt, which can take a while on large collections. If the new definition fails, the previous index is restored.
          </>
        ),
        confirmLabel: 'Rebuild index',
      })
      if (!ok) return
    }
    setSaving(true)
    setError('')
    try {
      const res = await Shell.SaveIndex(tab.sessionId, tab.database, ref.collection, ref.name ?? '', spec)
      const labels: Record<string, string> = { created: 'Index created', modified: 'Index updated', recreated: 'Index rebuilt', unchanged: 'No changes to save' }
      toast.success(labels[res.action] ?? 'Saved', { description: res.name })
      useStore.getState().updateTab(tab.id, { docIndex: { collection: ref.collection, name: res.name }, title: res.name })
      refreshTree()
      if (res.name === ref.name) void load()
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setSaving(false)
    }
  }

  const drop = async () => {
    if (!ref.name) return
    const ok = await confirmAction({
      title: 'Drop index?',
      message: (
        <>
          Index <b className="text-fg">{ref.name}</b> on <b className="text-fg">{tab.database}.{ref.collection}</b> will be dropped. Queries relying on it may become slow.
        </>
      ),
      confirmLabel: 'Drop index',
      danger: true,
    })
    if (!ok) return
    try {
      await Shell.DropIndex(tab.sessionId, tab.database, ref.collection, ref.name)
      toast.success(`Index ${ref.name} dropped`)
      refreshTree()
      useStore.getState().closeTab(tab.id)
    } catch (err) {
      toast.error('Could not drop index', { description: errorMessage(err) })
    }
  }

  const connect = () => useStore.getState().connect(tab.sessionId)
  const collections = useStore((s) => s.collections[storeKeys.db(tab.sessionId, tab.database)]?.items)
  const isView = collections?.find((c) => c.name === ref.collection)?.type === 'view'

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex h-11 shrink-0 items-center gap-1 border-b border-line bg-panel px-2">
        {!readOnly && (
          <Button variant="primary" size="sm" className="h-7 px-2.5" onClick={save} disabled={saving || loading || !session || !hasKey || !!invalid || (!isNew && !dirty)}>
            {saving ? <Spinner size={12} /> : <Save size={13} />}
            {isNew ? 'Create index' : 'Save'}
          </Button>
        )}
        {!isNew && !readOnly && (
          <IconButton label="Discard changes" disabled={!dirty} onClick={() => setForm(original)}>
            <RotateCcw size={14} />
          </IconButton>
        )}
        {!isNew && (
          <IconButton label="Reload" onClick={load} disabled={!session}>
            <RefreshCw size={14} />
          </IconButton>
        )}
        <IconButton
          label="Copy createIndex command"
          onClick={() => copyText(command).then(() => toast.success('Command copied'))}
          disabled={!hasKey}
        >
          <Copy size={14} />
        </IconButton>
        {!isNew && !readOnly && (
          <IconButton label="Drop index" tone="danger" onClick={drop}>
            <Trash2 size={14} />
          </IconButton>
        )}
        <div className="ml-auto flex min-w-0 items-center gap-1 text-xs text-muted">
          {readOnly && (
            <span className="mr-1 flex items-center gap-1 rounded-md bg-sunken px-1.5 py-0.5 text-[11px]">
              <Lock size={11} /> {ref.name === '_id_' ? 'Built-in index' : 'Read-only'}
            </span>
          )}
          {!session && (
            <button className="mr-1 rounded px-1.5 py-0.5 font-medium text-accent hover:bg-accent-soft" onClick={connect}>
              Connect
            </button>
          )}
          <span className="truncate font-medium text-fg">{profile?.name}</span>
          <ChevronRight size={12} className="shrink-0 text-faint" />
          <span className="truncate">{tab.database}</span>
          <ChevronRight size={12} className="shrink-0 text-faint" />
          <span className="truncate">{ref.collection}</span>
          <ChevronRight size={12} className="shrink-0 text-faint" />
          <span className="flex items-center gap-1 truncate font-medium text-fg">
            <KeyRound size={12} className="text-muted" />
            {ref.name ?? 'New index'}
          </span>
        </div>
      </div>

      <div className="min-h-0 flex-1 overflow-auto">
        {loading ? (
          <div className="flex h-full items-center justify-center text-muted">
            <Spinner />
          </div>
        ) : (
          <div className={cn('mx-auto flex max-w-3xl flex-col gap-5 px-6 py-5', readOnly && 'pointer-events-none select-text')}>
            {error && <div className="selectable pointer-events-auto rounded-md bg-danger-soft px-3 py-2 text-xs text-danger">{error}</div>}
            {isView && <div className="rounded-md bg-sunken px-3 py-2 text-xs text-muted">Views cannot have indexes; index the source collection instead.</div>}
            {details && (
              <div className="flex gap-6 text-xs text-muted">
                <span>
                  Size: <b className="font-medium text-fg">{details.sizeBytes >= 0 ? formatBytes(details.sizeBytes) : 'n/a'}</b>
                </span>
                <span>
                  Used: <b className="font-medium text-fg">{details.ops >= 0 ? `${formatNumber(details.ops)} times` : 'n/a'}</b>
                  {details.ops >= 0 && details.since && <span className="text-faint"> since {details.since.replace('T', ' ').replace('Z', ' UTC')}</span>}
                </span>
              </div>
            )}

            <Section title="Keys" hint="Order matters: it decides which queries and sorts the index supports.">
              <div className="flex flex-col gap-1.5">
                {form.keys.map((k, i) => (
                  <div key={i} className="flex items-center gap-1.5">
                    <Input
                      ref={i === 0 ? firstKey : undefined}
                      mono
                      value={k.field}
                      placeholder="field.path"
                      onChange={(e) => setKey(i, { field: e.target.value })}
                      className="flex-1"
                    />
                    <Select
                      value={KEY_TYPES.some((t) => t.value === k.type) ? k.type : 'custom'}
                      onChange={(v) => setKey(i, { type: v === 'custom' ? k.type : v })}
                      options={[...KEY_TYPES, ...(KEY_TYPES.some((t) => t.value === k.type) ? [] : [{ value: 'custom', label: k.type }])]}
                      className="w-40"
                    />
                    <IconButton label="Move up" disabled={i === 0} onClick={() => moveKey(i, -1)}>
                      <ArrowUp size={14} />
                    </IconButton>
                    <IconButton label="Move down" disabled={i === form.keys.length - 1} onClick={() => moveKey(i, 1)}>
                      <ArrowDown size={14} />
                    </IconButton>
                    <IconButton label="Remove field" disabled={form.keys.length === 1} onClick={() => set({ keys: form.keys.filter((_, j) => j !== i) })}>
                      <X size={14} />
                    </IconButton>
                  </div>
                ))}
                {!readOnly && (
                  <button
                    className="flex h-7 w-fit items-center gap-1.5 rounded-md px-2 text-xs font-medium text-accent hover:bg-accent-soft"
                    onClick={() => set({ keys: [...form.keys, { field: '', type: '1' }] })}
                  >
                    <Plus size={13} /> Add field
                  </button>
                )}
              </div>
            </Section>

            <Section title="General">
              <label className="flex flex-col gap-1.5">
                <span className="text-xs font-medium text-muted">Name</span>
                <Input mono value={form.name} onChange={(e) => set({ name: e.target.value })} placeholder="Generated from the keys when empty" />
              </label>
              <div className="grid grid-cols-2 gap-x-6 gap-y-3">
                <Switch checked={form.unique} onChange={(v) => set({ unique: v })} label="Unique" hint="Reject documents with duplicate key values." />
                <Switch checked={form.sparse} onChange={(v) => set({ sparse: v })} label="Sparse" hint="Only index documents that have the indexed fields." />
                <Switch
                  checked={form.hidden}
                  onChange={(v) => set({ hidden: v })}
                  label="Hidden"
                  hint="Kept up to date but ignored by the query planner. Changed in place."
                />
                <div className="flex flex-col gap-1.5">
                  <Switch
                    checked={form.ttlEnabled}
                    onChange={(v) => set({ ttlEnabled: v })}
                    label="Time to live (TTL)"
                    hint="Delete documents this many seconds after the date in the field. Changed in place."
                  />
                  {form.ttlEnabled && (
                    <div className="ml-10 flex items-center gap-2">
                      <Input mono type="number" min={0} value={form.ttl} onChange={(e) => set({ ttl: e.target.value })} className="w-32" />
                      <span className="text-xs text-faint">seconds</span>
                    </div>
                  )}
                </div>
              </div>
            </Section>

            <Section title="Partial filter" hint="Only index documents matching this filter, e.g. { status: &quot;active&quot; }.">
              <DocInput value={form.partial} onChange={(v) => set({ partial: v })} placeholder="{ }" readOnly={readOnly} />
            </Section>

            {hasType(form, 'text') && (
              <Section title="Text search">
                <label className="flex flex-col gap-1.5">
                  <span className="text-xs font-medium text-muted">Weights</span>
                  <DocInput value={form.weights} onChange={(v) => set({ weights: v })} placeholder="{ title: 10, body: 1 }" readOnly={readOnly} />
                </label>
                <div className="grid grid-cols-2 gap-3">
                  <label className="flex flex-col gap-1.5">
                    <span className="text-xs font-medium text-muted">Default language</span>
                    <Input value={form.defaultLanguage} onChange={(e) => set({ defaultLanguage: e.target.value })} placeholder="english" />
                  </label>
                  <label className="flex flex-col gap-1.5">
                    <span className="text-xs font-medium text-muted">Language override field</span>
                    <Input mono value={form.languageOverride} onChange={(e) => set({ languageOverride: e.target.value })} placeholder="language" />
                  </label>
                </div>
              </Section>
            )}

            {hasType(form, '2dsphere') && (
              <Section title="Geospatial">
                <label className="flex w-48 flex-col gap-1.5">
                  <span className="text-xs font-medium text-muted">2dsphere index version</span>
                  <Input mono type="number" min={1} value={form.sphereVersion} onChange={(e) => set({ sphereVersion: e.target.value })} placeholder="3" />
                </label>
              </Section>
            )}

            <Section title="Collation" hint="Language-specific string comparison, e.g. { locale: &quot;ro&quot;, strength: 2 }.">
              <DocInput value={form.collation} onChange={(v) => set({ collation: v })} placeholder="{ }" readOnly={readOnly} />
            </Section>

            <Section title="Other options" hint="Any other createIndexes option, e.g. { wildcardProjection: { a: 1 } }.">
              <DocInput value={form.extra} onChange={(v) => set({ extra: v })} placeholder="{ }" readOnly={readOnly} />
            </Section>

            <section className="pointer-events-auto flex flex-col gap-2">
              <div className="flex items-center justify-between">
                <h3 className="text-[13px] font-semibold">Command</h3>
                {invalid && <span className="truncate text-xs text-warn" title={invalid}>{invalid}</span>}
              </div>
              <pre className="selectable overflow-auto rounded-md bg-sunken p-3 font-mono text-[12px] whitespace-pre text-muted">{command}</pre>
              <p className="flex items-start gap-1.5 text-[11px] text-faint">
                <Info size={12} className="mt-px shrink-0" />
                <span>
                  Amazon DocumentDB does not support every option (for example collation, hidden, hashed or wildcard indexes); the server rejects unsupported
                  ones when you save.
                </span>
              </p>
            </section>
          </div>
        )}
      </div>
    </div>
  )
}
