import {
  useEffect,
  useRef,
  useState,
  type FormEvent,
  type ReactNode,
} from 'react'
import { CircleAlert, LoaderCircle, X } from 'lucide-react'
import { errorMessage } from './api'
import type { Phase } from './types'

export const phaseNames: Record<Phase, string> = {
  Pending: 'Queued',
  Running: 'Running',
  Succeeded: 'Succeeded',
  Failed: 'Failed',
  Cancelling: 'Cancelling',
  Cancelled: 'Cancelled',
  RequiresReview: 'Needs review',
}
export function Badge({ phase }: { phase: Phase }) {
  return (
    <span className={`badge badge-${phase.toLowerCase()}`}>
      {phaseNames[phase]}
    </span>
  )
}
export function date(value?: string) {
  if (!value) return '—'
  const moment = new Date(value)
  return Number.isNaN(moment.valueOf())
    ? '—'
    : moment.toLocaleString(undefined, {
        month: 'short',
        day: 'numeric',
        hour: 'numeric',
        minute: '2-digit',
      })
}
export function bytes(value: number) {
  return value < 1024
    ? `${value} B`
    : value < 1048576
      ? `${(value / 1024).toFixed(1)} KB`
      : `${(value / 1048576).toFixed(1)} MB`
}
export function Alert({ children }: { children: ReactNode }) {
  return (
    <div className="alert" role="alert">
      <CircleAlert size={18} />
      {children}
    </div>
  )
}
export function Empty({ children }: { children: ReactNode }) {
  return <div className="empty">{children}</div>
}
export function Loading() {
  return (
    <div className="empty">
      <LoaderCircle className="animate-spin" size={18} /> Loading…
    </div>
  )
}
export function Modal({
  title,
  close,
  children,
}: {
  title: string
  close: () => void
  children: ReactNode
}) {
  const ref = useRef<HTMLDialogElement>(null)
  useEffect(() => {
    ref.current?.showModal()
  }, [])
  return (
    <dialog
      ref={ref}
      onCancel={(event) => {
        event.preventDefault()
        close()
      }}
      aria-labelledby="dialog-title"
    >
      <div className="dialog-head">
        <h2 id="dialog-title">{title}</h2>
        <button
          type="button"
          className="icon-button"
          aria-label="Close"
          onClick={close}
        >
          <X size={19} />
        </button>
      </div>
      {children}
    </dialog>
  )
}
export function ActionForm({
  children,
  submit,
  label = 'Save',
}: {
  children: ReactNode
  submit: (fields: FormData) => Promise<void>
  label?: string
}) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const onSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const values = new FormData(event.currentTarget)
    setBusy(true)
    setError('')
    try {
      await submit(values)
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setBusy(false)
    }
  }
  return (
    <form onSubmit={onSubmit} className="action-form">
      {error && <Alert>{error}</Alert>}
      <fieldset disabled={busy}>{children}</fieldset>
      <div className="form-actions">
        <button type="submit" className="button primary" disabled={busy}>
          {busy ? 'Working…' : label}
        </button>
      </div>
    </form>
  )
}
