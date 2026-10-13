import { useEffect, useState, type ReactNode } from 'react'
import { CheckCircle2, FilePlus2, FolderOpen, KeyRound, Lock, Network, Settings2, ShieldCheck, XCircle } from 'lucide-react'
import { toast } from 'sonner'
import { Connections, Files, errorMessage, type DriverInfo, type Field as DriverField, type Profile, type TestResult } from '../lib/api'
import { useStore } from '../lib/store'
import { useUI } from '../lib/ui'
import { cn, PROFILE_COLORS } from '../lib/utils'
import { Button, Field, IconButton, Input, Modal, Select, Spinner, Switch } from './ui'
import { DriverIcon } from './DriverIcon'

function emptyProfile(info?: DriverInfo): Profile {
  return {
    id: '',
    name: '',
    color: '',
    driver: info?.id ?? 'postgres',
    host: info?.file ? '' : 'localhost',
    port: 0,
    database: info?.defaultDatabase ?? '',
    user: info?.defaultUser ?? '',
    password: '',
    uri: '',
    hasUri: false,
    options: Object.fromEntries((info?.fields ?? []).filter((f) => f.default).map((f) => [f.key, f.default])),
    secretOptions: {},
    readOnly: false,
    queryTimeoutMs: 120000,
    // The driver's default, else prefer where the driver has it (encrypt when the server can), else its first mode.
    tls: {
      mode: info?.tlsDefault || (!info?.tls?.length || info.tls.includes('prefer') ? 'prefer' : info.tls[0]),
      useAwsBundle: !!info?.awsBundle,
      caFile: '',
      certFile: '',
      keyFile: '',
      serverName: '',
    },
    ssh: { enabled: false, host: '', port: 22, user: '', auth: 'key', password: '', privateKeyPath: '~/.ssh/id_ed25519', passphrase: '', hostKey: '' },
  }
}

type Section = 'server' | 'auth' | 'tls' | 'ssh' | 'advanced'

const TLS_LABELS: Record<string, string> = {
  disable: 'Disable',
  prefer: 'Prefer (encrypt if the server supports it)',
  require: 'Require (encrypt, do not verify)',
  'verify-ca': 'Verify CA (check the certificate chain)',
  'verify-full': 'Verify full (chain and host name)',
}

