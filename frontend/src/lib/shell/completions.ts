import { snippet, type Completion, type CompletionContext, type CompletionResult } from '@codemirror/autocomplete'
import type { EditorView } from '@codemirror/view'

/** Names the shell editor can complete besides the built-in API. */
export interface ShellNames {
  collections: string[]
  databases: string[]
}

type Kind = 'db' | 'collection' | 'cursor' | 'objectId' | 'console'

interface Member {
  name: string
  /** Inserted text in CodeMirror snippet syntax; `${}` marks the cursor/tab stops. */
  tpl: string
  sig?: string
  info?: string
  /** The type a call returns, when it can be chained further. */
  returns?: Kind
  boost?: number
}

const m = (name: string, tpl: string, sig: string, info: string, returns?: Kind, boost?: number): Member => ({
  name,
  tpl,
  sig,
  info,
  returns,
  boost,
})

// Everything below mirrors what internal/shell actually implements.
const DB: Member[] = [
  m('getCollection', 'getCollection("${}")', '(name)', 'Collection by name; works for any name', 'collection', 3),
  m('getCollectionNames', 'getCollectionNames()', '()', 'Sorted names of the collections in this database', undefined, 1),
  m('getCollectionInfos', 'getCollectionInfos(${})', '(filter?)', 'listCollections output, optionally filtered'),
  m('getSiblingDB', 'getSiblingDB("${}")', '(name)', 'Another database on the same connection', 'db', 1),
  m('getMongo', 'getMongo()', '()', 'The connection', 'db'),
  m('getName', 'getName()', '()', 'Name of this database'),
  m('runCommand', 'runCommand({${}})', '(command)', 'Run a database command against this database', undefined, 1),
  m('adminCommand', 'adminCommand({${}})', '(command)', 'Run a database command against admin'),
  m('createCollection', 'createCollection("${}")', '(name, options?)', 'Create a collection'),
  m('createView', 'createView("${1}", "${2}", [${3}])', '(name, source, pipeline)', 'Create a view over a collection'),
  m('dropDatabase', 'dropDatabase()', '()', 'Drop this database'),
  m('stats', 'stats()', '()', 'dbStats'),
  m('serverStatus', 'serverStatus()', '()', 'serverStatus command'),
  m('hostInfo', 'hostInfo()', '()', 'hostInfo command'),
  m('version', 'version()', '()', 'Server version'),
  m('currentOp', 'currentOp(${})', '(filter?)', 'Operations in progress'),
  m('killOp', 'killOp(${})', '(opid)', 'Terminate an operation'),
  m('toString', 'toString()', '()', 'Name of this database'),
]

const COLLECTION: Member[] = [
  m('find', 'find({${}})', '(filter?, projection?)', 'Documents matching the filter', 'cursor', 3),
  m('findOne', 'findOne({${}})', '(filter?, projection?)', 'First document matching the filter', undefined, 2),
  m('aggregate', 'aggregate([${}])', '(pipeline)', 'Run an aggregation pipeline', 'cursor', 2),
  m('countDocuments', 'countDocuments({${}})', '(filter?, options?)', 'Exact count of matching documents', undefined, 1),
  m('estimatedDocumentCount', 'estimatedDocumentCount()', '()', 'Fast count from collection metadata'),
  m('count', 'count({${}})', '(filter?)', 'Count of matching documents (legacy)'),
  m('distinct', 'distinct("${1}", {${2}})', '(field, filter?)', 'Distinct values of a field', undefined, 1),
  m('insertOne', 'insertOne({${}})', '(doc)', 'Insert a document', undefined, 1),
  m('insertMany', 'insertMany([${}])', '(docs, options?)', 'Insert several documents'),
  m('updateOne', 'updateOne({${1}}, {$set: {${2}}})', '(filter, update, options?)', 'Update the first matching document', undefined, 1),
  m('updateMany', 'updateMany({${1}}, {$set: {${2}}})', '(filter, update, options?)', 'Update all matching documents'),
  m('replaceOne', 'replaceOne({${1}}, {${2}})', '(filter, doc, options?)', 'Replace the first matching document'),
  m('deleteOne', 'deleteOne({${}})', '(filter)', 'Delete the first matching document'),
  m('deleteMany', 'deleteMany({${}})', '(filter)', 'Delete all matching documents'),
  m('remove', 'remove({${}})', '(filter, options?)', 'Delete matching documents (legacy)'),
  m('findOneAndUpdate', 'findOneAndUpdate({${1}}, {$set: {${2}}})', '(filter, update, options?)', 'Update a document and return it'),
  m('findOneAndReplace', 'findOneAndReplace({${1}}, {${2}})', '(filter, doc, options?)', 'Replace a document and return it'),
  m('findOneAndDelete', 'findOneAndDelete({${}})', '(filter, options?)', 'Delete a document and return it'),
  m('createIndex', 'createIndex({${}})', '(keys, options?)', 'Build an index'),
  m('getIndexes', 'getIndexes()', '()', 'Indexes on this collection'),
  m('dropIndex', 'dropIndex("${}")', '(name | keys)', 'Drop an index'),
  m('dropIndexes', 'dropIndexes()', '()', 'Drop all indexes except _id'),
  m('drop', 'drop()', '()', 'Drop this collection'),
  m('stats', 'stats()', '()', 'collStats'),
  m('renameCollection', 'renameCollection("${}")', '(newName, dropTarget?)', 'Rename this collection'),
  m('getName', 'getName()', '()', 'Collection name'),
  m('getFullName', 'getFullName()', '()', 'database.collection'),
  m('getDB', 'getDB()', '()', 'The database of this collection', 'db'),
]

