import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { ApiError } from '../api/client'
import type { Status } from '../api/types'
import { useSession } from '../session'
import { tone } from '../lib/format'

export function StatusBadge({ status }: { status: Status }) {
  const { meta } = useSession()
  const title = meta?.statuses.find((s) => s.status === status)?.title ?? status
  return <span className={`badge tone-${tone(status)}`}>{title}</span>
}

export function Spinner({ label = 'Загружаем…' }: { label?: string }) {
  return <div className="loading" role="status"><span className="spinner" aria-hidden="true" />{label}</div>
}

export function ErrorBox({ error }: { error: unknown }) {
  if (!error) return null
  const msg = error instanceof ApiError ? error.message : 'Что-то пошло не так. Обновите страницу.'
  const rid = error instanceof ApiError && error.requestId ? ` Код обращения: ${error.requestId}.` : ''
  return <div className="alert alert-error" role="alert">{msg}{rid}</div>
}

export function Field({ label, hint, error, children, id }: { label: string; hint?: ReactNode; error?: string; children: ReactNode; id: string }) {
  return (
    <div className="field">
      <label htmlFor={id}>{label}</label>
      {children}
      {error ? <span className="error" id={`${id}-err`}>{error}</span> : hint ? <span className="hint">{hint}</span> : null}
    </div>
  )
}

/** Поле суммы: показывает разряды, хранит число. */
export function MoneyInput({ id, value, onChange, invalid, allowNegative }: { id: string; value: number; onChange: (v: number) => void; invalid?: boolean; allowNegative?: boolean }) {
  const fmt = (v: number) => (v ? new Intl.NumberFormat('ru-RU').format(v) : '')
  return (
    <input
      id={id}
      className={`input input-money ${invalid ? 'invalid' : ''}`}
      inputMode="numeric"
      value={fmt(value)}
      aria-invalid={invalid || undefined}
      onChange={(e) => {
        const raw = e.target.value.replace(allowNegative ? /[^\d-]/g : /\D/g, '')
        const n = Number(raw)
        onChange(Number.isFinite(n) ? n : 0)
      }}
    />
  )
}

/** Загрузка данных с состоянием и перезапросом. */
export function useAsync<T>(fn: () => Promise<T>, deps: unknown[]) {
  const [data, setData] = useState<T | undefined>()
  const [error, setError] = useState<unknown>()
  const [loading, setLoading] = useState(true)
  const fnRef = useRef(fn)
  fnRef.current = fn

  const reload = useCallback(async (silent = false) => {
    if (!silent) setLoading(true)
    try {
      setData(await fnRef.current())
      setError(undefined)
    } catch (e) {
      setError(e)
    } finally {
      setLoading(false)
    }
  }, [])

  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => { void reload() }, deps)
  return { data, error, loading, reload, setData }
}

/** Периодический вызов, пока active=true (статус заявки меняется на лету). */
export function usePolling(cb: () => void, active: boolean, ms = 1500) {
  const ref = useRef(cb)
  ref.current = cb
  useEffect(() => {
    if (!active) return
    const t = setInterval(() => ref.current(), ms)
    return () => clearInterval(t)
  }, [active, ms])
}
