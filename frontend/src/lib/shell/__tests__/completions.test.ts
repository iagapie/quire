import { describe, expect, it } from 'vitest'
import { EditorState } from '@codemirror/state'
import { CompletionContext } from '@codemirror/autocomplete'
import { javascript } from '@codemirror/lang-javascript'
import { parseChain, inferKind, shellCompletions } from '../completions'

const names = { collections: ['users', 'orders', 'my-coll'], databases: ['admin', 'shop'] }

/** Labels offered with the cursor at `|` (or at the end). */
function labels(doc: string, explicit = false) {
  const pos = doc.includes('|') ? doc.indexOf('|') : doc.length
  doc = doc.replace('|', '')
  const state = EditorState.create({ doc, extensions: [javascript()] })
  const r = shellCompletions(() => names)(new CompletionContext(state, pos, explicit))
  return r ? r.options.map((o) => o.label) : null
}

const kind = (expr: string, before = '') => inferKind(parseChain(expr) ?? [], before + expr)

describe('shell completions', () => {
  it('completes db methods while typing a prefix', () => {
    expect(labels('db.getCo')).toEqual(expect.arrayContaining(['getCollection', 'getCollectionNames', 'getCollectionInfos']))
    expect(labels('db.')).toEqual(expect.arrayContaining(['getSiblingDB', 'runCommand', 'dropDatabase', 'users', 'orders']))
    expect(labels('db.')).not.toContain('my-coll')
  })

  it('completes collection methods after getCollection(...) and db.<name>', () => {
    for (const doc of ['db.getCollection("users").', 'db.getCollection("users").fi', 'db.users.', "db['my-coll'].", 'db.getSiblingDB("shop").orders.']) {
      const l = labels(doc)
      expect(l, doc).toEqual(expect.arrayContaining(['find', 'distinct', 'aggregate', 'insertOne', 'countDocuments']))
      expect(l, doc).not.toContain('toArray')
    }
  })

  it('completes cursor methods after find and aggregate', () => {
    for (const doc of ['db.users.find({a: 1}).', 'db.users.find({}).sort({a: -1}).limit(5).', 'db.getCollection("x").aggregate([{$match: {}}]).', 'db.users\n  .find()\n  .']) {
      expect(labels(doc), doc).toEqual(expect.arrayContaining(['sort', 'limit', 'toArray', 'forEach']))
    }
  })

  it('offers nothing after values that are not shell objects', () => {
    expect(labels('db.users.findOne().')).toBeNull()
    expect(labels('db.users.find().toArray().')).toBeNull()
    expect(labels('foo().')).toBeNull()
  })

  it('follows variables', () => {
    expect(kind('c', 'const c = db.getCollection("users")\n')).toBe('collection')
    expect(kind('cur', 'let cur = db.users.find({})\n')).toBe('cursor')
    expect(kind('other', 'const other = db.getSiblingDB("shop")\n')).toBe('db')
    expect(labels('const c = db.users\nc.')).toEqual(expect.arrayContaining(['find']))
  })

  it('completes collection and database names inside strings', () => {
    expect(labels('db.getCollection("us')).toEqual(['users', 'orders', 'my-coll'])
    expect(labels('use("')).toEqual(['admin', 'shop'])
    expect(labels("db.getSiblingDB('")).toEqual(['admin', 'shop'])
    expect(labels('print("db.')).toBeNull()
  })

  it('completes globals, declared variables and operators', () => {
    expect(labels('Obj')).toContain('ObjectId')
    expect(labels('const total = 1\nto')).toContain('total')
    expect(labels('db.users.find({ age: { $g')).toEqual(expect.arrayContaining(['$gt', '$gte']))
    expect(labels('ObjectId().')).toContain('getTimestamp')
    expect(labels('console.')).toContain('log')
    expect(labels('// db.')).toBeNull()
    expect(labels('')).toBeNull()
  })
})