const CURSOR: Member[] = [
  m('sort', 'sort({${}})', '(spec)', 'Sort order, e.g. {createdAt: -1}', 'cursor', 2),
  m('limit', 'limit(${})', '(n)', 'Return at most n documents', 'cursor', 2),
  m('skip', 'skip(${})', '(n)', 'Skip the first n documents', 'cursor', 1),
  m('projection', 'projection({${}})', '(spec)', 'Fields to include or exclude', 'cursor'),
  m('project', 'project({${}})', '(spec)', 'Alias of projection', 'cursor'),
  m('filter', 'filter({${}})', '(filter)', 'Replace the query filter', 'cursor'),
  m('hint', 'hint({${}})', '(index)', 'Force an index', 'cursor'),
  m('collation', 'collation({${}})', '(spec)', 'Accepted for compatibility; ignored', 'cursor'),
  m('batchSize', 'batchSize(${})', '(n)', 'Accepted for compatibility; ignored', 'cursor'),
  m('maxTimeMS', 'maxTimeMS(${})', '(ms)', 'Accepted for compatibility; ignored', 'cursor'),
  m('comment', 'comment("${}")', '(text)', 'Accepted for compatibility; ignored', 'cursor'),
  m('allowDiskUse', 'allowDiskUse()', '()', 'Accepted for compatibility; ignored', 'cursor'),
  m('noCursorTimeout', 'noCursorTimeout()', '()', 'Accepted for compatibility; ignored', 'cursor'),
  m('readPref', 'readPref("${}")', '(mode)', 'Accepted for compatibility; ignored', 'cursor'),
  m('readConcern', 'readConcern("${}")', '(level)', 'Accepted for compatibility; ignored', 'cursor'),
  m('pretty', 'pretty()', '()', 'Accepted for compatibility; ignored', 'cursor'),
  m('toArray', 'toArray()', '()', 'All documents as an array', undefined, 1),
  m('forEach', 'forEach((doc) => {${}})', '(fn)', 'Call fn for every document', undefined, 1),
  m('map', 'map((doc) => ${})', '(fn)', 'Array of fn applied to every document'),
  m('count', 'count()', '()', 'Count of documents matching the filter', undefined, 1),
  m('size', 'size()', '()', 'Count honouring skip and limit'),
  m('itcount', 'itcount()', '()', 'Count by iterating the cursor'),
  m('hasNext', 'hasNext()', '()', 'Whether another document is available'),
  m('next', 'next()', '()', 'Next document'),
  m('tryNext', 'tryNext()', '()', 'Next document or null'),
  m('isExhausted', 'isExhausted()', '()', 'Whether the cursor is used up'),
  m('close', 'close()', '()', 'Close the cursor'),
  m('explain', 'explain(${})', '(verbosity?)', 'Query plan'),
]

const OBJECT_ID: Member[] = [
  m('toHexString', 'toHexString()', '()', 'Hex string of the id'),
  m('getTimestamp', 'getTimestamp()', '()', 'Creation time as a Date'),
  m('equals', 'equals(${})', '(other)', 'Compare with another ObjectId'),
  m('str', 'str', '', 'Hex string of the id'),
]

