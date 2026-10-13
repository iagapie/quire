// Splits SQL scripts into statements, mirroring internal/sqlsplit in the backend, so
// the editor can highlight the statement under the cursor exactly as it will run.

export interface SplitOptions {
  hashComments?: boolean
  backticks?: boolean
  brackets?: boolean
  dollarQuotes?: boolean
  nestedComments?: boolean
  backslashEscapes?: boolean
  eStrings?: boolean
  qQuotes?: boolean
  goBatches?: boolean
  slashTerminator?: boolean
  delimiter?: boolean
  blockStatements?: boolean
  blankLines?: boolean
}

export interface Statement {
  text: string
  /** Offsets of text in the script (UTF-16 code units, as in the editor). */
  from: number
  to: number
  line: number
}

/** Splitting rules of each editor dialect. */
export const DIALECT_SPLIT: Record<string, SplitOptions> = {
  PostgreSQL: { dollarQuotes: true, nestedComments: true, eStrings: true },
  MySQL: { hashComments: true, backticks: true, backslashEscapes: true, delimiter: true, blockStatements: true },
  MariaSQL: { hashComments: true, backticks: true, backslashEscapes: true, delimiter: true, blockStatements: true },
  SQLite: { backticks: true, brackets: true, blockStatements: true },
  MSSQL: { brackets: true, goBatches: true },
  PLSQL: { qQuotes: true, slashTerminator: true },
  Cosmos: {},
  ClickHouse: { hashComments: true, backticks: true, backslashEscapes: true, dollarQuotes: true },
  DuckDB: { dollarQuotes: true },
  StandardSQL: {},
}

const isSpace = (c: string) => c === ' ' || c === '\n' || c === '\t' || c === '\r' || c === '\f' || c === '\v'
const isDigit = (c: string) => c >= '0' && c <= '9'
const identStart = (c: string) => c === '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c.charCodeAt(0) >= 0x80
const identChar = (c: string) => identStart(c) || isDigit(c) || c === '$'

