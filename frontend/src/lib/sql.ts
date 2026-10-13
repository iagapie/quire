// SQL text helpers: identifier quoting, formatting, and turning result rows into the
// formats the grid copies (TSV for spreadsheets, CSV, JSON, INSERT, Markdown).
import { format as formatSQL, type SqlLanguage } from 'sql-formatter'
import type { Column } from './api'

type Cell = string | null

/** Quotes an identifier for the editor dialect, only when needed. */
export function quoteIdent(editor: string, name: string): string {
  const plain =
    editor === 'PLSQL'
      ? /^[A-Z_][A-Z0-9_$#]*$/
      : editor === 'PostgreSQL' || editor === 'Cosmos' || editor === 'DuckDB'
        ? /^[a-z_][a-z0-9_$]*$/
        : /^[A-Za-z_][A-Za-z0-9_$]*$/
  if (plain.test(name)) return name
  switch (editor) {
    case 'MySQL':
    case 'MariaSQL':
    case 'ClickHouse':
      return '`' + name.replaceAll('`', '``') + '`'
    case 'MSSQL':
      return '[' + name.replaceAll(']', ']]') + ']'
  }
  return '"' + name.replaceAll('"', '""') + '"'
}

/** Joins the non-empty parts of a name, quoting where needed. */
export function qualifiedName(editor: string, ...parts: string[]): string {
  return parts
    .filter(Boolean)
    .map((p) => quoteIdent(editor, p))
    .join('.')
}

const FORMATTER_LANGUAGE: Record<string, SqlLanguage> = {
  PostgreSQL: 'postgresql',
  MySQL: 'mysql',
  MariaSQL: 'mariadb',
  MSSQL: 'transactsql',
  SQLite: 'sqlite',
  PLSQL: 'plsql',
  StandardSQL: 'sql',
  Cosmos: 'sql',
  ClickHouse: 'clickhouse',
  DuckDB: 'duckdb',
}

/** Formats SQL for the dialect; returns the text unchanged when it cannot be parsed. */
export function prettySQL(text: string, editor: string): string {
  try {
    return formatSQL(text, { language: FORMATTER_LANGUAGE[editor] ?? 'sql', keywordCase: 'upper', tabWidth: 4, linesBetweenQueries: 1 })
  } catch {
    return text
  }
}

/** A cell as a JSON value: numbers and booleans keep their type when safe. */
export function jsonValue(col: Column, v: Cell): unknown {
  if (v === null) return null
  if (col.kind === 'number') {
    const n = Number(v)
    if (Number.isFinite(n) && (Number.isSafeInteger(n) || !/^-?\d+$/.test(v)) && String(n) === v.replace(/^(-?)0+(?=\d)/, '$1')) return n
    return v
  }
  if (col.kind === 'bool') return v === 'true' || v === '1' || v === 't'
  if (col.kind === 'json') {
    try {
      return JSON.parse(v)
    } catch {
      return v
    }
  }
  return v
}

export function rowObject(cols: Column[], row: Cell[]): Record<string, unknown> {
  const o: Record<string, unknown> = {}
  cols.forEach((c, i) => {
    o[c.name] = jsonValue(c, row[i])
  })
  return o
}

const tsvCell = (v: Cell) => (v === null ? '' : /[\t\n\r"]/.test(v) ? '"' + v.replaceAll('"', '""') + '"' : v)
const csvCell = (v: Cell, delim = ',') => (v === null ? '' : v.includes(delim) || /[\n\r"]/.test(v) ? '"' + v.replaceAll('"', '""') + '"' : v)

/** Tab-separated values with a header; pastes into Excel and Numbers. */
export function toTSV(cols: Column[], rows: Cell[][], header = true): string {
  const lines = rows.map((r) => r.map(tsvCell).join('\t'))
  if (header) lines.unshift(cols.map((c) => tsvCell(c.name)).join('\t'))
  return lines.join('\n')
}

export function toCSV(cols: Column[], rows: Cell[][]): string {
  return [cols.map((c) => csvCell(c.name)).join(','), ...rows.map((r) => r.map((v) => csvCell(v)).join(','))].join('\n')
}

export function toJSON(cols: Column[], rows: Cell[][]): string {
  const objs = rows.map((r) => rowObject(cols, r))
  return JSON.stringify(objs.length === 1 ? objs[0] : objs, null, 2)
}

export function toMarkdown(cols: Column[], rows: Cell[][]): string {
  const esc = (v: Cell) => (v === null ? 'NULL' : v.replaceAll('|', '\\|').replace(/\r?\n/g, ' '))
  return [
    '| ' + cols.map((c) => esc(c.name)).join(' | ') + ' |',
    '| ' + cols.map((c) => (c.kind === 'number' ? '---:' : '---')).join(' | ') + ' |',
    ...rows.map((r) => '| ' + r.map(esc).join(' | ') + ' |'),
  ].join('\n')
}

/** A SQL literal for a cell. */
export function literal(col: Column, v: Cell): string {
  if (v === null) return 'NULL'
  if (col.kind === 'number' && /^-?\d+(\.\d+)?(e[+-]?\d+)?$/i.test(v)) return v
  if (col.kind === 'bool' && (v === 'true' || v === 'false')) return v.toUpperCase()
  if (col.kind === 'binary' && v.startsWith('\\x')) return `'${v}'`
  return "'" + v.replaceAll("'", "''") + "'"
}

/** One INSERT statement per row. */
export function toInsert(editor: string, table: string, cols: Column[], rows: Cell[][]): string {
  const names = cols.map((c) => quoteIdent(editor, c.name)).join(', ')
  return rows.map((r) => `INSERT INTO ${table} (${names}) VALUES (${r.map((v, i) => literal(cols[i], v)).join(', ')});`).join('\n')
}
