import { useEffect, useImperativeHandle, useRef, forwardRef } from 'react'
import { Compartment, EditorState, Prec, RangeSetBuilder, StateField, type Extension } from '@codemirror/state'
import {
  Decoration,
  type DecorationSet,
  EditorView,
  keymap,
  lineNumbers,
  highlightActiveLine,
  highlightActiveLineGutter,
  drawSelection,
  placeholder as cmPlaceholder,
} from '@codemirror/view'
import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands'
import { json } from '@codemirror/lang-json'
import {
  sql,
  keywordCompletionSource,
  schemaCompletionSource,
  type SQLDialect,
  type SQLNamespace,
  StandardSQL,
  PostgreSQL,
  MySQL,
  MariaSQL,
  MSSQL,
  SQLite,
  PLSQL,
  SQLDialect as Dialect,
} from '@codemirror/lang-sql'
import { bracketMatching, foldGutter, foldKeymap, indentOnInput, syntaxHighlighting, HighlightStyle } from '@codemirror/language'
import {
  autocompletion,
  closeBrackets,
  closeBracketsKeymap,
  completionKeymap,
  snippetCompletion,
  type Completion,
  type CompletionContext,
  type CompletionSource,
} from '@codemirror/autocomplete'
import { highlightSelectionMatches, searchKeymap } from '@codemirror/search'
import { tags as t } from '@lezer/highlight'
import type { CompletionInfo, Snippet } from '../lib/api'
import { joinSuggestions } from '../lib/joins'
import { split, statementAt, type SplitOptions } from '../lib/split'
import { quoteIdent } from '../lib/sql'

const highlight = HighlightStyle.define([
  { tag: [t.keyword, t.controlKeyword, t.operatorKeyword, t.definitionKeyword, t.moduleKeyword], color: 'var(--syn-keyword)' },
  { tag: [t.string, t.special(t.string), t.regexp], color: 'var(--syn-string)' },
  { tag: [t.number, t.bool, t.null, t.atom], color: 'var(--syn-number)' },
  { tag: [t.propertyName, t.definition(t.propertyName), t.special(t.name)], color: 'var(--syn-key)' },
  { tag: [t.typeName, t.standard(t.name), t.function(t.variableName), t.function(t.propertyName)], color: 'var(--syn-type)' },
  { tag: [t.lineComment, t.blockComment], color: 'var(--syn-comment)', fontStyle: 'italic' },
  { tag: [t.punctuation, t.bracket, t.separator], color: 'var(--muted)' },
])

export const editorTheme = EditorView.theme({
  '&': { backgroundColor: 'transparent', color: 'var(--fg)' },
  '.cm-content': { caretColor: 'var(--accent)', padding: '8px 0' },
  '.cm-cursor, .cm-dropCursor': { borderLeftColor: 'var(--accent)', borderLeftWidth: '2px' },
  '&.cm-focused .cm-selectionBackground, .cm-selectionBackground, ::selection': {
    backgroundColor: 'color-mix(in srgb, var(--accent) 24%, transparent) !important',
  },
  '.cm-gutters': { backgroundColor: 'transparent', color: 'var(--faint)', border: 'none' },
  '.cm-lineNumbers .cm-gutterElement': { padding: '0 10px 0 14px', minWidth: '38px' },
  '.cm-activeLine': { backgroundColor: 'var(--hover)' },
  '.cm-activeLineGutter': { backgroundColor: 'transparent', color: 'var(--fg)' },
  '.cm-matchingBracket': { backgroundColor: 'var(--accent-soft)', outline: '1px solid color-mix(in srgb, var(--accent) 50%, transparent)' },
  '.cm-selectionMatch': { backgroundColor: 'var(--active)' },
  '.cm-tooltip': { backgroundColor: 'var(--raised)', border: '1px solid var(--line)', borderRadius: '8px', overflow: 'hidden' },
  '.cm-tooltip-autocomplete > ul > li': { padding: '3px 8px !important', fontFamily: 'var(--font-mono)' },
  '.cm-tooltip-autocomplete > ul > li[aria-selected]': { backgroundColor: 'var(--accent)', color: 'var(--accent-fg)' },
  '.cm-completionDetail': { opacity: 0.6, fontStyle: 'normal', marginLeft: '8px' },
  '.cm-panels': { backgroundColor: 'var(--panel)', color: 'var(--fg)', borderColor: 'var(--line)' },
  '.cm-searchMatch': { backgroundColor: 'color-mix(in srgb, var(--warn) 30%, transparent)' },
  '.cm-foldGutter .cm-gutterElement': { color: 'var(--faint)', padding: '0 4px' },
  '.cm-placeholder': { color: 'var(--faint)' },
  // The statement that runs with ⌘↵.
  '.cm-current-statement': { backgroundColor: 'color-mix(in srgb, var(--accent) 6%, transparent)' },
  '.cm-current-statement-gutter': { boxShadow: 'inset 2px 0 0 var(--accent)' },
})