const CONSOLE: Member[] = ['log', 'info', 'warn', 'error', 'debug'].map((n) => m(n, `${n}(\${})`, '(...values)', 'Print to the output'))

const MEMBERS: Record<Kind, Member[]> = { db: DB, collection: COLLECTION, cursor: CURSOR, objectId: OBJECT_ID, console: CONSOLE }
const KIND_LABEL: Record<Kind, string> = { db: 'db', collection: 'collection', cursor: 'cursor', objectId: 'ObjectId', console: 'console' }

const GLOBALS: Member[] = [
  m('db', 'db', '', 'Current database', 'db', 3),
  m('console', 'console', '', 'console.log and friends', 'console'),
  m('use', 'use("${}")', '(database)', 'Switch the current database', undefined, 1),
  m('print', 'print(${})', '(...values)', 'Print to the output'),
  m('printjson', 'printjson(${})', '(value)', 'Print a value as JSON'),
  m('tojson', 'tojson(${})', '(value)', 'Format a value as JSON text'),
  m('sleep', 'sleep(${})', '(ms)', 'Pause the script'),
  m('ObjectId', 'ObjectId("${}")', '(hex?)', 'ObjectId; new one without an argument', 'objectId', 1),
  m('ISODate', 'ISODate("${}")', '(iso?)', 'Date; now without an argument', undefined, 1),
  m('NumberLong', 'NumberLong(${})', '(n)', '64-bit integer'),
  m('Long', 'Long(${})', '(n)', '64-bit integer'),
  m('NumberInt', 'NumberInt(${})', '(n)', '32-bit integer'),
  m('Int32', 'Int32(${})', '(n)', '32-bit integer'),
  m('Double', 'Double(${})', '(n)', 'Double'),
  m('NumberDouble', 'NumberDouble(${})', '(n)', 'Double'),
  m('NumberDecimal', 'NumberDecimal("${}")', '(text)', 'Decimal128'),
  m('Decimal128', 'Decimal128("${}")', '(text)', 'Decimal128'),
  m('UUID', 'UUID("${}")', '(text?)', 'UUID; random without an argument'),
  m('BinData', 'BinData(${1}, "${2}")', '(subtype, base64)', 'Binary data'),
  m('Timestamp', 'Timestamp(${})', '(t, i)', 'BSON timestamp'),
  m('MinKey', 'MinKey()', '()', 'MinKey'),
  m('MaxKey', 'MaxKey()', '()', 'MaxKey'),
]

const OPERATORS: [string, string][] = [
  ['query', '$eq $ne $gt $gte $lt $lte $in $nin $and $or $nor $not $exists $type $regex $options $expr $elemMatch $size $all $mod $text $search $where $jsonSchema $geoWithin $geoIntersects $near $nearSphere'],
  ['update', '$set $unset $inc $mul $min $max $rename $push $pull $pullAll $addToSet $pop $each $position $slice $sort $currentDate $setOnInsert'],
  ['stage', '$match $group $project $sort $limit $skip $unwind $lookup $addFields $set $unset $count $facet $bucket $bucketAuto $sortByCount $replaceRoot $replaceWith $sample $out $merge $graphLookup $unionWith $densify $fill $setWindowFields $geoNear $redact $documents $indexStats $collStats'],
  ['accumulator', '$sum $avg $first $last $min $max $push $addToSet $count $top $bottom $topN $bottomN $firstN $lastN $stdDevPop $stdDevSamp $mergeObjects'],
  ['expression', '$cond $ifNull $switch $and $or $not $eq $ne $gt $gte $lt $lte $cmp $in $add $subtract $multiply $divide $mod $abs $ceil $floor $round $trunc $pow $sqrt $concat $split $substr $substrCP $toUpper $toLower $trim $ltrim $rtrim $strLenCP $regexMatch $regexFind $replaceAll $arrayElemAt $concatArrays $filter $map $reduce $size $slice $reverseArray $isArray $indexOfArray $range $zip $objectToArray $arrayToObject $mergeObjects $getField $setField $literal $let $toString $toInt $toLong $toDouble $toDecimal $toBool $toDate $toObjectId $convert $type $dateToString $dateFromString $dateAdd $dateSubtract $dateDiff $dateTrunc $year $month $week $dayOfMonth $dayOfWeek $dayOfYear $hour $minute $second $millisecond'],
]

