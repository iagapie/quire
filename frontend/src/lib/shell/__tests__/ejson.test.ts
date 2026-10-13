import { describe, expect, it } from 'vitest'
import { display, field, format, isContainer, parseEJSON, toCanonical, toRelaxed, typeName, type BVal } from '../ejson'

const doc = (src: object | string) => parseEJSON(typeof src === 'string' ? src : JSON.stringify(src))

describe('parseEJSON', () => {
  it('keeps key order, including integer-like keys that JSON.parse would reorder', () => {
    const v = doc('{"b":1,"10":2,"a":3,"2":4}')
    expect(v.t).toBe('object')
    expect(v.t === 'object' && v.v.map(([k]) => k)).toEqual(['b', '10', 'a', '2'])
  })

  it('decodes every canonical Extended JSON type', () => {
    const v = doc({
      oid: { $oid: '6ac59d2ab231244641a263b2' },
      i: { $numberInt: '-5' },
      l: { $numberLong: '9007199254740993' },
      d: { $numberDouble: '2.0' },
      dec: { $numberDecimal: '1.10' },
      date: { $date: { $numberLong: '0' } },
      relaxedDate: { $date: '1970-01-01T00:00:01Z' },
      re: { $regularExpression: { pattern: 'a/b', options: 'i' } },
      uuid: { $binary: { base64: 'WljLGUiQT32ayd70/7l9rg==', subType: '04' } },
      bin: { $binary: { base64: 'aGk=', subType: '00' } },
      ts: { $timestamp: { t: 1, i: 2 } },
      min: { $minKey: 1 },
      max: { $maxKey: 1 },
      undef: { $undefined: true },
      sym: { $symbol: 's' },
      code: { $code: 'x()' },
      s: 'str',
      b: false,
      n: null,
      arr: [1, 'x'],
      notSpecial: { $set: 1, other: 2 },
    })
    const types = Object.fromEntries((v as Extract<BVal, { t: 'object' }>).v.map(([k, x]) => [k, typeName(x)]))
    expect(types).toEqual({
      oid: 'ObjectId',
      i: 'Int32',
      l: 'Int64',
      d: 'Double',
      dec: 'Decimal128',
      date: 'Date',
      relaxedDate: 'Date',
      re: 'Regex',
      uuid: 'UUID',
      bin: 'Binary',
      ts: 'Timestamp',
      min: 'MinKey',
      max: 'MaxKey',
      undef: 'Undefined',
      sym: 'Symbol',
      code: 'Code',
      s: 'String',
      b: 'Boolean',
      n: 'Null',
      arr: 'Array',
      notSpecial: 'Object',
    })
  })

  it('handles escapes, unicode and nesting', () => {
    const v = doc('{"s":"a\\"b\\\\c\\n\\u00e9😀","nested":{"deep":[[{"x":[]}]]},"empty":{}}')
    expect(field(v, 's')).toEqual({ t: 'string', v: 'a"b\\c\né😀' })
    expect(field(v, 'empty')).toEqual({ t: 'object', v: [] })
    expect(field(v, 'missing')).toBeUndefined()
    expect(isContainer(field(v, 'nested')!)).toBe(true)
  })
})

describe('toCanonical', () => {
  it('round-trips canonical Extended JSON', () => {
    const src =
      '{"_id":{"$oid":"6ac59d2ab231244641a263b2"},"n":{"$numberLong":"5"},"d":{"$numberDouble":"1.5"},"at":{"$date":{"$numberLong":"1767323045006"}},"re":{"$regularExpression":{"pattern":"a\\\\.b","options":"i"}},"arr":[{"$numberInt":"1"},null,true,"x"],"ts":{"$timestamp":{"t":1,"i":2}},"bin":{"$binary":{"base64":"aGk=","subType":"00"}},"min":{"$minKey":1},"max":{"$maxKey":1}}'
    expect(toCanonical(parseEJSON(src))).toBe(src)
  })

  it('addresses a document by its _id', () => {
    const v = doc({ _id: { $numberInt: '7' }, x: 1 })
    expect(toCanonical(field(v, '_id')!)).toBe('{"$numberInt":"7"}')
  })
})

describe('format (shell syntax)', () => {
  it('writes values the backend shell can parse back', () => {
    const v = doc({
      _id: { $oid: '6ac59d2ab231244641a263b2' },
      'a-b': { $numberInt: '1' },
      l: { $numberLong: '5' },
      d: { $numberDouble: '3' },
      f: { $numberDouble: '0.5' },
      nan: { $numberDouble: 'NaN' },
      dec: { $numberDecimal: '1.10' },
      at: { $date: { $numberLong: '0' } },
      re: { $regularExpression: { pattern: 'a/b', options: 'i' } },
      uuid: { $binary: { base64: 'WljLGUiQT32ayd70/7l9rg==', subType: '04' } },
      bin: { $binary: { base64: 'aGk=', subType: '05' } },
    })
    expect(format(v, '')).toBe(
      '{ _id: ObjectId("6ac59d2ab231244641a263b2"), "a-b": 1, l: NumberLong(5), d: Double(3), f: 0.5, nan: NaN, dec: NumberDecimal("1.10"), at: ISODate("1970-01-01T00:00:00.000Z"), re: /a\\/b/i, uuid: UUID("5a58cb19-4890-4f7d-9ac9-def4ffb97dae"), bin: BinData(5, "aGk=") }',
    )
  })

  it('indents nested containers', () => {
    expect(format(doc({ a: [{ $numberInt: '1' }, { b: [] }], c: {} }))).toBe('{\n  a: [\n    1,\n    {\n      b: []\n    }\n  ],\n  c: {}\n}')
  })
})

describe('display', () => {
  it('summarises values for the grid', () => {
    const v = doc({
      o: { a: 1, b: 2 },
      one: { a: 1 },
      arr: [1],
      d: { $numberDouble: '2' },
      at: { $date: { $numberLong: '86400000' } },
      uuid: { $binary: { base64: 'WljLGUiQT32ayd70/7l9rg==', subType: '04' } },
      re: { $regularExpression: { pattern: 'x', options: 'm' } },
    })
    const shown = Object.fromEntries((v as Extract<BVal, { t: 'object' }>).v.map(([k, x]) => [k, display(x)]))
    expect(shown).toEqual({
      o: '{ 2 fields }',
      one: '{ 1 field }',
      arr: '[ 1 element ]',
      d: '2.0',
      at: '1970-01-02T00:00:00.000Z',
      uuid: '5a58cb19-4890-4f7d-9ac9-def4ffb97dae',
      re: '/x/m',
    })
  })
})

describe('toRelaxed', () => {
  it('produces plain JSON for copying', () => {
    const v = doc({
      _id: { $oid: '6ac59d2ab231244641a263b2' },
      n: { $numberInt: '1' },
      safe: { $numberLong: '42' },
      big: { $numberLong: '9007199254740993' },
      at: { $date: { $numberLong: '0' } },
      list: [null, true],
    })
    expect(toRelaxed(v)).toEqual({
      _id: { $oid: '6ac59d2ab231244641a263b2' },
      n: 1,
      safe: 42,
      big: { $numberLong: '9007199254740993' },
      at: { $date: '1970-01-01T00:00:00.000Z' },
      list: [null, true],
    })
  })
})