export const baseHighlight = syntaxHighlighting(highlight)

/** Cosmos DB's SQL: a SELECT-only dialect over JSON documents. */
export const CosmosSQL = Dialect.define({
  keywords:
    'select value top distinct from in join where and or not between is null true false undefined order by asc desc group offset limit as exists like escape array object',
  builtin:
    'abs acos array_concat array_contains array_length array_slice asin atan avg ceiling concat contains cos count endswith floor getcurrentdatetime index_of is_array is_bool is_defined is_null is_number is_object is_primitive is_string left length lower ltrim max min replace replicate reverse right round rtrim startswith st_distance st_within substring sum tostring trim upper',
  types: '',
  identifierQuotes: '"',
})

/** ClickHouse: backtick or double-quoted identifiers, backslash escapes in strings. */
export const ClickHouseSQL = Dialect.define({
  keywords:
    'select from where prewhere group by having order limit offset with totals rollup cube settings format union all distinct as on using join inner left right full outer cross array any asof semi anti global insert into values create table view materialized dictionary database if not exists engine partition primary key sample ttl alter add drop modify column index type granularity update delete rename truncate optimize final system show describe explain use set and or in between like ilike is null case when then else end interval',
  builtin:
    'any anyLast argMax argMin arrayJoin arrayMap arrayFilter avg count countIf dateDiff formatDateTime groupArray if ifNull multiIf now quantile sum sumIf toDate toDateTime toStartOfDay toString today uniq uniqExact',
  types:
    'UInt8 UInt16 UInt32 UInt64 UInt128 UInt256 Int8 Int16 Int32 Int64 Int128 Int256 Float32 Float64 Decimal String FixedString UUID Date Date32 DateTime DateTime64 Bool Enum8 Enum16 Array Tuple Map Nullable LowCardinality JSON IPv4 IPv6',
  identifierQuotes: '`"',
  backslashEscapes: true,
  hashComments: true,
  doubleDollarQuotedStrings: true,
})

/** DuckDB: PostgreSQL-like, with its own types and file readers. */
export const DuckDBSQL = Dialect.define({
  ...PostgreSQL.spec,
  builtin:
    'read_csv read_csv_auto read_parquet read_json read_json_auto glob list_transform list_filter struct_pack unnest generate_series range strftime strptime epoch_ms arg_max arg_min max_by min_by',
  types: (PostgreSQL.spec.types ?? '') + ' hugeint uhugeint utinyint usmallint uinteger ubigint list struct map union blob enum',
})

export const DIALECTS: Record<string, SQLDialect> = {
  PostgreSQL,
  MySQL,
  MariaSQL,
  MSSQL,
  SQLite,
  PLSQL,
  StandardSQL,
  Cosmos: CosmosSQL,
  ClickHouse: ClickHouseSQL,
  DuckDB: DuckDBSQL,
}