export function ConnectionDialog() {
  const { open, profileId } = useUI((s) => s.connectionDialog)
  const close = useUI((s) => s.closeConnectionDialog)
  const drivers = useStore((s) => s.driverList)
  const [p, setP] = useState<Profile>(() => emptyProfile())
  const [section, setSection] = useState<Section>('server')
  const [testing, setTesting] = useState(false)
  const [test, setTest] = useState<{ ok: true; res: TestResult } | { ok: false; error: string } | null>(null)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [useURI, setUseURI] = useState(false)
  const info = drivers.find((d) => d.id === p.driver)

  useEffect(() => {
    if (!open) return
    setSection('server')
    setTest(null)
    setError('')
    if (profileId) {
      Connections.GetProfile(profileId)
        .then((prof) => {
          setP({ ...prof, options: prof.options ?? {}, secretOptions: prof.secretOptions ?? {} })
          setUseURI(prof.hasUri)
        })
        .catch((err) => setError(errorMessage(err)))
    } else {
      setP(emptyProfile(drivers[0]))
      setUseURI(false)
    }
  }, [open, profileId, drivers])

  const upd = (patch: Partial<Profile>) => setP((x) => ({ ...x, ...patch }))
  const updTLS = (patch: Partial<Profile['tls']>) => setP((x) => ({ ...x, tls: { ...x.tls, ...patch } }))
  const updSSH = (patch: Partial<Profile['ssh']>) => setP((x) => ({ ...x, ssh: { ...x.ssh, ...patch } }))
  const setOption = (f: DriverField, v: string) =>
    setP((x) => (f.secret ? { ...x, secretOptions: { ...(x.secretOptions ?? {}), [f.key]: v } } : { ...x, options: { ...(x.options ?? {}), [f.key]: v } }))
  const editing = !!profileId

  const pickDriver = (d: DriverInfo) => {
    if (d.id === p.driver) return
    setP((x) => ({
      ...emptyProfile(d),
      name: x.name,
      color: x.color,
      readOnly: x.readOnly,
      queryTimeoutMs: x.queryTimeoutMs,
      id: x.id,
      ssh: x.ssh,
      host: d.file ? '' : x.host || 'localhost',
    }))
  }

  const prepared = (): Profile => ({
    ...p,
    name: p.name.trim() || (info?.file ? (p.database || ':memory:').split(/[\\/]/).pop()! : p.host.trim()) || 'New connection',
    uri: useURI ? p.uri : '',
    hasUri: useURI && (p.hasUri || !!p.uri),
  })

  const runTest = async () => {
    setTesting(true)
    setTest(null)
    try {
      setTest({ ok: true, res: await Connections.TestConnection(prepared()) })
    } catch (err) {
      setTest({ ok: false, error: errorMessage(err) })
    } finally {
      setTesting(false)
    }
  }

  const save = async (connect: boolean) => {
    setSaving(true)
    setError('')
    try {
      const saved = await Connections.SaveProfile(prepared())
      const store = useStore.getState()
      await store.loadProfiles()
      close()
      toast.success(editing ? 'Connection updated' : 'Connection saved')
      if (editing && store.sessions[saved.id]) {
        toast.info('Reconnect to apply the new settings', {
          action: {
            label: 'Reconnect',
            onClick: async () => {
              await store.disconnect(saved.id)
              await store.connect(saved.id)
            },
          },
        })
      }
      if (connect) {
        store.toggle(`c:${saved.id}`, true)
        await store.connect(saved.id)
      }
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setSaving(false)
    }
  }

  const pick = async (title: string, apply: (path: string) => void) => {
    try {
      const path = await Files.PickFile(title)
      if (path) apply(path)
    } catch (err) {
      toast.error('Could not open file picker', { description: errorMessage(err) })
    }
  }

  const sections: { id: Section; label: string; icon: ReactNode; show: boolean; dot?: boolean }[] = [
    { id: 'server', label: info?.file ? 'Database' : 'Server', icon: <Network size={14} />, show: true },
    { id: 'auth', label: 'Authentication', icon: <Lock size={14} />, show: !info?.file },
    { id: 'tls', label: 'TLS', icon: <ShieldCheck size={14} />, show: (info?.tls?.length ?? 0) > 0, dot: p.tls.mode !== 'disable' && p.tls.mode !== 'prefer' },
    { id: 'ssh', label: 'SSH tunnel', icon: <KeyRound size={14} />, show: !!info?.ssh, dot: p.ssh.enabled },
    { id: 'advanced', label: 'Advanced', icon: <Settings2 size={14} />, show: true },
  ]
  const fields = (sec: string) => (info?.fields ?? []).filter((f) => f.section === sec)

  return (
    <Modal
      open={open}
      onOpenChange={(o) => !o && close()}
      title={editing ? 'Edit connection' : 'New connection'}
      description="Saved locally; passwords are kept in the system keychain."
      width="max-w-3xl"
      footer={
        <>
          <div className="mr-auto flex min-w-0 items-center gap-2 text-xs">
            <Button onClick={runTest} disabled={testing}>
              {testing ? <Spinner size={13} /> : null}
              Test connection
            </Button>
            {test?.ok && (
              <span className="flex min-w-0 items-center gap-1 text-ok">
                <CheckCircle2 size={14} className="shrink-0" />
                <span className="truncate">
                  {test.res.serverVersion || 'Connected'} · {test.res.latencyMs} ms · {test.res.databases} {test.res.databases === 1 ? 'database' : 'databases'}
                </span>
              </span>
            )}
            {test && !test.ok && (
              <span className="flex min-w-0 items-center gap-1 text-danger" title={test.error}>
                <XCircle size={14} className="shrink-0" /> <span className="truncate">{test.error}</span>
              </span>
            )}
          </div>
          <Button onClick={close}>Cancel</Button>
          <Button onClick={() => save(false)} disabled={saving}>
            Save
          </Button>
          <Button variant="primary" onClick={() => save(true)} disabled={saving}>
            Save & connect
          </Button>
        </>
      }
    >
      <form
        className="flex h-[min(560px,72vh)]"
        onSubmit={(e) => {
          e.preventDefault()
          void save(true)
        }}
      >
        <nav className="flex w-44 shrink-0 flex-col gap-0.5 border-r border-line p-2">
          {sections
            .filter((s) => s.show)
            .map((s) => (
              <button
                key={s.id}
                type="button"
                onClick={() => setSection(s.id)}
                className={cn(
                  'flex h-8 items-center gap-2 rounded-md px-2.5 text-left text-[13px]',
                  section === s.id ? 'bg-accent-soft font-medium text-accent' : 'text-muted hover:bg-hover hover:text-fg',
                )}
              >
                {s.icon}
                {s.label}
                {s.dot && <span className="ml-auto size-1.5 rounded-full bg-accent" />}
              </button>
            ))}
        </nav>
        <div className="flex min-w-0 flex-1 flex-col gap-4 overflow-y-auto p-5">
          {error && <div className="selectable rounded-md bg-danger-soft px-3 py-2 text-xs whitespace-pre-wrap text-danger">{error}</div>}

          {section === 'server' && (
            <>
              <Field label="Database type">
                <div role="radiogroup" className="grid grid-cols-3 gap-1.5">
                  {drivers.map((d) => (
                    <button
                      key={d.id}
                      type="button"
                      role="radio"
                      aria-checked={p.driver === d.id}
                      onClick={() => pickDriver(d)}
                      title={d.description}
                      className={cn(
                        'flex h-8 items-center justify-center gap-1.5 rounded-md border px-2 text-[13px] transition-colors',
                        p.driver === d.id ? 'border-accent bg-accent-soft font-medium text-fg' : 'border-line-strong text-muted hover:border-faint hover:text-fg',
                      )}
                    >
                      <DriverIcon driver={d.id} size={13} />
                      <span className="truncate">{d.label}</span>
                    </button>
                  ))}
                </div>
              </Field>
              {info?.note && <div className="rounded-md bg-sunken px-3 py-2 text-xs whitespace-pre-wrap text-muted">{info.note}</div>}
              {test && !test.ok && test.error.length > 90 && (
                <div className="selectable rounded-md border border-danger/30 bg-danger-soft px-3 py-2 text-xs whitespace-pre-wrap text-danger">{test.error}</div>
              )}
              <div className="flex gap-3">
                <Field label="Name" className="flex-1">
                  <Input data-autofocus value={p.name} onChange={(e) => upd({ name: e.target.value })} placeholder="Production" />
                </Field>
                <Field label="Color">
                  <div className="flex h-7.5 items-center gap-1">
                    {PROFILE_COLORS.map((c) => (
                      <button
                        key={c || 'none'}
                        type="button"
                        aria-label={c ? `Color ${c}` : 'No color'}
                        onClick={() => upd({ color: c })}
                        className={cn('size-5 rounded-full border border-line-strong', p.color === c && 'ring-2 ring-accent ring-offset-1 ring-offset-panel')}
                        style={{ background: c || 'transparent' }}
                      />
                    ))}
                  </div>
                </Field>
              </div>
              {info?.file ? (
                <Field label="Database file" hint="Leave empty or write :memory: for an in-memory database that lives while the connection is open.">
                  <div className="flex gap-1.5">
                    <Input mono value={p.database} onChange={(e) => upd({ database: e.target.value })} placeholder="/path/to/database.db" />
                    <IconButton
                      type="button"
                      label="Open a database file…"
                      onClick={async () => {
                        const path = await Files.PickDatabaseFile(false).catch(() => '')
                        if (path) upd({ database: path })
                      }}
                    >
                      <FolderOpen size={15} />
                    </IconButton>
                    <IconButton
                      type="button"
                      label="Create a new database file…"
                      onClick={async () => {
                        const path = await Files.PickDatabaseFile(true).catch(() => '')
                        if (path) setP((x) => ({ ...x, database: path, options: { ...(x.options ?? {}), create: 'true' } }))
                      }}
                    >
                      <FilePlus2 size={15} />
                    </IconButton>
                  </div>
                </Field>
              ) : (
                <>
                  <Switch checked={useURI} onChange={setUseURI} label="Use a connection string" hint="Paste a URL or DSN instead of filling in the fields." />
                  {useURI ? (
                    <Field label="Connection string" hint={p.hasUri ? 'A connection string is saved; leave empty to keep it.' : 'Stored in the keychain, as it may contain a password.'}>
                      <Input mono type="password" value={p.uri} onChange={(e) => upd({ uri: e.target.value })} placeholder={p.hasUri ? '••••••••' : info?.uriPlaceholder || `${p.driver}://user:password@host:${info?.defaultPort ?? ''}/database`} />
                    </Field>
                  ) : (
                    <>
                      <div className={cn('grid gap-3', info?.noPort ? 'grid-cols-1' : 'grid-cols-[1fr_100px]')}>
                        <Field label={info?.hostLabel || 'Host'}>
                          <Input mono={!!info?.noPort} value={p.host} onChange={(e) => upd({ host: e.target.value })} placeholder={info?.hostPlaceholder || 'localhost'} />
                        </Field>
                        {!info?.noPort && (
                          <Field label="Port">
                            <Input type="number" value={p.port || ''} placeholder={String(info?.defaultPort ?? '')} onChange={(e) => upd({ port: Number(e.target.value) || 0 })} />
                          </Field>
                        )}
                      </div>
                      <Field
                        label={info?.databaseField || (info?.databaseLabel ? `Default ${info.databaseLabel.toLowerCase()}` : 'Default database')}
                        hint={info?.databaseField ? 'Or a SID, with the option below.' : info?.shell ? 'Opened in new shells; shown even without the right to list databases.' : 'Opened in new editors.'}
                      >
                        <Input value={p.database} onChange={(e) => upd({ database: e.target.value })} placeholder={info?.defaultDatabase} />
                      </Field>
                    </>
                  )}
                </>
              )}
              <DriverFields fields={fields('server')} p={p} onChange={setOption} editing={editing} />
              {!info?.queryOnly && (
                <Switch
                  checked={p.readOnly}
                  onChange={(v) => upd({ readOnly: v })}
                  label="Read-only"
                  hint={
                    info?.shell
                      ? 'Blocks inserts, updates, deletes and other writes from this app, even if the user is allowed to write.'
                      : 'The server refuses writes where it can, and the app runs only SELECT, WITH, EXPLAIN, SHOW, DESCRIBE and PRAGMA.'
                  }
                />
              )}
            </>
          )}

          {section === 'auth' && (
            <>
              <div className={cn('grid gap-3', info?.noUser ? 'grid-cols-1' : 'grid-cols-2')}>
                {!info?.noUser && (
                  <Field label="User">
                    <Input data-autofocus value={p.user} onChange={(e) => upd({ user: e.target.value })} />
                  </Field>
                )}
                <Field label={info?.passwordLabel || 'Password'} hint={editing ? `Leave empty to keep the saved ${(info?.passwordLabel || 'password').toLowerCase()}.` : undefined}>
                  <Input type="password" value={p.password ?? ''} onChange={(e) => upd({ password: e.target.value })} placeholder={editing ? '••••••••' : ''} />
                </Field>
              </div>
              <DriverFields fields={fields('auth')} p={p} onChange={setOption} editing={editing} />
            </>
          )}

          {section === 'tls' && (
            <>
              <Field label="Mode">
                <Select value={p.tls.mode} onChange={(v) => updTLS({ mode: v })} options={(info?.tls ?? []).map((m) => ({ value: m, label: TLS_LABELS[m] ?? m }))} />
              </Field>
              <div className={cn('flex flex-col gap-4', p.tls.mode === 'disable' && 'pointer-events-none opacity-45')}>
                <Switch checked={p.tls.useAwsBundle} onChange={(v) => updTLS({ useAwsBundle: v })} label="Trust the Amazon RDS CA bundle" hint="Bundled with the app, for RDS, Aurora and DocumentDB." />
                {(['caFile', 'certFile', 'keyFile'] as const).map((key) => (
                  <Field key={key} label={key === 'caFile' ? 'CA certificate' : key === 'certFile' ? 'Client certificate' : 'Client key'}>
                    <div className="flex gap-1.5">
                      <Input mono value={p.tls[key]} onChange={(e) => updTLS({ [key]: e.target.value })} placeholder="/path/to/file.pem" />
                      <IconButton type="button" label="Browse…" onClick={() => pick('Certificate', (path) => updTLS({ [key]: path }))}>
                        <FolderOpen size={15} />
                      </IconButton>
                    </div>
                  </Field>
                ))}
                <Field label="Server name" hint="Checked against the certificate instead of the host (useful through tunnels).">
                  <Input value={p.tls.serverName} onChange={(e) => updTLS({ serverName: e.target.value })} />
                </Field>
                <DriverFields fields={fields('tls')} p={p} onChange={setOption} editing={editing} />
              </div>
            </>
          )}

          {section === 'ssh' && (
            <>
              <Switch
                checked={p.ssh.enabled}
                onChange={(v) => updSSH({ enabled: v })}
                label="Connect through an SSH tunnel"
                hint="Connections go through the bastion; a dropped tunnel is re-established automatically."
              />
              <div className={cn('flex flex-col gap-4', !p.ssh.enabled && 'pointer-events-none opacity-45')}>
                <div className="grid grid-cols-[1fr_90px] gap-3">
                  <Field label="SSH host">
                    <Input value={p.ssh.host} onChange={(e) => updSSH({ host: e.target.value })} placeholder="bastion.example.com" />
                  </Field>
                  <Field label="Port">
                    <Input type="number" value={p.ssh.port} onChange={(e) => updSSH({ port: Number(e.target.value) || 22 })} />
                  </Field>
                </div>
                <div className="grid grid-cols-2 gap-3">
                  <Field label="User">
                    <Input value={p.ssh.user} onChange={(e) => updSSH({ user: e.target.value })} />
                  </Field>
                  <Field label="Authentication">
                    <Select
                      value={p.ssh.auth}
                      onChange={(v) => updSSH({ auth: v })}
                      options={[
                        { value: 'key', label: 'Private key' },
                        { value: 'agent', label: 'SSH agent' },
                        { value: 'password', label: 'Password' },
                      ]}
                    />
                  </Field>
                </div>
                {p.ssh.auth === 'key' && (
                  <div className="grid grid-cols-2 gap-3">
                    <Field label="Private key">
                      <div className="flex gap-1.5">
                        <Input mono value={p.ssh.privateKeyPath} onChange={(e) => updSSH({ privateKeyPath: e.target.value })} />
                        <IconButton type="button" label="Browse…" onClick={() => pick('SSH private key', (path) => updSSH({ privateKeyPath: path }))}>
                          <FolderOpen size={15} />
                        </IconButton>
                      </div>
                    </Field>
                    <Field label="Passphrase" hint={editing ? 'Leave empty to keep the saved one.' : 'Only for encrypted keys.'}>
                      <Input type="password" value={p.ssh.passphrase ?? ''} onChange={(e) => updSSH({ passphrase: e.target.value })} />
                    </Field>
                  </div>
                )}
                {p.ssh.auth === 'password' && (
                  <Field label="SSH password" hint={editing ? 'Leave empty to keep the saved password.' : undefined}>
                    <Input type="password" value={p.ssh.password ?? ''} onChange={(e) => updSSH({ password: e.target.value })} />
                  </Field>
                )}
                {p.ssh.hostKey && (
                  <div className="flex items-center justify-between gap-2 rounded-md bg-sunken px-3 py-2 text-xs text-muted">
                    <span className="min-w-0 truncate">
                      Trusted host key: <span className="font-mono">{p.ssh.hostKey}</span>
                    </span>
                    <button type="button" className="shrink-0 text-accent hover:underline" onClick={() => updSSH({ hostKey: '' })}>
                      Forget
                    </button>
                  </div>
                )}
              </div>
            </>
          )}

          {section === 'advanced' && (
            <>
              <Field label="Operation timeout (seconds)" hint="Any statement is cancelled after this time. You can always stop earlier.">
                <Input
                  type="number"
                  min={1}
                  value={Math.round(p.queryTimeoutMs / 1000)}
                  onChange={(e) => upd({ queryTimeoutMs: Math.max(1, Number(e.target.value) || 120) * 1000 })}
                  className="w-32"
                />
              </Field>
              <DriverFields fields={fields('advanced')} p={p} onChange={setOption} editing={editing} />
            </>
          )}
        </div>
        <button type="submit" className="hidden" />
      </form>
    </Modal>
  )
}

