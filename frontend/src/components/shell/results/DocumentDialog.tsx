import { useEffect, useRef, useState } from 'react'
import { AlertCircle } from 'lucide-react'
import { Shell, errorMessage } from '../../../lib/shell/api'
import { mod } from '../../../lib/utils'
import { Editor } from '../ShellEditor'
import { Button, Kbd, Modal, Spinner } from '../../ui'

export interface DocDialogState {
  mode: 'edit' | 'insert'
  sessionId: string
  db: string
  coll: string
  /** Canonical EJSON _id when editing. */
  id?: string
  /** Initial text when inserting. */
  text?: string
}

export function DocumentDialog({ state, onClose, onSaved }: { state: DocDialogState | null; onClose: () => void; onSaved: () => void }) {
  const [text, setText] = useState('')
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [invalid, setInvalid] = useState('')
  const saveRef = useRef<() => void>(() => {})

  useEffect(() => {
    if (!state) return
    setError('')
    setInvalid('')
    if (state.mode === 'insert') {
      setText(state.text ?? '{\n  \n}')
      return
    }
    setLoading(true)
    Shell.GetDocument(state.sessionId, state.db, state.coll, state.id!)
      .then((ej) => Shell.FormatDocument(ej))
      .then(setText)
      .catch((err) => setError(errorMessage(err)))
      .finally(() => setLoading(false))
  }, [state])

  useEffect(() => {
    if (!state || loading) return
    const t = setTimeout(() => {
      Shell.ValidateDocument(text).then(setInvalid).catch(() => {})
    }, 300)
    return () => clearTimeout(t)
  }, [text, state, loading])

  const save = async () => {
    if (!state || saving) return
    setSaving(true)
    setError('')
    try {
      if (state.mode === 'insert') await Shell.InsertDocument(state.sessionId, state.db, state.coll, text)
      else await Shell.ReplaceDocument(state.sessionId, state.db, state.coll, state.id!, text)
      onSaved()
      onClose()
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setSaving(false)
    }
  }
  saveRef.current = save

  return (
    <Modal
      open={!!state}
      onOpenChange={(o) => !o && onClose()}
      title={state?.mode === 'insert' ? 'Insert document' : 'Edit document'}
      description={state ? `${state.db}.${state.coll}` : undefined}
      width="max-w-3xl"
      footer={
        <>
          <span className="mr-auto flex min-w-0 items-center gap-1.5 text-xs">
            {error ? (
              <span className="flex min-w-0 items-center gap-1 text-danger">
                <AlertCircle size={13} className="shrink-0" />
                <span className="truncate" title={error}>
                  {error}
                </span>
              </span>
            ) : invalid ? (
              <span className="truncate text-warn" title={invalid}>
                {invalid}
              </span>
            ) : (
              <span className="text-faint">
                Shell syntax — ObjectId(), ISODate(), NumberLong(), Double(). <Kbd>{mod}↵</Kbd> to save
              </span>
            )}
          </span>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" onClick={save} disabled={saving || loading || !!invalid}>
            {saving && <Spinner size={13} />}
            {state?.mode === 'insert' ? 'Insert' : 'Save'}
          </Button>
        </>
      }
    >
      <div className="h-[60vh] bg-sunken">
        {loading ? (
          <div className="flex h-full items-center justify-center text-muted">
            <Spinner />
          </div>
        ) : (
          <Editor
            value={text}
            onChange={setText}
            autoFocus
            className="h-full"
            keys={[{ key: 'Mod-Enter', run: () => void saveRef.current() }, { key: 'Mod-s', run: () => void saveRef.current() }]}
          />
        )}
      </div>
    </Modal>
  )
}