/** Highlights the statement under the cursor. */
function currentStatement(getSplit: () => SplitOptions): Extension {
  const mark = Decoration.line({ class: 'cm-current-statement' })
  const compute = (state: EditorState): DecorationSet => {
    const doc = state.doc.toString()
    if (doc.length > 500_000) return Decoration.none
    const sel = state.selection.main
    if (!sel.empty) return Decoration.none
    const st = statementAt(doc, split(doc, getSplit()), sel.head)
    if (!st) return Decoration.none
    const b = new RangeSetBuilder<Decoration>()
    const first = state.doc.lineAt(st.from).number
    const last = state.doc.lineAt(st.to).number
    // A single-line statement is clear enough without a highlight.
    if (first === last) return Decoration.none
    for (let n = first; n <= last; n++) {
      const line = state.doc.line(n)
      b.add(line.from, line.from, mark)
    }
    return b.finish()
  }
  return StateField.define<DecorationSet>({
    create: compute,
    update: (deco, tr) => (tr.docChanged || tr.selection ? compute(tr.state) : deco),
    provide: (f) => EditorView.decorations.from(f),
  })
}

export interface EditorHandle {
  view: EditorView | null
  focus(): void
  selection(): { cursor: number; from: number; to: number }
  /** Replaces the whole text (undoable). */
  replace(text: string): void
}

export interface SQLSupport {
  /** CodeMirror dialect name (Info.editor). */
  dialect: string
  split?: SplitOptions
  functions?: string[]
  snippets?: Snippet[]
  /** Schema metadata for completion. */
  completion?: () => CompletionInfo | undefined
  /** The current schema, whose tables complete without a prefix. */
  schema?: () => string
}

export interface EditorProps {
  value: string
  onChange?: (v: string) => void
  readOnly?: boolean
  lineNumbers?: boolean
  placeholder?: string
  /** Extra key bindings; take precedence over defaults. */
  keys?: { key: string; run: () => boolean | void; preventDefault?: boolean }[]
  /** SQL language support; plain text when absent. */
  sql?: SQLSupport
  /** JSON highlighting (read-only documents). */
  json?: boolean
  extensions?: Extension[]
  className?: string
  autoFocus?: boolean
}

function namespace(info: CompletionInfo | undefined, schema: string): SQLNamespace {
  const ns: Record<string, SQLNamespace> = {}
  if (!info) return ns
  for (const [table, cols] of Object.entries(info.tables ?? {})) {
    ns[table] = (cols ?? []).map((c) => ({ label: c, type: 'property' }))
  }
  for (const s of info.schemas ?? []) {
    if (s === schema || s in ns) continue
    // Tables of other schemas complete after "schema." (without their columns).
    const children: Record<string, SQLNamespace> = {}
    for (const table of info.other?.[s] ?? []) children[table] = []
    ns[s] = { self: { label: s, type: 'namespace' }, children }
  }
  return ns
}

function sqlCompletions(support: SQLSupport, dialect: SQLDialect): CompletionSource[] {
  let cached: { info?: CompletionInfo; schema: string; source: CompletionSource } | null = null
  const schemaSource: CompletionSource = (ctx: CompletionContext) => {
    const info = support.completion?.()
    const schema = support.schema?.() ?? ''
    if (!cached || cached.info !== info || cached.schema !== schema) {
      cached = { info, schema, source: schemaCompletionSource({ dialect, schema: namespace(info, schema) }) }
    }
    return cached.source(ctx)
  }
  const fn: Completion[] = (support.functions ?? []).map((f) => ({ label: f, type: 'function', apply: `${f}()`, boost: -1 }))
  const snips = (support.snippets ?? []).map((s) => snippetCompletion(s.body, { label: s.label, detail: s.detail, type: 'text', boost: -2 }))
  const extra: CompletionSource = (ctx) => {
    const word = ctx.matchBefore(/\w+$/)
    if (!word || (word.from === word.to && !ctx.explicit)) return null
    // Not after a dot: that completes columns of a table or alias.
    if (ctx.state.sliceDoc(word.from - 1, word.from) === '.') return null
    return { from: word.from, options: [...fn, ...snips], validFor: /^\w*$/ }
  }
  // JOIN: tables related by a foreign key, with an alias and the ON condition; after
  // ON, the join conditions.
  const joinSource: CompletionSource = (ctx) => {
    const joins = support.completion?.()?.joins ?? []
    if (joins.length === 0) return null
    const doc = ctx.state.doc.toString()
    if (doc.length > 500_000) return null
    const st = statementAt(doc, split(doc, support.split ?? {}), ctx.pos)
    const before = doc.slice(st && st.from <= ctx.pos ? st.from : 0, ctx.pos)
    const found = joinSuggestions(before, joins, (s) => quoteIdent(support.dialect, s))
    if (found.length === 0) return null
    const word = ctx.matchBefore(/[\w$]*$/)
    return {
      from: word?.from ?? ctx.pos,
      options: found.map((j) => ({ label: j.label, detail: j.detail, apply: j.apply, type: 'keyword', boost: 10 })),
      filter: false,
    }
  }
  return [joinSource, schemaSource, keywordCompletionSource(dialect, true), extra]
}

