// The index editor: a form built from what the database supports (DriverInfo.index),
// with the SQL it runs shown live.
import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { ArrowDown, ArrowUp, Copy, Info, Plus, RefreshCw, Save, Trash2, X } from 'lucide-react'
import { toast } from 'sonner'
import { Connections, errorMessage, type Index, type IndexColumn, type IndexPlan } from '../lib/api'
import { keys, useStore, type Tab } from '../lib/store'
import { cn, copyText, uid } from '../lib/utils'
import { confirmAction } from './Confirm'
import { EditorView } from '@codemirror/view'
import { Editor } from './Editor'
import { Button, IconButton, Input, Select, Spinner, Switch } from './ui'

const wrap = [EditorView.lineWrapping]

function Section({ title, hint, children }: { title: string; hint?: ReactNode; children: ReactNode }) {
  return (
    <section className="flex flex-col gap-3 border-b border-line pb-5 last:border-b-0">
      <div>
        <h3 className="text-[13px] font-semibold">{title}</h3>
        {hint && <p className="mt-0.5 text-xs text-faint">{hint}</p>}
      </div>
      {children}
    </section>
  )
}

function Field({ label, hint, children, className }: { label: string; hint?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <label className={cn('flex min-w-0 flex-col gap-1.5', className)}>
      <span className="text-xs font-medium text-muted">{label}</span>
      {children}
      {hint && <span className="text-[11px] text-faint">{hint}</span>}
    </label>
  )
}

interface KeyRow extends IndexColumn {
  id: string
  /** Expression keys are edited as text. */
  expr: boolean
}

interface Form {
  name: string
  method: string
  unique: boolean
  keys: KeyRow[]
  include: string[]
  where: string
  tablespace: string
  fillFactor: string
  invisible: boolean
  clustered: boolean
  concurrently: boolean
  ifNotExists: boolean
}

const emptyKey = (): KeyRow => ({ id: uid('k'), name: '', expression: '', desc: false, nulls: '', collation: '', opclass: '', length: 0, expr: false })

function toForm(ix?: Index): Form {
  return {
    name: ix?.name ?? '',
    method: ix?.method ?? '',
    unique: !!ix?.unique,
    keys: ix?.columns?.length ? ix.columns.map((c) => ({ ...c, id: uid('k'), expr: !!c.expression })) : [emptyKey()],
    include: ix?.include ?? [],
    where: ix?.where ?? '',
    tablespace: ix?.tablespace ?? '',
    fillFactor: ix?.fillFactor ? String(ix.fillFactor) : '',
    invisible: !!ix?.invisible,
    clustered: !!ix?.clustered,
    concurrently: false,
    ifNotExists: false,
  }
}

function toIndex(f: Form, table: string): Index {
  return {
    name: f.name.trim(),
    table,
    columns: f.keys.map(({ id: _id, expr, ...c }) => (expr ? { ...c, name: '', expression: c.expression?.trim() ?? '' } : { ...c, expression: '' })),
    unique: f.unique,
    primary: false,
    method: f.method,
    where: f.where.trim(),
    include: f.include.filter(Boolean),
    tablespace: f.tablespace.trim(),
    fillFactor: Number(f.fillFactor) || 0,
    invisible: f.invisible,
    clustered: f.clustered,
    definition: '',
    sizeBytes: -1,
    constraint: '',
  }
}

