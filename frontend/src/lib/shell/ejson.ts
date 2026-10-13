// Canonical Extended JSON → typed, order-preserving values, plus shell-syntax formatting.

export type BVal =
  | { t: 'object'; v: [string, BVal][] }
  | { t: 'array'; v: BVal[] }
  | { t: 'string'; v: string }
  | { t: 'int32' | 'int64' | 'double' | 'decimal'; v: string }
  | { t: 'bool'; v: boolean }
  | { t: 'null' }
  | { t: 'undefined' }
  | { t: 'oid'; v: string }
  | { t: 'date'; v: number }
  | { t: 'regex'; pattern: string; options: string }
  | { t: 'binary'; base64: string; subType: string }
  | { t: 'timestamp'; ts: number; inc: number }
  | { t: 'minKey' | 'maxKey' }
  | { t: 'code'; v: string }
  | { t: 'symbol'; v: string }
  | { t: 'other'; v: string }

type Raw = string | number | boolean | null | Raw[] | RawObj
type RawObj = { __entries: [string, Raw][] }

// Minimal JSON parser that keeps object key order (JSON.parse reorders integer-like keys).
function parseOrdered(src: string): Raw {
  let i = 0
  const ws = () => {
    while (i < src.length && (src[i] === ' ' || src[i] === '\n' || src[i] === '\r' || src[i] === '\t')) i++
  }
  const str = (): string => {
    const start = i
    i++
    let simple = true
    while (src[i] !== '"') {
      if (src[i] === '\\') {
        simple = false
        i++
      }
      i++
    }
    i++
    const s = src.slice(start, i)
    return simple ? s.slice(1, -1) : JSON.parse(s)
  }
  const value = (): Raw => {
    ws()
    const c = src[i]
    if (c === '{') {
      i++
      const entries: [string, Raw][] = []
      ws()
      if (src[i] === '}') {
        i++
        return { __entries: entries }
      }
      for (;;) {
        ws()
        const k = str()
        ws()
        i++ // :
        entries.push([k, value()])
        ws()
        if (src[i++] === '}') return { __entries: entries }
      }
    }
    if (c === '[') {
      i++
      const arr: Raw[] = []
      ws()
      if (src[i] === ']') {
        i++
        return arr
      }
      for (;;) {
        arr.push(value())
        ws()
        if (src[i++] === ']') return arr
      }
    }
    if (c === '"') return str()
    if (src.startsWith('true', i)) {
      i += 4
      return true
    }
    if (src.startsWith('false', i)) {
      i += 5
      return false
    }
    if (src.startsWith('null', i)) {
      i += 4
      return null
    }
    const start = i
    while (i < src.length && /[-+0-9.eE]/.test(src[i])) i++
    return Number(src.slice(start, i))
  }
  return value()
}

const isObj = (r: Raw): r is RawObj => typeof r === 'object' && r !== null && !Array.isArray(r)
const get = (o: RawObj, k: string) => o.__entries.find((e) => e[0] === k)?.[1]

function convert(r: Raw): BVal {
  if (r === null) return { t: 'null' }
  if (typeof r === 'string') return { t: 'string', v: r }
  if (typeof r === 'boolean') return { t: 'bool', v: r }
  if (typeof r === 'number') return { t: 'double', v: String(r) }
  if (Array.isArray(r)) return { t: 'array', v: r.map(convert) }
  const e = r.__entries
  if (e.length >= 1 && e[0][0].startsWith('$')) {
    const [k, v] = e[0]
    if (e.length === 1) {
      switch (k) {
        case '$oid':
          return { t: 'oid', v: v as string }
        case '$numberInt':
          return { t: 'int32', v: v as string }
        case '$numberLong':
          return { t: 'int64', v: v as string }
        case '$numberDouble':
          return { t: 'double', v: v as string }
        case '$numberDecimal':
          return { t: 'decimal', v: v as string }
        case '$date': {
          if (isObj(v)) return { t: 'date', v: Number(get(v, '$numberLong')) }
          return { t: 'date', v: typeof v === 'string' ? Date.parse(v) : (v as number) }
        }
        case '$regularExpression':
          if (isObj(v)) return { t: 'regex', pattern: String(get(v, 'pattern')), options: String(get(v, 'options') ?? '') }
          break
        case '$binary':
          if (isObj(v)) return { t: 'binary', base64: String(get(v, 'base64')), subType: String(get(v, 'subType')) }
          break
        case '$timestamp':
          if (isObj(v)) return { t: 'timestamp', ts: Number(get(v, 't')), inc: Number(get(v, 'i')) }
          break
        case '$minKey':
          return { t: 'minKey' }
        case '$maxKey':
          return { t: 'maxKey' }
        case '$undefined':
          return { t: 'undefined' }
        case '$symbol':
          return { t: 'symbol', v: String(v) }
        case '$code':
          return { t: 'code', v: String(v) }
      }
    }
    if (k === '$code') return { t: 'code', v: String(v) }
    if (k === '$dbPointer') return { t: 'other', v: JSON.stringify(v) }
  }
  return { t: 'object', v: e.map(([k, v]) => [k, convert(v)]) }
}

