import { describe, expect, it } from 'vitest'
import { parseEJSON } from '../../../../lib/shell/ejson'
import { docsText, menuTargets } from '../common'

const a = parseEJSON('{"_id":{"$oid":"6ac59d2ab231244641a263b2"},"n":{"$numberLong":"5"},"tags":["x"]}')
const b = parseEJSON('{"_id":{"$numberInt":"2"},"ok":true}')

describe('docsText', () => {
  it('copies one document as a document', () => {
    expect(docsText([a])).toBe('{\n  _id: ObjectId("6ac59d2ab231244641a263b2"),\n  n: NumberLong(5),\n  tags: [\n    "x"\n  ]\n}')
  })

  it('copies several documents as an array, nested indentation included', () => {
    expect(docsText([a, b])).toBe(
      '[\n  {\n    _id: ObjectId("6ac59d2ab231244641a263b2"),\n    n: NumberLong(5),\n    tags: [\n      "x"\n    ]\n  },\n  {\n    _id: 2,\n    ok: true\n  }\n]',
    )
  })

  it('copies JSON', () => {
    expect(JSON.parse(docsText([b], true))).toEqual({ _id: 2, ok: true })
    expect(JSON.parse(docsText([a, b], true))).toEqual([
      { _id: { $oid: '6ac59d2ab231244641a263b2' }, n: 5, tags: ['x'] },
      { _id: 2, ok: true },
    ])
  })
})

describe('menuTargets', () => {
  it('acts on the whole selection when the row is part of it', () => {
    expect(menuTargets(new Set([4, 1, 2]), 2)).toEqual([1, 2, 4])
  })
  it('acts on the clicked row otherwise', () => {
    expect(menuTargets(new Set([1, 2]), 7)).toEqual([7])
    expect(menuTargets(new Set([3]), 3)).toEqual([3])
  })
})
