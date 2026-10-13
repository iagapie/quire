import { describe, expect, it } from 'vitest'
import { display, field, format, parseJSON, toJVal, typeName } from '../jsonval'

describe('jsonval', () => {
  it('keeps key order and number text', () => {
    const v = parseJSON('{"b": 1, "2": 12345678901234567890, "a": [true, null, "x"]}')
    expect(v.t === 'object' && v.v.map(([k]) => k)).toEqual(['b', '2', 'a'])
    expect(field(v, '2')).toEqual({ t: 'number', v: '12345678901234567890' })
    expect(format(v, '')).toBe('{"b": 1, "2": 12345678901234567890, "a": [true, null, "x"]}')
  })
  it('describes values', () => {
    const v = parseJSON('{"o": {"x": 1}, "s": "é\\n"}')
    expect(display(field(v, 'o')!)).toBe('{ 1 field }')
    expect(field(v, 's')).toEqual({ t: 'string', v: 'é\n' })
    expect(typeName(field(v, 'o')!)).toBe('Object')
  })
  it('falls back to text for non-JSON', () => {
    expect(toJVal('not json')).toEqual({ t: 'string', v: 'not json' })
    expect(toJVal(null)).toEqual({ t: 'null' })
    expect(() => parseJSON('{"a": 1} x')).toThrow()
  })
})
