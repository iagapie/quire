import { describe, expect, it } from 'vitest'
import { cn, formatDuration, uid } from '../utils'

describe('formatDuration', () => {
  it.each([
    [0, '0 ms'],
    [999, '999 ms'],
    [1234, '1.23 s'],
    [12_345, '12.3 s'],
    [61_000, '1m 1s'],
    [3_599_000, '59m 59s'],
  ])('%i ms → %s', (ms, want) => {
    expect(formatDuration(ms)).toBe(want)
  })
})

describe('uid', () => {
  it('is unique and prefixed', () => {
    const ids = new Set(Array.from({ length: 1000 }, () => uid('tab')))
    expect(ids.size).toBe(1000)
    expect([...ids].every((id) => id.startsWith('tab-'))).toBe(true)
  })
})

describe('cn', () => {
  it('joins truthy class names', () => {
    expect(cn('a', false, undefined, 'b', { c: true, d: false })).toBe('a b c')
  })
})
