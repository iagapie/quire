import { useRef, useState } from 'react'
import { cn, readLocal, writeLocal } from '../../../lib/utils'

const MIN_WIDTH = 48
const MAX_SAVED = 300

export type Widths = Record<string, number>

/** Column widths saved per collection under `store`, most recently changed last. */
function loadWidths(store: string, key?: string): Widths {
  return key ? (readLocal<Record<string, Widths>>(store, {})[key] ?? {}) : {}
}

function saveWidths(store: string, key: string | undefined, widths: Widths) {
  if (!key) return
  const all = readLocal<Record<string, Widths>>(store, {})
  delete all[key]
  if (Object.keys(widths).length > 0) all[key] = widths
  const keys = Object.keys(all)
  for (const k of keys.slice(0, Math.max(0, keys.length - MAX_SAVED))) delete all[k]
  writeLocal(store, all)
}

/**
 * Widths the user dragged columns to, by column name; unset columns keep their
 * default size. Saved under `store` for `key` (a collection) when it is given.
 */
export function useColumnWidths(store: string, key?: string) {
  const [widths, setWidths] = useState<Widths>(() => loadWidths(store, key))
  const latest = useRef(widths)
  latest.current = widths

  /**
   * Starts dragging the handle of column `col`. The handle sits inside the column's
   * header cell; `dir` is -1 when it is on the cell's left edge.
   */
  const startResize = (col: string, e: React.PointerEvent<HTMLElement>, dir: 1 | -1 = 1) => {
    e.preventDefault()
    e.stopPropagation()
    const handle = e.currentTarget
    const startX = e.clientX
    const startW = handle.parentElement!.getBoundingClientRect().width
    handle.setPointerCapture(e.pointerId)
    document.body.style.cursor = 'col-resize'
    const move = (ev: PointerEvent) => {
      const w = Math.max(MIN_WIDTH, Math.round(startW + dir * (ev.clientX - startX)))
      setWidths((prev) => ({ ...prev, [col]: w }))
    }
    const up = () => {
      document.body.style.cursor = ''
      saveWidths(store, key, latest.current)
      handle.removeEventListener('pointermove', move)
      handle.removeEventListener('pointerup', up)
      handle.removeEventListener('pointercancel', up)
    }
    handle.addEventListener('pointermove', move)
    handle.addEventListener('pointerup', up)
    handle.addEventListener('pointercancel', up)
  }

  const resetWidth = (col: string) => {
    const { [col]: _, ...rest } = latest.current
    setWidths(rest)
    saveWidths(store, key, rest)
  }

  return { widths, startResize, resetWidth }
}

/** Drag handle on the edge of a header cell, which must be positioned. */
export function ResizeHandle({
  side = 'right',
  onPointerDown,
  onReset,
}: {
  side?: 'left' | 'right'
  onPointerDown: (e: React.PointerEvent<HTMLDivElement>) => void
  onReset: () => void
}) {
  return (
    <div
      onPointerDown={onPointerDown}
      onDoubleClick={onReset}
      onClick={(e) => e.stopPropagation()}
      title="Drag to resize, double-click to reset"
      className={cn('absolute inset-y-0 z-10 w-2 cursor-col-resize hover:bg-accent/40', side === 'right' ? '-right-1' : '-left-1')}
    />
  )
}