export function parseEJSON(src: string): BVal {
  return convert(parseOrdered(src))
}

export function field(doc: BVal, key: string): BVal | undefined {
  return doc.t === 'object' ? doc.v.find((e) => e[0] === key)?.[1] : undefined
}

// Canonical Extended JSON of a value (used to address documents by _id).
export function toCanonical(v: BVal): string {
  switch (v.t) {
    case 'object':
      return '{' + v.v.map(([k, x]) => JSON.stringify(k) + ':' + toCanonical(x)).join(',') + '}'
    case 'array':
      return '[' + v.v.map(toCanonical).join(',') + ']'
    case 'string':
      return JSON.stringify(v.v)
    case 'int32':
      return `{"$numberInt":"${v.v}"}`
    case 'int64':
      return `{"$numberLong":"${v.v}"}`
    case 'double':
      return `{"$numberDouble":"${v.v}"}`
    case 'decimal':
      return `{"$numberDecimal":"${v.v}"}`
    case 'bool':
      return String(v.v)
    case 'null':
      return 'null'
    case 'undefined':
      return '{"$undefined":true}'
    case 'oid':
      return `{"$oid":"${v.v}"}`
    case 'date':
      return `{"$date":{"$numberLong":"${v.v}"}}`
    case 'regex':
      return `{"$regularExpression":{"pattern":${JSON.stringify(v.pattern)},"options":"${v.options}"}}`
    case 'binary':
      return `{"$binary":{"base64":"${v.base64}","subType":"${v.subType}"}}`
    case 'timestamp':
      return `{"$timestamp":{"t":${v.ts},"i":${v.inc}}}`
    case 'minKey':
      return '{"$minKey":1}'
    case 'maxKey':
      return '{"$maxKey":1}'
    case 'code':
      return `{"$code":${JSON.stringify(v.v)}}`
    case 'symbol':
      return `{"$symbol":${JSON.stringify(v.v)}}`
    case 'other':
      return v.v
  }
}

const TYPE_NAMES: Record<BVal['t'], string> = {
  object: 'Object',
  array: 'Array',
  string: 'String',
  int32: 'Int32',
  int64: 'Int64',
  double: 'Double',
  decimal: 'Decimal128',
  bool: 'Boolean',
  null: 'Null',
  undefined: 'Undefined',
  oid: 'ObjectId',
  date: 'Date',
  regex: 'Regex',
  binary: 'Binary',
  timestamp: 'Timestamp',
  minKey: 'MinKey',
  maxKey: 'MaxKey',
  code: 'Code',
  symbol: 'Symbol',
  other: 'Other',
}

export function typeName(v: BVal): string {
  if (v.t === 'binary' && (v.subType === '04' || v.subType === '03')) return 'UUID'
  return TYPE_NAMES[v.t]
}

export function isContainer(v: BVal): v is Extract<BVal, { t: 'object' | 'array' }> {
  return v.t === 'object' || v.t === 'array'
}

function b64ToHex(b64: string): string {
  try {
    return Array.from(atob(b64), (c) => c.charCodeAt(0).toString(16).padStart(2, '0')).join('')
  } catch {
    return ''
  }
}

function uuid(b64: string): string {
  const h = b64ToHex(b64)
  return h.length === 32 ? `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}` : b64
}

export function isoDate(ms: number): string {
  const d = new Date(ms)
  return Number.isNaN(d.getTime()) ? String(ms) : d.toISOString()
}

function displayDouble(s: string): string {
  if (/^-?\d+$/.test(s)) return s + '.0'
  return s
}