export function IndexTab({ tab }: { tab: Tab }) {
  const ref = tab.index!
  const isNew = !ref.name
  const session = useStore((s) => s.sessions[tab.sessionId])
  const info = useStore((s) => s.drivers[s.profiles.find((p) => p.id === tab.sessionId)?.driver ?? ''])
  const details = useStore((s) => s.tables[keys.table(tab.sessionId, ref.database, ref.schema, ref.table)])
  const { loadTable, updateTab, invalidate } = useStore.getState()
  const feat = info?.index
  const existing = details?.items?.indexes?.find((ix) => ix.name === ref.name)
  const columns = (details?.items?.columns ?? []).map((c) => c.name)
  const [form, setForm] = useState<Form>(() => toForm())
  const [loaded, setLoaded] = useState(isNew)
  const [plan, setPlan] = useState<IndexPlan | null>(null)
  const [planError, setPlanError] = useState('')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const readOnly = !!session?.readOnly || !feat?.editable || !!existing?.constraint

  useEffect(() => {
    if (session && !details) void loadTable(tab.sessionId, ref.database, ref.schema, ref.table, false)
  }, [session, details, tab.sessionId, ref.database, ref.schema, ref.table, loadTable])

  // The form starts from the index once its details arrive.
  useEffect(() => {
    if (!loaded && existing) {
      setForm(toForm(existing))
      setLoaded(true)
    }
  }, [existing, loaded])

  const original = useMemo(() => (existing ? JSON.stringify(toIndex(toForm(existing), ref.table)) : ''), [existing, ref.table])
  const index = useMemo(() => toIndex(form, ref.table), [form, ref.table])
  const changed = isNew || JSON.stringify(index) !== original

  // The statements are planned by the backend as the form changes.
  useEffect(() => {
    if (!session || !changed) {
      setPlan(null)
      setPlanError('')
      return
    }
    let live = true
    const t = setTimeout(() => {
      Connections.PlanIndex(tab.sessionId, ref.database, ref.schema, ref.name, index, { concurrently: form.concurrently, ifNotExists: form.ifNotExists })
        .then((p) => {
          if (!live) return
          setPlan(p)
          setPlanError('')
        })
        .catch((err) => {
          if (!live) return
          setPlan(null)
          setPlanError(errorMessage(err))
        })
    }, 150)
    return () => {
      live = false
      clearTimeout(t)
    }
  }, [session, changed, index, form.concurrently, form.ifNotExists, tab.sessionId, ref.database, ref.schema, ref.name])

  const set = (patch: Partial<Form>) => setForm((f) => ({ ...f, ...patch }))
  const setKey = (id: string, patch: Partial<KeyRow>) => setForm((f) => ({ ...f, keys: f.keys.map((k) => (k.id === id ? { ...k, ...patch } : k)) }))
  const moveKey = (i: number, d: number) =>
    setForm((f) => {
      const keys = [...f.keys]
      const [k] = keys.splice(i, 1)
      keys.splice(i + d, 0, k)
      return { ...f, keys }
    })

  const refresh = () => {
    invalidate(tab.sessionId, ref.database, ref.schema)
    void loadTable(tab.sessionId, ref.database, ref.schema, ref.table, false)
  }

  const save = async () => {
    setSaving(true)
    setError('')
    try {
      const done = await Connections.SaveIndex(tab.sessionId, uid('idx'), ref.database, ref.schema, ref.name, index, { concurrently: form.concurrently, ifNotExists: form.ifNotExists })
      toast.success(isNew ? `Index ${index.name} created` : `Index ${index.name} saved`, { description: done.statements?.length === 2 ? 'Dropped and created again.' : undefined })
      updateTab(tab.id, { index: { ...ref, name: index.name }, title: index.name })
      setLoaded(false)
      refresh()
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setSaving(false)
    }
  }

  const drop = async () => {
    let stmt = ''
    try {
      stmt = await Connections.DropIndexStatement(tab.sessionId, ref.database, ref.schema, ref.table, ref.name)
    } catch (err) {
      return toast.error('Cannot drop this index', { description: errorMessage(err) })
    }
    const ok = await confirmAction({
      title: 'Drop index?',
      message: (
        <>
          This runs <code className="selectable rounded bg-sunken px-1 font-mono text-[12px] text-fg">{stmt}</code>.
        </>
      ),
      confirmLabel: 'Drop',
      danger: true,
    })
    if (!ok) return
    try {
      await Connections.DropIndex(tab.sessionId, ref.database, ref.schema, ref.table, ref.name, form.concurrently)
      toast.success(`Index ${ref.name} dropped`)
      refresh()
      useStore.getState().closeTab(tab.id)
    } catch (err) {
      toast.error('Drop failed', { description: errorMessage(err) })
    }
  }

  const methods = feat?.methods ?? []
  const sql = (plan?.statements ?? []).map((s) => s + ';').join('\n')

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex h-11 shrink-0 items-center gap-2 border-b border-line bg-panel px-3">
        <span className="text-[13px] font-semibold">{isNew ? 'New index' : ref.name}</span>
        <span className="truncate text-xs text-muted">
          on {[ref.schema, ref.table].filter(Boolean).join('.')} · {ref.database}
        </span>
        <div className="ml-auto flex items-center gap-1.5">
          <IconButton label="Reload" onClick={refresh}>
            <RefreshCw size={14} />
          </IconButton>
          {!isNew && !readOnly && (
            <Button size="sm" variant="ghost" onClick={drop}>
              <Trash2 size={13} /> Drop
            </Button>
          )}
          <Button size="sm" variant="primary" onClick={save} disabled={saving || readOnly || !changed || !plan}>
            {saving ? <Spinner size={13} /> : <Save size={13} />} {isNew ? 'Create' : 'Save'}
          </Button>
        </div>
      </div>
      <div className="min-h-0 flex-1 overflow-auto">
        <div className="mx-auto flex max-w-3xl flex-col gap-5 px-6 py-5">
          {error && <div className="selectable rounded-md bg-danger-soft px-3 py-2 text-xs text-danger">{error}</div>}
          {existing?.constraint && (
            <div className="flex items-start gap-2 rounded-md bg-sunken px-3 py-2 text-xs text-muted">
              <Info size={14} className="mt-px shrink-0" />
              This index backs the constraint {existing.constraint}; change the constraint instead.
            </div>
          )}
          {!feat?.editable && <div className="rounded-md bg-sunken px-3 py-2 text-xs text-muted">Indexes cannot be changed on this database.</div>}
          {session?.readOnly && <div className="rounded-md bg-sunken px-3 py-2 text-xs text-muted">This connection is read-only.</div>}

          <Section title="Index">
            <div className="grid grid-cols-2 gap-3">
              <Field label="Name">
                <Input mono value={form.name} disabled={readOnly} onChange={(e) => set({ name: e.target.value })} placeholder={`${ref.table}_idx`} />
              </Field>
              {methods.length > 0 && (
                <Field label="Type">
                  <Select value={form.method || methods[0]} disabled={readOnly} onChange={(v) => set({ method: v })} options={methods.map((m) => ({ value: m, label: m }))} />
                </Field>
              )}
            </div>
            <div className="grid grid-cols-2 gap-x-6 gap-y-3">
              <Switch checked={form.unique} disabled={readOnly} onChange={(v) => set({ unique: v })} label="Unique" hint="Rows cannot repeat the key." />
              {feat?.clustered && <Switch checked={form.clustered} disabled={readOnly} onChange={(v) => set({ clustered: v })} label="Clustered" hint="Stores the rows in key order (one per table)." />}
              {feat?.invisible && <Switch checked={form.invisible} disabled={readOnly} onChange={(v) => set({ invisible: v })} label="Invisible" hint="Kept up to date, but not used by the planner." />}
            </div>
          </Section>

          <Section title="Keys" hint={feat?.expressions ? 'Columns, or expressions such as lower(email).' : 'The columns of the key, in order.'}>
            <div className="flex flex-col gap-2">
              {form.keys.map((k, i) => (
                <div key={k.id} className="flex flex-wrap items-center gap-2">
                  {k.expr ? (
                    <Input mono className="min-w-48 flex-1" value={k.expression ?? ''} disabled={readOnly} placeholder="lower(email)" onChange={(e) => setKey(k.id, { expression: e.target.value })} />
                  ) : (
                    <div className="min-w-48 flex-1">
                      <Select
                        className="w-full"
                        value={k.name}
                        disabled={readOnly}
                        onChange={(v) => setKey(k.id, { name: v })}
                        options={[{ value: '', label: 'Choose a column…' }, ...columns.map((c) => ({ value: c, label: c }))]}
                      />
                    </div>
                  )}
                  {feat?.expressions && (
                    <Button size="sm" variant="ghost" disabled={readOnly} onClick={() => setKey(k.id, { expr: !k.expr })} title="Column or expression">
                      {k.expr ? 'Column' : 'Expression'}
                    </Button>
                  )}
                  {feat?.desc && (
                    <Select value={k.desc ? 'desc' : 'asc'} disabled={readOnly} onChange={(v) => setKey(k.id, { desc: v === 'desc' })} options={[{ value: 'asc', label: 'ASC' }, { value: 'desc', label: 'DESC' }]} />
                  )}
                  {feat?.nulls && (
                    <Select
                      value={k.nulls ?? ''}
                      disabled={readOnly}
                      onChange={(v) => setKey(k.id, { nulls: v })}
                      options={[
                        { value: '', label: 'Nulls default' },
                        { value: 'first', label: 'Nulls first' },
                        { value: 'last', label: 'Nulls last' },
                      ]}
                    />
                  )}
                  {feat?.length && !k.expr && (
                    <Input
                      className="w-20"
                      type="number"
                      min={0}
                      value={k.length || ''}
                      disabled={readOnly}
                      placeholder="Length"
                      title="Prefix length"
                      onChange={(e) => setKey(k.id, { length: Number(e.target.value) || 0 })}
                    />
                  )}
                  {feat?.collation && <Input mono className="w-28" value={k.collation ?? ''} disabled={readOnly} placeholder="Collation" onChange={(e) => setKey(k.id, { collation: e.target.value })} />}
                  <IconButton label="Move up" disabled={readOnly || i === 0} onClick={() => moveKey(i, -1)}>
                    <ArrowUp size={14} />
                  </IconButton>
                  <IconButton label="Move down" disabled={readOnly || i === form.keys.length - 1} onClick={() => moveKey(i, 1)}>
                    <ArrowDown size={14} />
                  </IconButton>
                  <IconButton label="Remove key" disabled={readOnly || form.keys.length === 1} onClick={() => set({ keys: form.keys.filter((x) => x.id !== k.id) })}>
                    <X size={14} />
                  </IconButton>
                </div>
              ))}
              {!readOnly && (
                <div>
                  <Button size="sm" onClick={() => set({ keys: [...form.keys, emptyKey()] })}>
                    <Plus size={13} /> Add key
                  </Button>
                </div>
              )}
            </div>
          </Section>

          {(feat?.include || feat?.partial || feat?.tablespace || feat?.fillFactor || feat?.concurrently || feat?.ifNotExists) && (
            <Section title="Options">
              {feat?.include && (
                <Field label="Included columns" hint="Stored in the index without being part of the key (covering index).">
                  <div className="flex flex-wrap gap-1.5">
                    {columns.map((c) => {
                      const on = form.include.includes(c)
                      return (
                        <button
                          key={c}
                          type="button"
                          disabled={readOnly}
                          onClick={() => set({ include: on ? form.include.filter((x) => x !== c) : [...form.include, c] })}
                          className={cn('rounded-md border px-2 py-0.5 font-mono text-[12px]', on ? 'border-accent bg-accent-soft text-fg' : 'border-line-strong text-muted hover:bg-hover')}
                        >
                          {c}
                        </button>
                      )
                    })}
                  </div>
                </Field>
              )}
              {feat?.partial && (
                <Field label="Only rows where" hint="A partial index covers the rows matching this condition.">
                  <Input mono value={form.where} disabled={readOnly} placeholder="deleted_at IS NULL" onChange={(e) => set({ where: e.target.value })} />
                </Field>
              )}
              <div className="grid grid-cols-2 gap-3">
                {feat?.tablespace && (
                  <Field label="Tablespace">
                    <Input mono value={form.tablespace} disabled={readOnly} onChange={(e) => set({ tablespace: e.target.value })} />
                  </Field>
                )}
                {feat?.fillFactor && (
                  <Field label="Fill factor" hint="10–100; lower leaves room for updates.">
                    <Input type="number" min={10} max={100} value={form.fillFactor} disabled={readOnly} onChange={(e) => set({ fillFactor: e.target.value })} />
                  </Field>
                )}
              </div>
              <div className="grid grid-cols-2 gap-x-6 gap-y-3">
                {feat?.concurrently && (
                  <Switch
                    checked={form.concurrently}
                    disabled={readOnly}
                    onChange={(v) => set({ concurrently: v })}
                    label="Build concurrently"
                    hint="Does not block writes while it builds; slower, and cannot run in a transaction."
                  />
                )}
                {feat?.ifNotExists && isNew && <Switch checked={form.ifNotExists} disabled={readOnly} onChange={(v) => set({ ifNotExists: v })} label="If not exists" hint="Skips creating it when an index has this name." />}
              </div>
            </Section>
          )}

          <Section title={isNew ? 'SQL to run' : 'SQL to run when saving'} hint={changed ? undefined : 'No changes.'}>
            {planError && <div className="selectable rounded-md bg-danger-soft px-3 py-2 text-xs text-danger">{planError}</div>}
            {plan?.warning && <div className="rounded-md bg-warn/15 px-3 py-2 text-xs text-warn">{plan.warning}</div>}
            {sql && (
              <div className="relative overflow-hidden rounded-md border border-line">
                <Editor value={sql} readOnly lineNumbers={false} sql={{ dialect: info?.editor ?? 'StandardSQL' }} extensions={wrap} className="max-h-60 pr-8" />
                <div className="absolute top-1 right-1">
                  <IconButton label="Copy SQL" onClick={() => copyText(sql).then(() => toast.success('SQL copied'))}>
                    <Copy size={13} />
                  </IconButton>
                </div>
              </div>
            )}
            {plan && (plan.statements?.length ?? 0) > 1 && <p className="text-[11px] text-faint">{plan.atomic ? 'Runs in one transaction: if creating fails, the old index stays.' : 'Runs in two steps.'}</p>}
          </Section>
        </div>
      </div>
    </div>
  )
}
