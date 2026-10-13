// Index editor model: listIndexes spec <-> form <-> createIndexes spec (shell syntax).
import { type BVal, format } from './ejson'

export const KEY_TYPES = [
  { value: '1', label: 'Ascending (1)' },
  { value: '-1', label: 'Descending (-1)' },
  { value: 'text', label: 'Text' },
  { value: '2dsphere', label: '2dsphere' },
  { value: '2d', label: '2d' },
  { value: 'hashed', label: 'Hashed' },
]

export interface KeyField {
  field: string
  type: string
}

export interface IndexForm {
  name: string
  keys: KeyField[]
  unique: boolean
  sparse: boolean
  hidden: boolean
  ttlEnabled: boolean
  ttl: string
  /** Shell-syntax documents; empty means "not set". */
  partial: string
  collation: string
  weights: string
  defaultLanguage: string
  languageOverride: string
  sphereVersion: string
  /** Any other creation options, as a shell-syntax document. */
  extra: string
}

export const emptyForm = (): IndexForm => ({
  name: '',
  keys: [{ field: '', type: '1' }],
  unique: false,
  sparse: false,
  hidden: false,
  ttlEnabled: false,
  ttl: '3600',
  partial: '',
  collation: '',
  weights: '',
  defaultLanguage: '',
  languageOverride: '',
  sphereVersion: '',
  extra: '',
})

// Fields shown in dedicated inputs, or added by the server and not creation options.
const KNOWN = new Set([
  'key',
  'name',
  'unique',
  'sparse',
  'hidden',
  'expireAfterSeconds',
  'partialFilterExpression',
  'collation',
  'weights',
  'default_language',
  'language_override',
  '2dsphereIndexVersion',
  'v',
  'ns',
  'textIndexVersion',
])

const scalar = (v?: BVal): string => {
  if (!v) return ''
  if (v.t === 'string') return v.v
  if (v.t === 'int32' || v.t === 'int64' || v.t === 'double' || v.t === 'decimal') return String(Number(v.v))
  return format(v, '')
}
const bool = (v?: BVal) => v?.t === 'bool' && v.v
const doc = (v?: BVal) => (v ? format(v) : '')

export function fromSpec(spec: BVal): IndexForm {
  if (spec.t !== 'object') return emptyForm()
  const get = (k: string) => spec.v.find(([key]) => key === k)?.[1]
  const key = get('key')
  const ttl = get('expireAfterSeconds')
  const extra = spec.v.filter(([k]) => !KNOWN.has(k))
  return {
    name: scalar(get('name')),
    keys: key?.t === 'object' && key.v.length > 0 ? key.v.map(([field, v]) => ({ field, type: scalar(v) })) : [{ field: '', type: '1' }],
    unique: bool(get('unique')),
    sparse: bool(get('sparse')),
    hidden: bool(get('hidden')),
    ttlEnabled: ttl !== undefined,
    ttl: ttl ? scalar(ttl) : '3600',
    partial: doc(get('partialFilterExpression')),
    collation: doc(get('collation')),
    weights: doc(get('weights')),
    defaultLanguage: scalar(get('default_language')),
    languageOverride: scalar(get('language_override')),
    sphereVersion: scalar(get('2dsphereIndexVersion')),
    extra: extra.length ? format({ t: 'object', v: extra }) : '',
  }
}

const isNumericType = (t: string) => /^-?\d+(\.\d+)?$/.test(t.trim())

export function keysText(form: IndexForm): string {
  const parts = form.keys
    .filter((k) => k.field.trim())
    .map((k) => `${JSON.stringify(k.field.trim())}: ${isNumericType(k.type) ? k.type.trim() : JSON.stringify(k.type)}`)
  return `{ ${parts.join(', ')} }`
}

/** The creation options (everything but the key), as "name: value" entries. */
function optionEntries(form: IndexForm): string[] {
  const o: string[] = []
  if (form.name.trim()) o.push(`name: ${JSON.stringify(form.name.trim())}`)
  if (form.unique) o.push('unique: true')
  if (form.sparse) o.push('sparse: true')
  if (form.hidden) o.push('hidden: true')
  if (form.ttlEnabled) o.push(`expireAfterSeconds: ${Number(form.ttl) || 0}`)
  if (form.partial.trim()) o.push(`partialFilterExpression: ${form.partial.trim()}`)
  if (form.collation.trim()) o.push(`collation: ${form.collation.trim()}`)
  if (form.weights.trim()) o.push(`weights: ${form.weights.trim()}`)
  if (form.defaultLanguage.trim()) o.push(`default_language: ${JSON.stringify(form.defaultLanguage.trim())}`)
  if (form.languageOverride.trim()) o.push(`language_override: ${JSON.stringify(form.languageOverride.trim())}`)
  if (form.sphereVersion.trim()) o.push(`"2dsphereIndexVersion": ${Number(form.sphereVersion) || 0}`)
  return o
}

/** The createIndexes spec for the backend: { key, name, ...options }. */
export function specText(form: IndexForm): string {
  const base = `{ key: ${keysText(form)}${optionEntries(form).map((e) => `, ${e}`).join('')} }`
  return form.extra.trim() ? `Object.assign(${base}, ${form.extra.trim()})` : base
}

/** The equivalent shell command, for copying. */
export function commandText(collection: string, form: IndexForm): string {
  const opts = optionEntries(form)
  const extra = form.extra.trim()
  let options = opts.length ? `{\n  ${opts.join(',\n  ')}\n}` : ''
  if (extra) options = options ? `Object.assign(${options}, ${extra})` : extra
  return `db.getCollection(${JSON.stringify(collection)}).createIndex(\n  ${keysText(form)}${options ? `,\n  ${options.replace(/\n/g, '\n  ')}` : ''}\n)`
}

/** Whether saving the form over `original` needs a drop and rebuild (only TTL and hidden change in place). */
export function needsRebuild(original: IndexForm, form: IndexForm): boolean {
  const strip = (f: IndexForm) => JSON.stringify({ ...f, hidden: false, ttl: '', ttlEnabled: false })
  return strip(original) !== strip(form) || (original.ttlEnabled && !form.ttlEnabled)
}

export function hasType(form: IndexForm, type: string) {
  return form.keys.some((k) => k.type === type)
}

export { formatBytes } from '../utils'
