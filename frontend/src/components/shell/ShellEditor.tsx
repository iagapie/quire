import { useEffect, useImperativeHandle, useRef, forwardRef } from 'react'
import { EditorState, Prec, type Extension } from '@codemirror/state'
import {
  EditorView,
  keymap,
  lineNumbers,
  highlightActiveLine,
  highlightActiveLineGutter,
  drawSelection,
  placeholder as cmPlaceholder,
} from '@codemirror/view'
import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands'
import { javascript } from '@codemirror/lang-javascript'
import { bracketMatching, foldGutter, foldKeymap, indentOnInput, syntaxHighlighting, HighlightStyle } from '@codemirror/language'
import { autocompletion, closeBrackets, closeBracketsKeymap, completionKeymap } from '@codemirror/autocomplete'
import { highlightSelectionMatches, searchKeymap } from '@codemirror/search'
import { tags as t } from '@lezer/highlight'
import { shellCompletions, type ShellNames } from '../../lib/shell/completions'

const highlight = HighlightStyle.define([
  { tag: [t.keyword, t.controlKeyword, t.operatorKeyword, t.definitionKeyword, t.moduleKeyword], color: 'var(--syn-keyword)' },
  { tag: [t.string, t.special(t.string), t.regexp], color: 'var(--syn-string)' },
  { tag: [t.number, t.bool, t.null, t.atom], color: 'var(--syn-number)' },
  { tag: [t.propertyName, t.definition(t.propertyName)], color: 'var(--syn-key)' },
  { tag: [t.function(t.variableName), t.function(t.propertyName)], color: 'var(--syn-type)' },
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
})

export const baseHighlight = syntaxHighlighting(highlight)

export interface EditorHandle {
  view: EditorView | null
  focus(): void
  selection(): { cursor: number; from: number; to: number }
}

export interface EditorProps {
  value: string
  onChange?: (v: string) => void
  readOnly?: boolean
  lineNumbers?: boolean
  placeholder?: string
  /** Extra key bindings; take precedence over defaults. */
  keys?: { key: string; run: () => boolean | void; preventDefault?: boolean }[]
  /** Enables shell autocompletion; returns the collection and database names to offer. */
  completions?: () => ShellNames
  extensions?: Extension[]
  className?: string
  autoFocus?: boolean
}

export const Editor = forwardRef<EditorHandle, EditorProps>(function Editor(
  { value, onChange, readOnly, lineNumbers: showNumbers = true, placeholder, keys, completions, extensions, className, autoFocus },
  ref,
) {
  const host = useRef<HTMLDivElement>(null)
  const view = useRef<EditorView | null>(null)
  const cb = useRef({ onChange, keys, completions })
  cb.current = { onChange, keys, completions }

  useImperativeHandle(ref, () => ({
    get view() {
      return view.current
    },
    focus: () => view.current?.focus(),
    selection: () => {
      const sel = view.current?.state.selection.main
      return { cursor: sel?.head ?? 0, from: sel?.from ?? 0, to: sel?.to ?? 0 }
    },
  }))

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
      javascript(),
      baseHighlight,
      editorTheme,
      EditorState.tabSize.of(2),
      keymap.of([...closeBracketsKeymap, ...defaultKeymap, ...searchKeymap, ...historyKeymap, ...foldKeymap, ...completionKeymap, indentWithTab]),
      EditorView.updateListener.of((u) => {
        if (u.docChanged) cb.current.onChange?.(u.state.doc.toString())
      }),
      readOnly ? [EditorState.readOnly.of(true), EditorView.editable.of(false)] : [],
      placeholder ? cmPlaceholder(placeholder) : [],
      completions ? autocompletion({ override: [shellCompletions(() => cb.current.completions?.() ?? { collections: [], databases: [] })], activateOnTyping: true }) : [],
      ...(extensions ?? []),
    ]
    const v = new EditorView({ state: EditorState.create({ doc: value, extensions: exts }), parent: host.current })
    view.current = v
    if (autoFocus) v.focus()
    return () => {
      v.destroy()
      view.current = null
    }
    // The editor is created once; value changes are synced below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [readOnly])

  useEffect(() => {
    const v = view.current
    if (v && v.state.doc.toString() !== value) {
      v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: value } })
    }
  }, [value])

  return <div ref={host} className={className} />
})
