import { create } from 'zustand'
import type { ReactNode } from 'react'
import { Button, Modal } from './ui'

interface ConfirmOptions {
  title: string
  message: ReactNode
  confirmLabel?: string
  danger?: boolean
}

const useConfirm = create<{ opts: ConfirmOptions | null; resolve?: (ok: boolean) => void }>(() => ({ opts: null }))

/** Asks the user to confirm an action; resolves to true when confirmed. */
export function confirmAction(opts: ConfirmOptions): Promise<boolean> {
  return new Promise((resolve) => useConfirm.setState({ opts, resolve }))
}

export function ConfirmHost() {
  const { opts, resolve } = useConfirm()
  const close = (ok: boolean) => {
    resolve?.(ok)
    useConfirm.setState({ opts: null, resolve: undefined })
  }
  return (
    <Modal
      open={!!opts}
      onOpenChange={(o) => !o && close(false)}
      title={opts?.title ?? ''}
      width="max-w-md"
      footer={
        <>
          <Button onClick={() => close(false)}>Cancel</Button>
          <Button data-autofocus variant={opts?.danger ? 'danger' : 'primary'} onClick={() => close(true)}>
            {opts?.confirmLabel ?? 'Confirm'}
          </Button>
        </>
      }
    >
      <div className="px-5 py-4 text-[13px] text-muted">{opts?.message}</div>
    </Modal>
  )
}
