import { beforeEach, describe, expect, it } from 'vitest'
import { resetWidthCache, saveWidth, savedWidths, widthKey } from '../colwidths'

describe('column widths', () => {
  beforeEach(() => {
    resetWidthCache()
    globalThis.localStorage?.clear()
  })

  it('keys by connection, database, schema and table', () => {
    expect(widthKey('c1', 'db', 'public', 'users')).not.toBe(widthKey('c2', 'db', 'public', 'users'))
    expect(widthKey('c1', 'db', 'public', 'users')).not.toBe(widthKey('c1', 'db', 'other', 'users'))
    expect(widthKey('c1', 'db', '', '')).toBe('')
  })

  it('saves, keeps apart and forgets widths', () => {
    const users = widthKey('c', 'db', '', 'users')
    const orders = widthKey('c', 'db', '', 'orders')
    saveWidth(users, 'email', 120.4)
    saveWidth(orders, 'email', 300)
    expect(savedWidths(users)).toEqual({ email: 120 })
    expect(savedWidths(orders)).toEqual({ email: 300 })
    saveWidth(users, 'email', null)
    expect(savedWidths(users)).toEqual({})
    expect(savedWidths(orders)).toEqual({ email: 300 })
  })

  it('ignores results that are not a table', () => {
    saveWidth('', 'email', 100)
    expect(savedWidths('')).toEqual({})
  })
})
