// JSON documents as typed, order-preserving values, for the document views (Cosmos DB).
// Numbers keep their source text, so large integers are shown exactly.

export type JVal =
  | { t: 'object'; v: [string, JVal][] }
  | { t: 'array'; v: JVal[] }
  | { t: 'string'; v: string }
  | { t: 'number'; v: string }
  | { t: 'bool'; v: boolean }
  | { t: 'null' }

/** Parses JSON text, keeping the order of object keys (JSON.parse moves integer-like keys first). */
export function parseJSON(src: string): JVal {
  let i = 0
  const ws = () => {
    while (i < src.length && (src[i] === ' ' || src[i] === '\n' || src[i] === '\r' || src[i] === '\t')) i++
  }
  const fail = (): never => {
    throw new SyntaxError(`invalid JSON at ${i}`)
  }
  const str = (): string => {
    const start = i
    i++
    let simple = true
    while (i < src.length && src[i] !== '"') {
      if (src[i] === '\\') {
        simple = false
        i++
      }
      i++
    }
    if (i >= src.length) fail()
    i++
    const s = src.slice(start, i)
    return simple ? s.slice(1, -1) : (JSON.parse(s) as string)
  }
  const value = (): JVal => {
    ws()
    const c = src[i]
    if (c === '{') {
      i++
      const entries: [string, JVal][] = []
      ws()
      if (src[i] === '}') {
        i++
        return { t: 'object', v: entries }
      }
      for (;;) {
        ws()
        if (src[i] !== '"') fail()
        const k = str()
        ws()
        if (src[i++] !== ':') fail()
        entries.push([k, value()])
        ws()
        const sep = src[i++]
        if (sep === '}') return { t: 'object', v: entries }
        if (sep !== ',') fail()
      }
    }
    if (c === '[') {
      i++
      const arr: JVal[] = []
      ws()
      if (src[i] === ']') {
        i++
        return { t: 'array', v: arr }
      }
      for (;;) {
        arr.push(value())
        ws()
        const sep = src[i++]
        if (sep === ']') return { t: 'array', v: arr }
        if (sep !== ',') fail()
      }
    }
    if (c === '"') return { t: 'string', v: str() }
    if (src.startsWith('true', i)) {
      i += 4
      return { t: 'bool', v: true }
    }
    if (src.startsWith('false', i)) {
      i += 5
      return { t: 'bool', v: false }
    }
    if (src.startsWith('null', i)) {
      i += 4
      return { t: 'null' }
    }
    const start = i
    while (i < src.length && /[-+0-9.eE]/.test(src[i])) i++
    if (i === start) fail()
    return { t: 'number', v: src.slice(start, i) }
  }
  const out = value()
  ws()
  if (i < src.length) fail()
  return out
}

/** Parses JSON, or keeps the text as a string when it is not JSON. */
export function toJVal(text: string | null): JVal {
  if (text === null) return { t: 'null' }
  try {
    return parseJSON(text)
  } catch {
    return { t: 'string', v: text }
  }
}

export function field(doc: JVal, key: string): JVal | undefined {
  return doc.t === 'object' ? doc.v.find((e) => e[0] === key)?.[1] : undefined
}

const TYPE_NAMES: Record<JVal['t'], string> = { object: 'Object', array: 'Array', string: 'String', number: 'Number', bool: 'Boolean', null: 'Null' }

export function typeName(v: JVal): string {
  return TYPE_NAMES[v.t]
}

export function isContainer(v: JVal): v is Extract<JVal, { t: 'object' | 'array' }> {
  return v.t === 'object' || v.t === 'array'
}

/** Short display of a value for grids. */
export function display(v: JVal): string {
  switch (v.t) {
    case 'object':
      return `{ ${v.v.length} field${v.v.length === 1 ? '' : 's'} }`
    case 'array':
      return `[ ${v.v.length} element${v.v.length === 1 ? '' : 's'} ]`
    case 'string':
    case 'number':
      return v.v
    case 'bool':
      return String(v.v)
    case 'null':
      return 'null'
  }
}

/** JSON text of a value; indent '' writes it on one line. */
export function format(v: JVal, indent = '  ', cur = ''): string {
  const next = cur + indent
  const nl = indent ? '\n' : ''
  switch (v.t) {
    case 'object': {
      if (v.v.length === 0) return '{}'
      const parts = v.v.map(([k, x]) => (indent ? next : '') + JSON.stringify(k) + ': ' + format(x, indent, next))
      return '{' + nl + parts.join(indent ? ',\n' : ', ') + nl + cur + '}'
    }
    case 'array': {
      if (v.v.length === 0) return '[]'
      const parts = v.v.map((x) => (indent ? next : '') + format(x, indent, next))
      return '[' + nl + parts.join(indent ? ',\n' : ', ') + nl + cur + ']'
    }
    case 'string':
      return JSON.stringify(v.v)
    case 'number':
      return v.v
    case 'bool':
      return String(v.v)
    case 'null':
      return 'null'
  }
}