export function split(src: string, o: SplitOptions = {}): Statement[] {
  const out: Statement[] = []
  let delim = ';'
  let start = 0
  let head: string[] = []
  let depth = 0
  let code = false

  const at = (i: number) => (i < src.length ? src[i] : '')
  const lineEnd = (i: number) => {
    const n = src.indexOf('\n', i)
    return n < 0 ? src.length : n
  }
  const blockComment = (i: number) => {
    let d = 0
    let j = i
    while (j < src.length) {
      if (src[j] === '/' && at(j + 1) === '*') {
        if (d === 0 || o.nestedComments) d++
        j += 2
      } else if (src[j] === '*' && at(j + 1) === '/') {
        d--
        j += 2
        if (d === 0) return j
      } else j++
    }
    return src.length
  }
  const quoted = (i: number, closer: string, backslash: boolean) => {
    let j = i + 1
    while (j < src.length) {
      const c = src[j]
      if (backslash && c === '\\') j += 2
      else if (c === closer) {
        if (at(j + 1) === closer) {
          j += 2
          continue
        }
        return j + 1
      } else j++
    }
    return src.length
  }
  const blockHead = () => head.length >= 2 && head[0] === 'CREATE' && head.slice(1).some((w) => w === 'TRIGGER' || w === 'PROCEDURE' || w === 'FUNCTION' || w === 'EVENT')
  const plsql = () => {
    if (!o.slashTerminator || head.length === 0) return false
    if (head[0] === 'DECLARE' || head[0] === 'BEGIN') return true
    if (head[0] !== 'CREATE') return false
    for (const w of head.slice(1)) {
      if (['PROCEDURE', 'FUNCTION', 'PACKAGE', 'TRIGGER', 'TYPE'].includes(w)) return true
      if (['OR', 'REPLACE', 'EDITIONABLE', 'NONEDITIONABLE', 'EDITIONING', 'BODY'].includes(w)) continue
      return false
    }
    return false
  }
  const emit = (end: number) => {
    let a = start
    let b = end
    for (;;) {
      while (a < b && isSpace(src[a])) a++
      if (a + 1 < b && src[a] === '-' && src[a + 1] === '-') {
        a = Math.min(lineEnd(a), b)
        continue
      }
      if (a < b && src[a] === '#' && o.hashComments) {
        a = Math.min(lineEnd(a), b)
        continue
      }
      if (a + 1 < b && src[a] === '/' && src[a + 1] === '*') {
        a = Math.min(blockComment(a), b)
        continue
      }
      break
    }
    while (b > a && isSpace(src[b - 1])) b--
    if (a >= b) return
    let line = 1
    for (let i = 0; i < a; i++) if (src[i] === '\n') line++
    out.push({ text: src.slice(a, b), from: a, to: b, line })
  }
  const reset = (i: number) => {
    start = i
    head = []
    depth = 0
    code = false
  }
  const isGo = (line: string) => /^go(\s+\d+)?(\s*--.*)?$/i.test(line)
  const nextWord = (i: number) => {
    while (i < src.length && isSpace(src[i])) i++
    let j = i
    while (j < src.length && identChar(src[j])) j++
    return src.slice(i, j).toUpperCase()
  }

  let i = 0
  while (i < src.length) {
    if (i === 0 || src[i - 1] === '\n') {
      const end = lineEnd(i)
      const line = src.slice(i, end).trim()
      const next = Math.min(end + 1, src.length)
      if (o.goBatches && isGo(line)) {
        emit(i)
        reset(next)
        i = next
        continue
      }
      if (o.slashTerminator && line === '/') {
        emit(i)
        reset(next)
        i = next
        continue
      }
      if (o.delimiter && depth === 0 && /^delimiter\s+\S/i.test(line)) {
        emit(i)
        delim = line.slice(10).trim()
        reset(next)
        i = next
        continue
      }
      if (o.blankLines && line === '' && code && depth === 0 && !plsql()) {
        emit(i)
        reset(next)
        i = next
        continue
      }
    }
    const c = src[i]
    if (isSpace(c)) i++
    else if (c === '-' && at(i + 1) === '-') i = lineEnd(i)
    else if (c === '#' && o.hashComments) i = lineEnd(i)
    else if (c === '/' && at(i + 1) === '*') i = blockComment(i)
    else if (c === "'") {
      code = true
      const e = o.eStrings && (src[i - 1] === 'e' || src[i - 1] === 'E') && !(i >= 2 && identChar(src[i - 2]))
      i = quoted(i, "'", !!o.backslashEscapes || !!e)
    } else if ((c === 'q' || c === 'Q') && o.qQuotes && at(i + 1) === "'" && !(i > 0 && identChar(src[i - 1]))) {
      code = true
      const open = at(i + 2)
      const closer = ({ '[': ']', '{': '}', '(': ')', '<': '>' } as Record<string, string>)[open] ?? open
      let j = i + 3
      while (j < src.length && !(src[j] === closer && at(j + 1) === "'")) j++
      i = Math.min(j + 2, src.length)
    } else if (c === '"') {
      code = true
      i = quoted(i, '"', false)
    } else if (c === '`' && o.backticks) {
      code = true
      i = quoted(i, '`', false)
    } else if (c === '[' && o.brackets) {
      code = true
      i = quoted(i, ']', false)
    } else if (c === '$' && o.dollarQuotes && !(i > 0 && identChar(src[i - 1]))) {
      code = true
      let j = i + 1
      while (j < src.length && (identStart(src[j]) || (j > i + 1 && isDigit(src[j])))) j++
      if (j < src.length && src[j] === '$') {
        const tag = src.slice(i, j + 1)
        const n = src.indexOf(tag, j + 1)
        i = n < 0 ? src.length : n + tag.length
      } else i++
    } else if (delim !== ';' && src.startsWith(delim, i)) {
      emit(i)
      i += delim.length
      reset(i)
    } else if (c === ';' && delim === ';') {
      if (o.goBatches || depth > 0 || plsql()) {
        i++
        continue
      }
      emit(i)
      i++
      reset(i)
    } else if (identStart(c)) {
      code = true
      let j = i
      while (j < src.length && identChar(src[j])) {
        if (delim !== ';' && j > i && src.startsWith(delim, j)) break
        j++
      }
      const w = src.slice(i, j).toUpperCase()
      if (head.length < 8) head.push(w)
      if (o.blockStatements && blockHead()) {
        if (w === 'BEGIN' || w === 'CASE') depth++
        else if (w === 'END' && depth > 0 && !['IF', 'LOOP', 'WHILE', 'REPEAT', 'FOR'].includes(nextWord(j))) depth--
      }
      i = j
    } else {
      code = true
      i++
    }
  }
  emit(src.length)
  return out
}

/** The statement under the cursor, chosen as the backend does. */
export function statementAt(src: string, stmts: Statement[], cursor: number): Statement | null {
  const lineOf = (pos: number) => {
    let n = 1
    for (let i = 0; i < Math.min(pos, src.length); i++) if (src[i] === '\n') n++
    return n
  }
  const line = lineOf(cursor)
  let before: Statement | null = null
  for (const st of stmts) {
    if (cursor >= st.from && cursor <= st.to) return st
    if (st.to <= cursor) before = st
  }
  if (before && lineOf(before.to) === line) return before
  const same = stmts.find((s) => s.line === line)
  if (same) return same
  return before ?? (stmts[0] && lineOf(stmts[0].from) === line ? stmts[0] : null)
}
