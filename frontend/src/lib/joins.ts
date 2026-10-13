// Join completion from foreign keys: after JOIN, the tables related to those already in
// the statement (with an alias and the ON condition); after ON, the conditions.

export interface ForeignKeyRef {
  table: string
  columns: string[] | null
  refSchema?: string
  refTable: string
  refColumns: string[] | null
}

export interface TableRef {
  /** The table name, unquoted, without its schema. */
  name: string
  /** The alias, or the name when there is none. */
  alias: string
}

export interface JoinSuggestion {
  label: string
  detail: string
  apply: string
}

const KEYWORDS = new Set(
  'on using where join left right inner outer full cross natural group order limit offset fetch having union except intersect window as set values returning lateral and or for'.split(' '),
)

const IDENT = String.raw`(?:"(?:[^"]|"")+"|\`(?:[^\`]|\`\`)+\`|\[(?:[^\]]|\]\])+\]|[\w$#]+)`
const NAME = String.raw`${IDENT}(?:\s*\.\s*${IDENT}){0,2}`

function unquote(part: string): string {
  const p = part.trim()
  if ((p.startsWith('"') && p.endsWith('"')) || (p.startsWith('`') && p.endsWith('`'))) return p.slice(1, -1)
  if (p.startsWith('[') && p.endsWith(']')) return p.slice(1, -1)
  return p
}

/** The last part of a possibly qualified, possibly quoted name. */
export function baseName(name: string): string {
  const parts = name.match(new RegExp(IDENT, 'g')) ?? [name]
  return unquote(parts[parts.length - 1])
}

/** The tables of FROM and JOIN clauses in a statement, with their aliases. */
export function tablesInStatement(text: string): TableRef[] {
  const out: TableRef[] = []
  const re = new RegExp(String.raw`\b(?:from|join)\s+(${NAME})(?:\s+(?:as\s+)?(${IDENT}))?`, 'gi')
  for (const m of text.matchAll(re)) {
    const name = baseName(m[1])
    let alias = m[2] ? unquote(m[2]) : ''
    if (alias && KEYWORDS.has(alias.toLowerCase())) alias = ''
    out.push({ name, alias: alias || name })
  }
  return out
}

const same = (a: string, b: string) => a.toLowerCase() === b.toLowerCase()

/** A short alias for a table: the initials of its words, made unique. */
export function aliasFor(table: string, taken: string[]): string {
  const words = table.split(/[_\s-]+|(?<=[a-z])(?=[A-Z])/).filter(Boolean)
  const base = (words.length > 1 ? words.map((w) => w[0]).join('') : table.slice(0, 1)).toLowerCase() || 't'
  const used = new Set(taken.map((t) => t.toLowerCase()))
  if (!used.has(base) && !KEYWORDS.has(base)) return base
  for (let i = 2; ; i++) if (!used.has(base + i)) return base + i
}

function condition(fk: ForeignKeyRef, childAlias: string, parentAlias: string, quote: (s: string) => string): string {
  const cols = fk.columns ?? []
  const refs = fk.refColumns ?? []
  return cols.map((c, i) => `${quote(parentAlias)}.${quote(refs[i] ?? '')} = ${quote(childAlias)}.${quote(c)}`).join(' AND ')
}

/** Links between table a and table b, in either direction. */
function links(joins: ForeignKeyRef[], a: string, b: string) {
  const out: { fk: ForeignKeyRef; aIsChild: boolean }[] = []
  for (const fk of joins) {
    if (same(fk.table, a) && same(fk.refTable, b)) out.push({ fk, aIsChild: true })
    else if (same(fk.table, b) && same(fk.refTable, a)) out.push({ fk, aIsChild: false })
  }
  return out
}

/**
 * Suggestions for the text before the cursor (the current statement up to it):
 * after "JOIN <word>" the related tables, after "JOIN t [alias] ON <word>" the conditions.
 */
export function joinSuggestions(before: string, joins: ForeignKeyRef[], quote: (s: string) => string = (s) => s): JoinSuggestion[] {
  if (joins.length === 0) return []
  const onMatch = new RegExp(String.raw`\bjoin\s+(${NAME})(?:\s+(?:as\s+)?(${IDENT}))?\s+on\s+[\w$]*$`, 'i').exec(before)
  if (onMatch) {
    const target = baseName(onMatch[1])
    const targetAlias = onMatch[2] && !KEYWORDS.has(onMatch[2].toLowerCase()) ? unquote(onMatch[2]) : target
    const earlier = tablesInStatement(before.slice(0, onMatch.index))
    const out: JoinSuggestion[] = []
    for (const t of earlier) {
      for (const { fk, aIsChild } of links(joins, target, t.name)) {
        const cond = aIsChild ? condition(fk, targetAlias, t.alias, quote) : condition(fk, t.alias, targetAlias, quote)
        out.push({ label: cond, detail: `foreign key ${fk.table} → ${fk.refTable}`, apply: cond })
      }
    }
    return out
  }
  if (!/\bjoin\s+[\w$]*$/i.test(before)) return []
  const present = tablesInStatement(before)
  if (present.length === 0) return []
  const taken = present.map((t) => t.alias)
  const out: JoinSuggestion[] = []
  const seen = new Set<string>()
  for (const t of present) {
    for (const fk of joins) {
      let other = ''
      let otherIsChild = false
      if (same(fk.refTable, t.name)) {
        other = fk.table
        otherIsChild = true
      } else if (same(fk.table, t.name)) {
        other = fk.refTable
      } else continue
      const alias = aliasFor(other, taken)
      const cond = otherIsChild ? condition(fk, alias, t.alias, quote) : condition(fk, t.alias, alias, quote)
      const apply = `${quote(other)} ${alias} ON ${cond}`
      if (seen.has(apply)) continue
      seen.add(apply)
      out.push({ label: `${other} ${alias} ON …`, detail: cond, apply })
    }
  }
  return out
}
