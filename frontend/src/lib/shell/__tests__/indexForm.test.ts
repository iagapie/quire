import { describe, expect, it } from 'vitest'
import { parseEJSON } from '../ejson'
import { commandText, emptyForm, formatBytes, fromSpec, needsRebuild, specText } from '../indexForm'

const listed = parseEJSON(
  JSON.stringify({
    v: { $numberInt: '2' },
    key: { tenant: { $numberInt: '1' }, createdAt: { $numberInt: '-1' }, loc: '2dsphere' },
    name: 'tenant_1_createdAt_-1_loc_2dsphere',
    unique: true,
    expireAfterSeconds: { $numberInt: '3600' },
    partialFilterExpression: { active: true },
    '2dsphereIndexVersion': { $numberInt: '3' },
    storageEngine: { wiredTiger: {} },
  }),
)

describe('fromSpec', () => {
  it('maps a listIndexes document to the form', () => {
    const f = fromSpec(listed)
    expect(f.keys).toEqual([
      { field: 'tenant', type: '1' },
      { field: 'createdAt', type: '-1' },
      { field: 'loc', type: '2dsphere' },
    ])
    expect(f).toMatchObject({ name: 'tenant_1_createdAt_-1_loc_2dsphere', unique: true, sparse: false, ttlEnabled: true, ttl: '3600', sphereVersion: '3' })
    expect(f.partial).toBe('{\n  active: true\n}')
    // Unknown creation options are kept, server-only fields are not.
    expect(f.extra).toBe('{\n  storageEngine: {\n    wiredTiger: {}\n  }\n}')
  })

  it('starts with one empty key for a new index', () => {
    expect(emptyForm().keys).toEqual([{ field: '', type: '1' }])
  })
})

describe('specText / commandText', () => {
  it('builds the createIndexes spec', () => {
    const f = { ...emptyForm(), keys: [{ field: 'a.b', type: '-1' }, { field: 'title', type: 'text' }], sparse: true, weights: '{ title: 5 }' }
    expect(specText(f)).toBe('{ key: { "a.b": -1, "title": "text" }, sparse: true, weights: { title: 5 } }')
    expect(commandText('orders', f)).toBe('db.getCollection("orders").createIndex(\n  { "a.b": -1, "title": "text" },\n  {\n    sparse: true,\n    weights: { title: 5 }\n  }\n)')
  })

  it('merges extra options and skips empty key rows', () => {
    const f = { ...emptyForm(), keys: [{ field: 'a', type: '1' }, { field: ' ', type: '1' }], extra: '{ bits: 26 }' }
    expect(specText(f)).toBe('Object.assign({ key: { "a": 1 } }, { bits: 26 })')
    expect(commandText('c', f)).toBe('db.getCollection("c").createIndex(\n  { "a": 1 },\n  { bits: 26 }\n)')
  })

  it('round-trips a listed index without changes', () => {
    const f = fromSpec(listed)
    expect(specText(f)).toContain('expireAfterSeconds: 3600')
    expect(specText(f)).toContain('"2dsphereIndexVersion": 3')
  })
})

describe('needsRebuild', () => {
  const base = fromSpec(listed)
  it('allows TTL and hidden changes in place', () => {
    expect(needsRebuild(base, { ...base, ttl: '60' })).toBe(false)
    expect(needsRebuild(base, { ...base, hidden: true })).toBe(false)
  })
  it('rebuilds for anything else, including removing the TTL', () => {
    expect(needsRebuild(base, { ...base, unique: false })).toBe(true)
    expect(needsRebuild(base, { ...base, name: 'x' })).toBe(true)
    expect(needsRebuild(base, { ...base, keys: [...base.keys].reverse() })).toBe(true)
    expect(needsRebuild(base, { ...base, ttlEnabled: false })).toBe(true)
  })
})

describe('formatBytes', () => {
  it.each([
    [512, '512 B'],
    [2048, '2.0 KiB'],
    [32768, '32 KiB'],
    [5 * 1024 * 1024, '5.0 MiB'],
  ])('%i → %s', (n, s) => expect(formatBytes(n)).toBe(s))
})
