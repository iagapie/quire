// Progress math and labels for export/import jobs.
import type { TransferItem, TransferJob } from './api'

export type TransferMode = 'export' | 'import'

export const FORMATS = [
  { value: 'jsonl', label: 'JSON Lines', ext: '.jsonl', hint: 'One document per line. Streams well; works with mongoimport.' },
  { value: 'json', label: 'JSON array', ext: '.json', hint: 'A single array. Easy to open in other tools.' },
  { value: 'bson', label: 'BSON', ext: '.bson', hint: 'mongodump format. Fastest and exact.' },
] as const

export const IMPORT_MODES = [
  { value: 'insert', label: 'Insert', hint: 'Add documents; skip those whose _id already exists.' },
  { value: 'upsert', label: 'Upsert', hint: 'Replace documents with the same _id, add the rest.' },
  { value: 'replace', label: 'Drop and replace', hint: 'Drop each target collection first.' },
] as const

export const isActive = (j?: Pick<TransferJob, 'state'> | null) => j?.state === 'running'
export const isFinished = (state: string) => state === 'done' || state === 'failed' || state === 'cancelled'

/** Progress of one item in [0, 1], or null when it cannot be known yet. */
export function itemFraction(it: TransferItem): number | null {
  if (it.state === 'done') return 1
  if (it.state === 'pending') return 0
  if (it.total > 0) return Math.min(1, it.docs / it.total)
  if (it.totalBytes > 0) return Math.min(1, it.bytes / it.totalBytes)
  if (it.total === 0) return it.state === 'running' ? null : 1
  return null
}

/** Overall progress in [0, 1], weighting items by their expected size. */
export function jobFraction(j: TransferJob): number {
  const items = j.items ?? []
  if (items.length === 0) return 0
  let sum = 0
  let weights = 0
  for (const it of items) {
    const w = Math.max(1, j.kind === 'import' ? it.totalBytes : it.total)
    sum += w * (itemFraction(it) ?? 0)
    weights += w
  }
  return weights ? sum / weights : 0
}

export interface Totals {
  docs: number
  /** Expected documents, or -1 when some are unknown. */
  total: number
  bytes: number
  skipped: number
  failed: number
  done: number
  failedItems: number
  cancelledItems: number
}

export function totals(j: TransferJob): Totals {
  const t: Totals = { docs: 0, total: 0, bytes: 0, skipped: 0, failed: 0, done: 0, failedItems: 0, cancelledItems: 0 }
  for (const it of j.items ?? []) {
    t.docs += it.docs
    t.bytes += it.bytes
    t.skipped += it.skipped
    t.failed += it.failed
    if (t.total >= 0) t.total = it.total >= 0 ? t.total + it.total : -1
    if (it.state === 'done') t.done++
    if (it.state === 'failed') t.failedItems++
    if (it.state === 'cancelled') t.cancelledItems++
  }
  return t
}

/** Documents per second, or 0 when there is not enough data. */
export function rate(docs: number, elapsedMs: number): number {
  return elapsedMs >= 500 ? (docs * 1000) / elapsedMs : 0
}

/** Estimated milliseconds left, or null while the estimate would be noise. */
export function eta(fraction: number, elapsedMs: number): number | null {
  if (fraction < 0.02 || fraction >= 1 || elapsedMs < 2000) return null
  return (elapsedMs * (1 - fraction)) / fraction
}

/** A clock-like duration: 0:42, 12:05, 1:02:09. */
export function clock(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000))
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const sec = String(s % 60).padStart(2, '0')
  return h ? `${h}:${String(m).padStart(2, '0')}:${sec}` : `${m}:${sec}`
}

export function compact(n: number): string {
  if (n < 10_000) return Math.round(n).toLocaleString()
  return new Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 }).format(n)
}

/** The file name an export writes for a collection (mirrors transfer.FileName). */
export function exportFileName(coll: string, format: string, gzip: boolean): string {
  let safe = coll.replace(/[/\\:*?"<>|\0]/g, '_')
  if (safe === '' || safe === '.' || safe === '..') safe = `_${safe}`
  return `${safe}.${format}${gzip ? '.gz' : ''}`
}

export function jobTitle(j: TransferJob): string {
  const n = j.items?.length ?? 0
  const what = `${n} ${n === 1 ? 'collection' : 'collections'}`
  const verb = j.kind === 'export' ? 'Export' : 'Import'
  switch (j.state) {
    case 'running':
      return `${j.kind === 'export' ? 'Exporting' : 'Importing'} ${what}`
    case 'done':
      return `${verb} finished`
    case 'failed':
      return `${verb} finished with errors`
    default:
      return `${verb} cancelled`
  }
}

/** Shortens a path for display: the home folder becomes ~. */
export function prettyPath(p: string): string {
  return p.replace(/^\/Users\/[^/]+/, '~').replace(/^\/home\/[^/]+/, '~')
}