const OPERATOR_OPTIONS: Completion[] = (() => {
  const kinds = new Map<string, string[]>()
  for (const [kind, names] of OPERATORS) {
    for (const n of names.split(' ')) kinds.set(n, [...(kinds.get(n) ?? []), kind])
  }
  return [...kinds].map(([label, k]) => ({ label, type: 'keyword', detail: k.join(' · ') }))
})()

/** Builds a completion that inserts a member as a snippet, or just its name when a call already follows. */
function option(mem: Member, type: string, owner?: string): Completion {
  const fill = snippet(mem.tpl)
  return {
    label: mem.name,
    type,
    detail: owner ? `${mem.sig ?? ''} ${owner}`.trim() : mem.sig,
    info: mem.info,
    boost: mem.boost,
    apply: (view: EditorView, c: Completion, from: number, to: number) => {
      if (view.state.sliceDoc(to, to + 1) === '(' || !mem.tpl.startsWith(mem.name + '(')) {
        view.dispatch({ changes: { from, to, insert: mem.name }, selection: { anchor: from + mem.name.length }, userEvent: 'input.complete' })
        return
      }
      fill(view, c, from, to)
    },
  }
}

const IDENT = /[\w$]/

interface Segment {
  name: string
  call: boolean
}

/** Index of the bracket that opens the one closing at `close`, skipping strings; -1 if unbalanced. */
function matchOpen(text: string, close: number): number {
  const pairs: Record<string, string> = { ')': '(', ']': '[', '}': '{' }
  const stack: string[] = []
  let quote = ''
  for (let i = close; i >= 0; i--) {
    const ch = text[i]
    if (quote) {
      if (ch === quote && text[i - 1] !== '\\') quote = ''
      continue
    }
    if (ch === '"' || ch === "'" || ch === '`') quote = ch
    else if (pairs[ch]) stack.push(pairs[ch])
    else if (ch === '(' || ch === '[' || ch === '{') {
      if (stack.pop() !== ch) return -1
      if (stack.length === 0) return i
    }
  }
  return -1
}