// Short human display of a value for grids.
export function display(v: BVal): string {
  switch (v.t) {
    case 'object':
      return `{ ${v.v.length} field${v.v.length === 1 ? '' : 's'} }`
    case 'array':
      return `[ ${v.v.length} element${v.v.length === 1 ? '' : 's'} ]`
    case 'string':
      return v.v
    case 'double':
      return displayDouble(v.v)
    case 'int32':
    case 'int64':
    case 'decimal':
      return v.v
    case 'bool':
      return String(v.v)
    case 'null':
      return 'null'
    case 'undefined':
      return 'undefined'
    case 'oid':
      return v.v
    case 'date':
      return isoDate(v.v)
    case 'regex':
      return `/${v.pattern}/${v.options}`
    case 'binary':
      return v.subType === '04' || v.subType === '03' ? uuid(v.base64) : v.base64
    case 'timestamp':
      return `Timestamp(${v.ts}, ${v.inc})`
    case 'minKey':
      return 'MinKey'
    case 'maxKey':
      return 'MaxKey'
    case 'code':
    case 'symbol':
    case 'other':
      return v.v
  }
}

const IDENT = /^[A-Za-z_$][A-Za-z0-9_$]*$/

// Shell-syntax rendering, parseable by the backend shell.
export function format(v: BVal, indent = '  ', cur = ''): string {
  const next = cur + indent
  const nl = indent ? '\n' : ''
  switch (v.t) {
    case 'object': {
      if (v.v.length === 0) return '{}'
      const parts = v.v.map(([k, x]) => (indent ? next : ' ') + (IDENT.test(k) ? k : JSON.stringify(k)) + ': ' + format(x, indent, next))
      return '{' + nl + parts.join(',' + nl) + nl + (indent ? cur + '}' : ' }')
    }
    case 'array': {
      if (v.v.length === 0) return '[]'
      const parts = v.v.map((x) => (indent ? next : '') + format(x, indent, next))
      return '[' + nl + parts.join(indent ? ',\n' : ', ') + nl + cur + ']'
    }
    case 'string':
      return JSON.stringify(v.v)
    case 'int32':
      return v.v
    case 'int64':
      return `NumberLong(${v.v})`
    case 'double':
      if (v.v === 'NaN' || v.v === 'Infinity' || v.v === '-Infinity') return v.v
      return /^-?\d+$/.test(v.v) ? `Double(${v.v})` : v.v
    case 'decimal':
      return `NumberDecimal("${v.v}")`
    case 'bool':
      return String(v.v)
    case 'null':
      return 'null'
    case 'undefined':
      return 'undefined'
    case 'oid':
      return `ObjectId("${v.v}")`
    case 'date':
      return `ISODate("${isoDate(v.v)}")`
    case 'regex':
      return `/${v.pattern.replace(/(^|[^\\])\//g, '$1\\/') || '(?:)'}/${v.options}`
    case 'binary':
      return v.subType === '04' ? `UUID("${uuid(v.base64)}")` : `BinData(${parseInt(v.subType, 16)}, "${v.base64}")`
    case 'timestamp':
      return `Timestamp(${v.ts}, ${v.inc})`
    case 'minKey':
      return 'MinKey()'
    case 'maxKey':
      return 'MaxKey()'
    case 'code':
      return `Code(${JSON.stringify(v.v)})`
    case 'symbol':
      return JSON.stringify(v.v)
    case 'other':
      return v.v
  }
}

// Plain JSON (relaxed) for copying to other tools.
export function toRelaxed(v: BVal): unknown {
  switch (v.t) {
    case 'object':
      return Object.fromEntries(v.v.map(([k, x]) => [k, toRelaxed(x)]))
    case 'array':
      return v.v.map(toRelaxed)
    case 'string':
    case 'bool':
      return v.v
    case 'int32':
    case 'double':
      return Number(v.v)
    case 'int64':
      return Number.isSafeInteger(Number(v.v)) ? Number(v.v) : { $numberLong: v.v }
    case 'decimal':
      return { $numberDecimal: v.v }
    case 'null':
    case 'undefined':
      return null
    case 'oid':
      return { $oid: v.v }
    case 'date':
      return { $date: isoDate(v.v) }
    default:
      return JSON.parse(toCanonical(v))
  }
}
