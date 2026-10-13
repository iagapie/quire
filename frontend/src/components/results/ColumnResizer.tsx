import { useState } from 'react'
import { cn } from '../../lib/utils'

/** The nearest ancestor that clips its content. */
function clipParent(el: HTMLElement): HTMLElement | null {
  for (let p = el.parentElement; p; p = p.parentElement) {
    const o = getComputedStyle(p)
    if (o.overflowY !== 'visible' || o.overflowX !== 'visible') return p
  }
  return null
}

/**
 * The right edge of a column header: drag to resize, double-click for the automatic width.
 * Like the sidebar separator, it shows an accent line down the column while hovered or dragged.
 */
export function ColumnResizer({
  width,
  side = 'right',
  min = 48,
  max = 2000,
  onChange,
  onCommit,
  onReset,
}: {
  /** The current width; when left out, the width the column has on screen. */
  width?: number
  /** The edge it sits on; dragging the left edge left widens the column. */
  side?: 'left' | 'right'
  min?: number
  max?: number
  /** Called on every move while dragging. */
  onChange: (w: number) => void
  /** Called once when a drag changed the width. */
  onCommit?: (w: number) => void
  onReset?: () => void
}) {
  const [dragging, setDragging] = useState(false)
  // The guide line runs down to the bottom of what is visible, never past it, so it
  // does not make the results scroll further.
  const [line, setLine] = useState(0)
  const measure = (el: HTMLElement) => {
    const box = clipParent(el)
    if (!box) return
    const r = box.getBoundingClientRect()
    setLine(Math.max(0, r.top + box.clientTop + box.clientHeight - el.getBoundingClientRect().top))
  }
  const start = (e: React.PointerEvent<HTMLElement>) => {
    if (e.button !== 0) return
    e.preventDefault()
    e.stopPropagation()
    const x0 = e.clientX
    const w0 = width ?? Math.round(e.currentTarget.parentElement?.getBoundingClientRect().width ?? 0)
    const dir = side === 'left' ? -1 : 1
    let w = w0
    measure(e.currentTarget)
    setDragging(true)
    // Keeps the cursor while the pointer runs ahead of the edge.
    document.body.style.cursor = 'col-resize'
    const move = (ev: PointerEvent) => {
      w = Math.round(Math.min(max, Math.max(min, w0 + dir * (ev.clientX - x0))))
      onChange(w)
    }
    const up = () => {
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', up)
      document.body.style.cursor = ''
      setDragging(false)
      if (w !== w0) onCommit?.(w)
    }
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', up)
  }
  return (
    <span
      role="separator"
      aria-orientation="vertical"
      aria-valuenow={width}
      title="Drag to resize, double-click for the automatic width"
      className={cn('col-resizer', side === 'left' && 'is-left', dragging && 'is-active')}
      style={line ? ({ '--line-h': `${line}px` } as React.CSSProperties) : undefined}
      onPointerEnter={(e) => measure(e.currentTarget)}
      onPointerDown={start}
      onClick={(e) => e.stopPropagation()}
      onDoubleClick={(e) => {
        e.stopPropagation()
        onReset?.()
      }}
    />
  )
}