/** Parses the member chain that ends at the end of `text`, e.g. `db.getCollection("x").find({})`. */
export function parseChain(text: string): Segment[] | null {
  const segs: Segment[] = []
  let i = text.length
  for (;;) {
    while (i > 0 && /\s/.test(text[i - 1])) i--
    let call = false
    if (text[i - 1] === ')') {
      const open = matchOpen(text, i - 1)
      if (open < 0) return null
      call = true
      i = open
    } else if (text[i - 1] === ']') {
      // Bracket access: db["my-coll"] behaves like a property.
      const open = matchOpen(text, i - 1)
      if (open < 0) return null
      const key = /^\s*(["'`])(.*)\1\s*$/.exec(text.slice(open + 1, i - 1))
      if (!key) return null
      segs.push({ name: key[2], call: false })
      i = open
      if (text[i - 1] === '.') return null
      continue
    }
    let start = i
    while (start > 0 && IDENT.test(text[start - 1])) start--
    if (start === i) return segs.length && !call ? segs.reverse() : null
    segs.push({ name: text.slice(start, i), call })
    i = start
    while (i > 0 && /\s/.test(text[i - 1])) i--
    if (text[i - 1] === '.' && text[i - 2] === '?') i -= 2
    else if (text[i - 1] === '.' && text[i - 2] !== '.') i--
    else break
  }
  return segs.reverse()
}

/** The expression assigned to `name` most recently before the end of `text`. */
function assignedExpr(text: string, name: string): string | null {
  const re = new RegExp(`(?:^|[^\\w$.])${name.replace(/\$/g, '\\$')}\\s*=(?![=>])\\s*`, 'g')
  let last: RegExpExecArray | null = null
  for (let r = re.exec(text); r; r = re.exec(text)) last = r
  if (!last) return null
  const start = last.index + last[0].length
  let depth = 0
  let quote = ''
  let i = start
  for (; i < text.length; i++) {
    const ch = text[i]
    if (quote) {
      if (ch === quote && text[i - 1] !== '\\') quote = ''
      continue
    }
    if (ch === '"' || ch === "'" || ch === '`') quote = ch
    else if ('([{'.includes(ch)) depth++
    else if (')]}'.includes(ch)) {
      if (--depth < 0) break
    } else if (depth === 0 && (ch === ';' || ch === '\n')) {
      // A newline followed by `.` continues the chain on the next line.
      if (ch === '\n' && /^\s*\./.test(text.slice(i + 1))) continue
      break
    }
  }
  return text.slice(start, i).replace(/^await\s+/, '').trim()
}

/** Infers what the chain evaluates to; `text` is the script before the chain, for variable lookups. */
export function inferKind(segs: Segment[], text: string, depth = 0): Kind | null {
  if (!segs.length || depth > 4) return null
  const [head, ...rest] = segs
  let kind: Kind | null = null
  if (head.call) kind = GLOBALS.find((g) => g.name === head.name)?.returns ?? null
  else if (head.name === 'db' || head.name === 'console') kind = head.name
  else {
    const expr = assignedExpr(text, head.name)
    const chain = expr ? parseChain(expr) : null
    if (chain && chain.map((s) => s.name).join('.') !== head.name) {
      kind = inferKind(chain, text.slice(0, text.lastIndexOf(expr!)), depth + 1)
    }
  }
  for (const seg of rest) {
    if (!kind) return null
    const mem = MEMBERS[kind].find((x) => x.name === seg.name)
    if (mem) kind = seg.call || !mem.tpl.includes('(') ? (mem.returns ?? null) : null
    else if (!seg.call && (kind === 'db' || kind === 'collection')) kind = 'collection' // db.orders, db.system.profile
    else return null
  }
  return kind
}

/** Variables declared in the script, offered next to the globals. */
function declared(text: string): string[] {
  const out = new Set<string>()
  for (const r of text.matchAll(/\b(?:const|let|var)\s+([A-Za-z_$][\w$]*)/g)) out.add(r[1])
  return [...out]
}

export function shellCompletions(getNames: () => ShellNames) {
  return (ctx: CompletionContext): CompletionResult | null => {
    const line = ctx.state.doc.lineAt(ctx.pos)
    const lineBefore = line.text.slice(0, ctx.pos - line.from)

    // Names inside the string argument of getCollection("…"), getSiblingDB("…") and use("…").
    const str = /\b(getCollection|getSiblingDB|use)\(\s*(["'`])([^"'`]*)$/.exec(lineBefore)
    if (str) {
      const names = getNames()
      const list = str[1] === 'getCollection' ? names.collections : names.databases
      return {
        from: ctx.pos - str[3].length,
        options: list.map((n) => ({ label: n, type: str[1] === 'getCollection' ? 'class' : 'namespace' })),
        validFor: /^[^"'`]*$/,
      }
    }
    // Inside some other string or a comment: nothing to offer.
    if (/^(?:[^"'`]|"[^"]*"|'[^']*'|`[^`]*`)*["'`][^"'`]*$/.test(lineBefore) || /\/\/.*$/.test(lineBefore.replace(/(["'`]).*?\1/g, ''))) {
      return null
    }

    const word = ctx.matchBefore(/[\w$]*$/)!
    const before = ctx.state.sliceDoc(Math.max(0, word.from - 2000), word.from)
    const dot = /(\?)?\.\s*$/.exec(before)
    if (dot && !/\.\.\s*$/.test(before)) {
      const chainText = before.slice(0, dot.index)
      const segs = parseChain(chainText)
      const kind = segs ? inferKind(segs, chainText) : null
      if (!kind) return null
      const options = MEMBERS[kind].map((x) => option(x, x.tpl.includes('(') ? 'method' : 'property', KIND_LABEL[kind]))
      if (kind === 'db') {
        for (const c of getNames().collections) {
          if (/^[A-Za-z_$][\w$]*$/.test(c)) options.push({ label: c, type: 'class', detail: 'collection', boost: 2 })
        }
      }
      return { from: word.from, options, validFor: /^[\w$]*$/ }
    }

    if (word.text.startsWith('$')) return { from: word.from, options: OPERATOR_OPTIONS, validFor: /^\$[\w$]*$/ }
    if (word.from === word.to && !ctx.explicit) return null
    if (/^\d/.test(word.text)) return null
    const vars = declared(ctx.state.sliceDoc(0, word.from)).filter((v) => !GLOBALS.some((g) => g.name === v))
    return {
      from: word.from,
      options: [
        ...GLOBALS.map((g) => option(g, g.tpl.includes('(') ? 'function' : 'variable')),
        ...vars.map((v) => ({ label: v, type: 'variable', boost: 2 })),
      ],
      validFor: /^[\w$]*$/,
    }
  }
}