function DriverFields({ fields, p, onChange, editing }: { fields: DriverField[]; p: Profile; onChange: (f: DriverField, v: string) => void; editing: boolean }) {
  return (
    <>
      {fields.map((f) => {
        const value = (f.secret ? p.secretOptions?.[f.key] : p.options?.[f.key]) ?? ''
        const stored = f.secret && editing && p.secretOptions && f.key in p.secretOptions
        if (f.type === 'bool') {
          return <Switch key={f.key} checked={(value || f.default) === 'true'} onChange={(v) => onChange(f, v ? 'true' : 'false')} label={f.label} hint={f.hint} />
        }
        return (
          <Field key={f.key} label={f.label} hint={stored ? 'Leave empty to keep the saved value.' : f.hint}>
            {f.type === 'select' ? (
              <Select value={value || f.default} onChange={(v) => onChange(f, v)} options={(f.options ?? []).map((o) => ({ value: o.value, label: o.label }))} />
            ) : f.type === 'file' ? (
              <div className="flex gap-1.5">
                <Input mono value={value} onChange={(e) => onChange(f, e.target.value)} placeholder={f.placeholder} />
                <IconButton
                  type="button"
                  label="Browse…"
                  onClick={async () => {
                    const path = await Files.PickFile(f.label).catch(() => '')
                    if (path) onChange(f, path)
                  }}
                >
                  <FolderOpen size={15} />
                </IconButton>
              </div>
            ) : (
              <Input
                mono={f.type !== 'text'}
                type={f.secret || f.type === 'password' ? 'password' : f.type === 'number' ? 'number' : 'text'}
                value={value}
                onChange={(e) => onChange(f, e.target.value)}
                placeholder={stored ? '••••••••' : f.placeholder}
              />
            )}
          </Field>
        )
      })}
    </>
  )
}
