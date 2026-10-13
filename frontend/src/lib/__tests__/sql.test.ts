import { describe, expect, it } from 'vitest'
import type { Column } from '../api'
import { jsonValue, qualifiedName, quoteIdent, toCSV, toInsert, toJSON, toMarkdown, toTSV } from '../sql'

const cols: Column[] = [
  { name: 'id', type: 'INT8', kind: 'number' },
  { name: 'name', type: 'TEXT', kind: 'text' },
  { name: 'ok', type: 'BOOL', kind: 'bool' },
]
const rows = [
  ['1', 'a\tb', 'true'],
  ['2', null, 'false'],
]

describe('identifiers', () => {
  it('quotes only when needed, per dialect', () => {
    expect(quoteIdent('PostgreSQL', 'users')).toBe('users')
    expect(quoteIdent('PostgreSQL', 'Users')).toBe('"Users"')
    expect(quoteIdent('MySQL', 'my table')).toBe('`my table`')
    expect(quoteIdent('MSSQL', 'a]b')).toBe('[a]]b]')
    expect(quoteIdent('ClickHouse', 'Events')).toBe('Events')
    expect(quoteIdent('ClickHouse', 'my`table')).toBe('`my``table`')
    expect(quoteIdent('DuckDB', 'Users')).toBe('"Users"')
    expect(quoteIdent('PLSQL', 'EMP')).toBe('EMP')
    expect(qualifiedName('PostgreSQL', '', 'public', 'Order')).toBe('public."Order"')
  })
})

describe('copy formats', () => {
  it('writes TSV that spreadsheets accept', () => {
    expect(toTSV(cols, rows)).toBe('id\tname\tok\n1\t"a\tb"\ttrue\n2\t\tfalse')
  })
  it('writes CSV with quoting', () => {
    expect(toCSV(cols, [['1', 'x,y', 'true']])).toBe('id,name,ok\n1,"x,y",true')
  })
  it('writes JSON with typed values', () => {
    expect(JSON.parse(toJSON(cols, rows))).toEqual([
      { id: 1, name: 'a\tb', ok: true },
      { id: 2, name: null, ok: false },
    ])
    expect(jsonValue(cols[0], '12345678901234567890')).toBe('12345678901234567890')
  })
  it('writes INSERT statements', () => {
    expect(toInsert('PostgreSQL', 't', cols, [['1', "it's", 'true']])).toBe("INSERT INTO t (id, name, ok) VALUES (1, 'it''s', TRUE);")
  })
  it('writes Markdown tables', () => {
    expect(toMarkdown(cols, [['1', 'a|b', 'true']])).toBe('| id | name | ok |\n| ---: | --- | --- |\n| 1 | a\\|b | true |')
  })
})
