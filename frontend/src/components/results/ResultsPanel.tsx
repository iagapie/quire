import { AlertCircle, CheckCircle2, Sheet, TerminalSquare, X } from 'lucide-react'
import { useScrollX } from '../../lib/useScrollX'
import { OUTPUT_RESULT, useStore, type ResultState, type Tab } from '../../lib/store'
import { cn, formatDuration } from '../../lib/utils'
import { ResultView } from './ResultView'

function icon(r: ResultState) {
  switch (r.kind) {
    case 'rows':
      return <Sheet size={12} />
    case 'error':
      return <AlertCircle size={12} className="text-danger" />
    default:
      return <CheckCircle2 size={12} className="text-ok" />
  }
}

export function ResultsPanel({ tab }: { tab: Tab }) {
  const updateTab = useStore((s) => s.updateTab)
  const active = tab.results.find((r) => r.id === tab.activeResult)
  const strip = useScrollX<HTMLDivElement>(tab.activeResult)
  const closeResult = (id: string) => {
    const results = tab.results.filter((r) => r.id !== id)
    updateTab(tab.id, { results, activeResult: tab.activeResult === id ? (results[0]?.id ?? OUTPUT_RESULT) : tab.activeResult })
  }

  return (
    <div className="flex h-full min-h-0 flex-col bg-bg">
      <div ref={strip} className="scroll-x flex h-9 shrink-0 items-end gap-0.5 border-b border-line bg-panel px-2" role="tablist">
        <TabButton active={tab.activeResult === OUTPUT_RESULT} onClick={() => updateTab(tab.id, { activeResult: OUTPUT_RESULT })} icon={<TerminalSquare size={12} />}>
          Output
          {tab.output.length > 0 && <span className="rounded bg-sunken px-1 text-[10px] text-muted tabular-nums">{tab.output.length}</span>}
        </TabButton>
        {tab.results.map((r) => (
          <TabButton key={r.id} active={tab.activeResult === r.id} onClick={() => updateTab(tab.id, { activeResult: r.id })} icon={icon(r)} onClose={() => closeResult(r.id)}>
            <span className="max-w-56 truncate" title={r.statement}>
              {r.title}
            </span>
          </TabButton>
        ))}
        <div className="flex-1" />
        {tab.lastDurationMs != null && !tab.running && (
          <span className="mb-2 shrink-0 pr-1 text-[11px] text-faint tabular-nums">Ran in {formatDuration(tab.lastDurationMs)}</span>
        )}
      </div>
      {tab.activeResult === OUTPUT_RESULT || !active ? (
        <div className="selectable min-h-0 flex-1 overflow-auto p-3 font-mono text-[12px]">
          {tab.output.length === 0 ? (
            <div className="flex h-full flex-col items-center justify-center gap-1 text-center font-sans text-muted">
              <span className="text-[13px]">{tab.results.length ? 'No messages' : 'Run a script to see results here'}</span>
              <span className="text-xs text-faint">Rows affected, durations and notices appear in this tab.</span>
            </div>
          ) : (
            tab.output.map((line, i) => (
              <pre key={i} className="border-b border-line/50 py-1 whitespace-pre-wrap">
                {line}
              </pre>
            ))
          )}
        </div>
      ) : (
        <ResultView key={active.id} tab={tab} result={active} />
      )}
    </div>
  )
}

function TabButton({
  active,
  onClick,
  icon,
  children,
  onClose,
}: {
  active: boolean
  onClick: () => void
  icon: React.ReactNode
  children: React.ReactNode
  onClose?: () => void
}) {
  return (
    <div
      role="tab"
      aria-selected={active}
      tabIndex={0}
      onClick={onClick}
      onKeyDown={(e) => e.key === 'Enter' && onClick()}
      onAuxClick={(e) => e.button === 1 && onClose?.()}
      className={cn(
        'group relative flex h-8 shrink-0 items-center gap-1.5 rounded-t-md px-2.5 text-xs',
        active ? 'bg-bg text-fg after:absolute after:inset-x-0 after:-bottom-px after:h-px after:bg-bg' : 'text-muted hover:text-fg',
        active && 'border border-b-0 border-line',
      )}
    >
      <span className="text-muted">{icon}</span>
      {children}
      {onClose && (
        <button
          aria-label="Close result"
          onClick={(e) => {
            e.stopPropagation()
            onClose()
          }}
          className={cn('-mr-1 rounded p-0.5 text-faint hover:bg-hover hover:text-fg', !active && 'opacity-0 group-hover:opacity-100')}
        >
          <X size={11} />
        </button>
      )}
    </div>
  )
}
