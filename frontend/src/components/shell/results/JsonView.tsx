import { useMemo } from 'react'
import { type BVal, format } from '../../../lib/shell/ejson'
import { Editor } from '../ShellEditor'

export function JsonView({ docs, offset }: { docs: BVal[]; offset: number }) {
  const text = useMemo(() => docs.map((d, i) => `// ${offset + i + 1}\n${format(d)}`).join('\n\n'), [docs, offset])
  return <Editor value={text} readOnly className="min-h-0 flex-1 overflow-hidden" />
}

export function ValueView({ value }: { value: BVal }) {
  const text = useMemo(() => format(value), [value])
  return <Editor value={text} readOnly className="min-h-0 flex-1 overflow-hidden" />
}
