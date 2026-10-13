import { clsx, type ClassValue } from 'clsx'

export const cn = (...v: ClassValue[]) => clsx(v)

let counter = 0
export const uid = (prefix = 'id') => `${prefix}-${Date.now().toString(36)}-${(counter++).toString(36)}`

export const isMac = typeof navigator !== 'undefined' && /Mac/.test(navigator.platform)
export const mod = isMac ? '⌘' : 'Ctrl'
export const shift = isMac ? '⇧' : 'Shift'

export function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms} ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(ms < 10_000 ? 2 : 1)} s`
  const m = Math.floor(ms / 60_000)
  return `${m}m ${Math.round((ms % 60_000) / 1000)}s`
}

export function formatNumber(n: number): string {
  return n.toLocaleString()
}

export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  const units = ['KiB', 'MiB', 'GiB', 'TiB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(v < 10 ? 1 : 0)} ${units[i]}`
}

export function readLocal<T>(key: string, fallback: T): T {
  try {
    const raw = localStorage.getItem(key)
    return raw ? (JSON.parse(raw) as T) : fallback
  } catch {
    return fallback
  }
}

export function writeLocal(key: string, value: unknown) {
  try {
    localStorage.setItem(key, JSON.stringify(value))
  } catch {
    // storage full or unavailable: persistence is best-effort
  }
}

export async function copyText(text: string) {
  await navigator.clipboard.writeText(text)
}

/** Persisted panel sizes. */
export const layoutKeys = {
  sidebarPx: 'quire.layout.sidebar',
  editorPercent: 'quire.layout.editor',
}

export const PROFILE_COLORS = ['', '#10b981', '#3b82f6', '#8b5cf6', '#f59e0b', '#ef4444', '#ec4899', '#14b8a6', '#64748b']

/** Platform name of the file manager. */
export const fileManager = isMac ? 'Finder' : 'the file manager'