export const Editor = forwardRef<EditorHandle, EditorProps>(function Editor(
  { value, onChange, readOnly, lineNumbers: showNumbers = true, placeholder, keys, sql: support, json: isJSON, extensions, className, autoFocus },
  ref,
) {
  const host = useRef<HTMLDivElement>(null)
  const view = useRef<EditorView | null>(null)
  const cb = useRef({ onChange, keys, support })
  cb.current = { onChange, keys, support }
  const language = useRef(new Compartment())

  useImperativeHandle(ref, () => ({
    get view() {
      return view.current
    },
    focus: () => view.current?.focus(),
    selection: () => {
      const sel = view.current?.state.selection.main
      return { cursor: sel?.head ?? 0, from: sel?.from ?? 0, to: sel?.to ?? 0 }
    },
    replace: (text) => {
      const v = view.current
      if (v) v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: text } })
    },
  }))

  const languageExtension = (): Extension => {
    const s = cb.current.support
    if (isJSON) return json()
    if (!s) return []
    const dialect = DIALECTS[s.dialect] ?? StandardSQL
    return [
      sql({ dialect, upperCaseKeywords: true }).language,
      autocompletion({ override: sqlCompletions(s, dialect), activateOnTyping: true }),
      readOnly ? [] : currentStatement(() => cb.current.support?.split ?? {}),
    ]
  }

  useEffect(() => {
    if (!host.current) return
    const custom = keymap.of(
      (cb.current.keys ?? []).map((k) => ({
        key: k.key,
        preventDefault: true,
        run: () => {
          const b = cb.current.keys?.find((x) => x.key === k.key)
          const r = b?.run()
          return r !== false
        },
      })),
    )
    const exts: Extension[] = [
      Prec.highest(custom),
      showNumbers ? [lineNumbers(), foldGutter({ openText: '▾', closedText: '▸' })] : [],
      highlightActiveLineGutter(),
      history(),
      drawSelection(),
      indentOnInput(),
      bracketMatching(),
      closeBrackets(),
      highlightActiveLine(),
      highlightSelectionMatches(),
      language.current.of(languageExtension()),
      baseHighlight,
      editorTheme,
      EditorState.tabSize.of(4),
      keymap.of([...closeBracketsKeymap, ...defaultKeymap, ...searchKeymap, ...historyKeymap, ...foldKeymap, ...completionKeymap, indentWithTab]),
      EditorView.updateListener.of((u) => {
        if (u.docChanged) cb.current.onChange?.(u.state.doc.toString())
      }),
      readOnly ? [EditorState.readOnly.of(true), EditorView.editable.of(false)] : [],
      placeholder ? cmPlaceholder(placeholder) : [],
      ...(extensions ?? []),
    ]
    const v = new EditorView({ state: EditorState.create({ doc: value, extensions: exts }), parent: host.current })
    view.current = v
    if (autoFocus) v.focus()
    return () => {
      v.destroy()
      view.current = null
    }
    // The editor is created once; value and dialect changes are synced below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [readOnly])

  // A different database type (the tab moved to another connection) swaps the language.
  const dialectName = support?.dialect
  useEffect(() => {
    view.current?.dispatch({ effects: language.current.reconfigure(languageExtension()) })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [dialectName, isJSON])

  useEffect(() => {
    const v = view.current
    if (v && v.state.doc.toString() !== value) {
      v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: value } })
    }
  }, [value])

  return <div ref={host} className={className} />
})
