import { toast } from 'sonner'
import { type BVal, display, format, toRelaxed } from '../../../lib/shell/ejson'
import { cn, copyText } from '../../../lib/utils'

export interface DocActions {
  editable: boolean
  onEdit?: (index: number) => void
  onSetField?: (index: number, path: string, text: string) => Promise<boolean>
  onUnsetField?: (index: number, path: string) => void
  onDelete?: (indices: number[]) => void
  onDuplicate?: (index: number) => void
}

export const valueClass = (v: BVal) =>
  cn(
    v.t === 'string' && 'text-syn-string',
    (v.t === 'int32' || v.t === 'int64' || v.t === 'double' || v.t === 'decimal') && 'text-syn-number tabular-nums',
    (v.t === 'bool' || v.t === 'null' || v.t === 'undefined') && 'text-syn-keyword',
    (v.t === 'oid' || v.t === 'date' || v.t === 'binary' || v.t === 'timestamp') && 'text-syn-type',
    v.t === 'regex' && 'text-syn-string',
    (v.t === 'object' || v.t === 'array') && 'text-faint',
  )

/** Outline marking the cell or row a click or an open context menu acts on. */
export const targetClass =
  'data-[active=true]:shadow-[inset_0_0_0_1px_var(--color-accent)] data-[state=open]:bg-accent-soft data-[state=open]:shadow-[inset_0_0_0_1.5px_var(--color-accent)]'

/** Short field name for menu labels, e.g. Copy value of "name". */
export function fieldLabel(name: string) {
  return name.length > 24 ? `"${name.slice(0, 23)}…"` : `"${name}"`
}

export function Value({ v, className }: { v: BVal; className?: string }) {
  const text = display(v)
  return (
    <span className={cn('truncate', valueClass(v), className)} title={text.length > 60 ? text.slice(0, 2000) : undefined}>
      {v.t === 'string' && text === '' ? <span className="text-faint italic">empty</span> : text}
    </span>
  )
}

export function joinPath(base: string, key: string) {
  return base ? `${base}.${key}` : key
}

/**
 * Text for copying documents: one document as is, several as an array (ready for
 * insertMany). Shell syntax keeps BSON types; JSON is relaxed Extended JSON.
 */
export function docsText(docs: BVal[], json = false): string {
  if (json) return JSON.stringify(docs.length === 1 ? toRelaxed(docs[0]) : docs.map(toRelaxed), null, 2)
  if (docs.length === 1) return format(docs[0])
  return '[\n' + docs.map((d) => '  ' + format(d, '  ', '  ')).join(',\n') + '\n]'
}

/** Copies the documents at `indices` (in page order) to the clipboard. */
export async function copyDocs(docs: BVal[], indices: Iterable<number>, json = false) {
  const picked = [...indices].sort((a, b) => a - b).map((i) => docs[i]).filter(Boolean)
  if (picked.length === 0) return
  try {
    await copyText(docsText(picked, json))
    toast.success(picked.length === 1 ? 'Document copied' : `${picked.length} documents copied`)
  } catch (err) {
    toast.error('Could not copy', { description: err instanceof Error ? err.message : String(err) })
  }
}

/** The documents a context menu acts on: the whole selection when the row is part of it. */
export function menuTargets(selected: Set<number>, row: number): number[] {
  return selected.has(row) && selected.size > 1 ? [...selected].sort((a, b) => a - b) : [row]
}
