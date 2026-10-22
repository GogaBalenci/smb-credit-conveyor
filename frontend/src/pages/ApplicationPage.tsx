import { useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { api, ApiError, downloadDocument } from '../api/client'
import type { ApplicationDetails, DocKind } from '../api/types'
import { useSession } from '../session'
import { Conveyor } from '../components/Conveyor'
import { ErrorBox, Spinner, StatusBadge, useAsync, usePolling } from '../components/ui'
import { dateTime, LIVE_STATUSES, money, months, SOURCE_TITLES, timeOnly } from '../lib/format'

export function ApplicationPage() {
  const { id = '' } = useParams()
  const q = useAsync(() => api.get(id), [id])
  const d = q.data
  usePolling(() => void q.reload(true), !!d && LIVE_STATUSES.includes(d.application.status))

  if (q.loading && !d) return <main className="page"><Spinner /></main>
  if (!d) return <main className="page"><ErrorBox error={q.error} /></main>
  const a = d.application

  return (
    <main className="page">
      <div className="page-head">
        <div>
          <div className="small muted"><Link to="/">Заявки</Link> / № {a.number}</div>
          <h1 style={{ marginTop: 6 }}>{d.product.name}</h1>
          <p>{money(a.amount)} на {months(a.term_months)} · подана {a.submitted_at ? dateTime(a.submitted_at) : 'ещё не отправлена'}</p>
        </div>
        <StatusBadge status={a.status} />
      </div>

      <div className="grid-main">
        <div>
          <section className="panel status-hero" aria-live="polite">
            <Conveyor status={a.status} />
            <div className="status-hero-text">
              <h2>{d.status_info.title}</h2>
              <p>{d.status_info.description}</p>
            </div>
            {d.pipeline ? (
              <div className="next-step delay" role="status">
                <span className="spinner" aria-hidden="true" />
                <div>
                  <b>{d.pipeline.state === 'failed' ? 'Проверка задерживается. ' : 'Небольшая задержка. '}</b>{d.pipeline.message}
                  {d.pipeline.next_attempt_at && <div className="small" style={{ marginTop: 4 }}>Следующая попытка — {timeOnly(d.pipeline.next_attempt_at)}</div>}
                </div>
              </div>
            ) : (
              <div className={`next-step ${d.status_info.client_action ? 'act' : ''}`}>
                <div><b>{d.status_info.client_action ? 'Что сделать: ' : 'Дальше: '}</b>{d.status_info.next_step}</div>
              </div>
            )}
          </section>
          <Actions d={d} reload={() => q.reload(true)} />
        </div>

        <aside>
          <section className="panel" aria-labelledby="tl-h">
            <h2 id="tl-h" style={{ marginBottom: 16 }}>История</h2>
            <Timeline d={d} />
          </section>
          {d.checks.length > 0 && (
            <section className="panel" aria-labelledby="ch-h">
              <h2 id="ch-h" style={{ marginBottom: 16 }}>Проверки банка</h2>
              <div className="checks">
                {d.checks.map((c) => (
                  <div key={c.source} className="check-row">
                    <span className={`check-icon ${c.status}`} aria-label={c.status === 'ok' ? 'пройдена' : c.status === 'warning' ? 'есть замечания' : 'не пройдена'}>
                      {c.status === 'ok' ? '✓' : '!'}
                    </span>
                    <div><b style={{ fontWeight: 500 }}>{SOURCE_TITLES[c.source]}</b><div className="muted small">{c.summary}</div></div>
                  </div>
                ))}
              </div>
            </section>
          )}
          {d.documents.length > 0 && (
            <section className="panel" aria-labelledby="doc-h">
              <h2 id="doc-h" style={{ marginBottom: 12 }}>Документы</h2>
              {d.documents.map((doc) => (
                <div key={doc.id} className="row small" style={{ justifyContent: 'space-between', padding: '6px 0' }}>
                  <span>{doc.filename}</span>
                  <button className="btn btn-ghost btn-sm" onClick={() => downloadDocument(a.id, doc)}>Скачать</button>
                </div>
              ))}
            </section>
          )}
        </aside>
      </div>
    </main>
  )
}

function Timeline({ d }: { d: ApplicationDetails }) {
  const { meta } = useSession()
  const title = (s: string) => meta?.statuses.find((x) => x.status === s)?.title ?? s
  const who = { client: 'Вы', system: 'Банк, автоматически', underwriter: 'Кредитный эксперт' }
  return (
    <ol className="timeline">
      {d.events.map((e) => (
        <li key={e.id}>
          <b>{e.from === e.to ? 'Комментарий' : title(e.to)}</b>
          <div className="small muted">{who[e.actor_kind]} · {dateTime(e.created_at)}</div>
          {e.comment && <div className="small" style={{ marginTop: 2 }}>{e.comment}</div>}
        </li>
      ))}
    </ol>
  )
}

function Actions({ d, reload }: { d: ApplicationDetails; reload: () => Promise<void> }) {
  const a = d.application
  const [error, setError] = useState<unknown>()
  const [busy, setBusy] = useState(false)

  async function run(fn: () => Promise<unknown>) {
    setBusy(true)
    setError(undefined)
    try { await fn(); await reload() } catch (e) { setError(e) } finally { setBusy(false) }
  }

  const fieldErrors = error instanceof ApiError ? Object.values(error.fields) : []

  return (
    <>
      {(a.status === 'NEEDS_INFO' || (a.status === 'DRAFT' && a.required_docs.length > 0) || a.status === 'MANUAL_REVIEW') && a.required_docs.length > 0 && (
        <section className="panel stack">
          <h2>{a.status === 'MANUAL_REVIEW' ? 'Документы' : 'Загрузите документы'}</h2>
          {a.required_docs.map((r) => <DocUpload key={r.kind} appId={a.id} kind={r.kind} reason={r.reason} provided={r.provided} onDone={reload} />)}
          {a.status === 'NEEDS_INFO' && (
            <div className="row">
              <button className="btn btn-primary" disabled={busy || a.required_docs.some((r) => !r.provided)} onClick={() => run(() => api.submit(a.id))}>
                Отправить заявку повторно
              </button>
              {a.required_docs.some((r) => !r.provided) && <span className="small muted">Кнопка станет доступна, когда загрузите все документы</span>}
            </div>
          )}
        </section>
      )}

      {a.status === 'DRAFT' && (
        <section className="panel stack">
          <h2>Черновик</h2>
          <p className="muted">Заявка сохранена, но ещё не отправлена в банк.</p>
          <div className="row">
            <button className="btn btn-primary" disabled={busy} onClick={() => run(() => api.submit(a.id))}>Отправить заявку</button>
            <button className="btn btn-ghost" disabled={busy} onClick={() => run(() => api.cancel(a.id))}>Удалить черновик</button>
          </div>
        </section>
      )}

      {(a.status === 'APPROVED' || a.status === 'COUNTER_OFFER') && a.decision?.offer && <OfferPanel d={d} reload={reload} />}

      {a.status === 'REJECTED' && a.decision && (
        <section className="panel stack">
          <h2>Почему банк отказал</h2>
          <ul style={{ margin: 0, paddingLeft: 20 }}>{a.decision.reasons.map((r) => <li key={r}>{r}</li>)}</ul>
          {a.decision.suggestions.length > 0 && <>
            <h3>Что можно сделать</h3>
            <ul style={{ margin: 0, paddingLeft: 20 }} className="muted">{a.decision.suggestions.map((r) => <li key={r}>{r}</li>)}</ul>
          </>}
          <div><Link to="/apply" className="btn btn-secondary">Подобрать другой продукт</Link></div>
        </section>
      )}

      {(a.status === 'SIGNED' || a.status === 'ISSUED') && a.decision?.offer && (
        <section className="panel stack">
          <h2>{a.status === 'ISSUED' ? 'Кредит выдан' : 'Готовим выдачу'}</h2>
          <dl className="kv">
            <dt>Сумма</dt><dd className="num">{money(a.decision.offer.amount)}</dd>
            <dt>Срок</dt><dd>{months(a.decision.offer.term_months)}</dd>
            <dt>Ставка</dt><dd>{a.decision.offer.rate}% годовых</dd>
            <dt>Ежемесячный платёж</dt><dd className="num">{money(a.decision.offer.monthly_payment)}</dd>
          </dl>
          {d.schedule && <Schedule d={d} />}
        </section>
      )}

      {(a.status === 'DRAFT' || a.status === 'NEEDS_INFO') && (
        <div style={{ marginTop: 12 }}>
          <button className="btn btn-ghost btn-sm" disabled={busy} onClick={() => run(() => api.cancel(a.id))}>Отозвать заявку</button>
        </div>
      )}
      {(error || fieldErrors.length > 0) && <div style={{ marginTop: 12 }}>
        {fieldErrors.length > 0 ? <div className="alert alert-error">{fieldErrors.join(' ')}</div> : <ErrorBox error={error} />}
      </div>}
    </>
  )
}

function DocUpload({ appId, kind, reason, provided, onDone }: { appId: string; kind: DocKind; reason: string; provided: boolean; onDone: () => Promise<void> }) {
  const { meta } = useSession()
  const [error, setError] = useState<unknown>()
  const [busy, setBusy] = useState(false)
  const title = meta?.doc_kinds.find((k) => k.code === kind)?.title ?? kind
  const inputId = `file-${kind}`

  async function upload(f: File | undefined) {
    if (!f) return
    setBusy(true)
    setError(undefined)
    try { await api.upload(appId, kind, f); await onDone() } catch (e) { setError(e) } finally { setBusy(false) }
  }

  return (
    <div className={`drop ${provided ? 'done' : ''}`}>
      <div>
        <b style={{ fontWeight: 500 }}>{title}</b>
        <div className="small muted">{provided ? 'Загружено' : reason}</div>
        {error instanceof ApiError && <div className="small" style={{ color: 'var(--red-ink)' }}>{Object.values(error.fields).join(' ') || error.message}</div>}
      </div>
      <label htmlFor={inputId} className={`btn btn-sm ${provided ? 'btn-ghost' : 'btn-secondary'}`} style={{ cursor: 'pointer' }}>
        {busy ? 'Загружаем…' : provided ? 'Заменить' : 'Выбрать файл'}
      </label>
      <input id={inputId} type="file" accept="application/pdf,image/png,image/jpeg" className="sr-only" onChange={(e) => upload(e.target.files?.[0])} />
    </div>
  )
}

function OfferPanel({ d, reload }: { d: ApplicationDetails; reload: () => Promise<void> }) {
  const a = d.application
  const o = a.decision!.offer!
  const [code, setCode] = useState('')
  const [challenge, setChallenge] = useState<{ demo_code?: string; hint: string } | null>(null)
  const [declining, setDeclining] = useState(false)
  const [reason, setReason] = useState('')
  const [error, setError] = useState<unknown>()
  const [busy, setBusy] = useState(false)

  async function run(fn: () => Promise<unknown>) {
    setBusy(true)
    setError(undefined)
    try { await fn(); await reload() } catch (e) { setError(e) } finally { setBusy(false) }
  }

  const changed = (x: boolean) => (x ? 'changed' : '')
  const requested = a.amount
  return (
    <section className="panel stack">
      <h2>{a.status === 'COUNTER_OFFER' ? 'Банк предлагает другие условия' : 'Условия кредита'}</h2>
      <ul style={{ margin: 0, paddingLeft: 20 }} className="muted">{a.decision!.reasons.map((r) => <li key={r}>{r}</li>)}</ul>
      <div className="compare">
        <div>
          <b>Вы запросили</b>
          <dl>
            <dt>Сумма</dt><dd>{money(requested)}</dd>
            <dt>Срок</dt><dd>{months(a.term_months)}</dd>
          </dl>
        </div>
        <div className="offer">
          <b>Предложение банка</b>
          <dl>
            <dt>Сумма</dt><dd className={changed(o.amount !== requested)}>{money(o.amount)}</dd>
            <dt>Срок</dt><dd className={changed(o.term_months !== a.term_months)}>{months(o.term_months)}</dd>
            <dt>Ставка</dt><dd>{o.rate}%</dd>
            <dt>Платёж в месяц</dt><dd><b>{money(o.monthly_payment)}</b></dd>
            <dt>Всего выплатите</dt><dd>{money(o.total_payment)}</dd>
          </dl>
        </div>
      </div>

      {!challenge ? (
        <div className="row">
          <button className="btn btn-primary" disabled={busy} onClick={() => run(async () => setChallenge(await api.signCode(a.id)))}>Подписать договор</button>
          {!declining && <button className="btn btn-ghost" disabled={busy} onClick={() => setDeclining(true)}>Отказаться</button>}
        </div>
      ) : (
        <form className="stack" onSubmit={(e) => { e.preventDefault(); void run(() => api.sign(a.id, code)) }}>
          <p>Введите код подтверждения, чтобы подписать договор простой электронной подписью.</p>
          {challenge.demo_code && (
            <div className="alert alert-info">Демо-режим: код <span className="demo-code">{challenge.demo_code}</span>. {challenge.hint}</div>
          )}
          <label htmlFor="sign-code" className="sr-only">Код подтверждения</label>
          <input id="sign-code" className="input code-input" inputMode="numeric" maxLength={6} autoComplete="one-time-code" value={code}
            onChange={(e) => setCode(e.target.value.replace(/\D/g, ''))} autoFocus />
          <div className="row">
            <button className="btn btn-primary" disabled={busy || code.length !== 6}>Подтвердить подпись</button>
            <button type="button" className="btn btn-ghost" onClick={() => run(async () => setChallenge(await api.signCode(a.id)))}>Новый код</button>
          </div>
        </form>
      )}

      {declining && (
        <div className="stack" style={{ gap: 8 }}>
          <label htmlFor="decline-reason" className="small muted">Почему не подходит? Это поможет предложить лучшие условия в следующий раз.</label>
          <textarea id="decline-reason" className="input" rows={2} value={reason} onChange={(e) => setReason(e.target.value)} />
          <div className="row">
            <button className="btn btn-secondary" disabled={busy} onClick={() => run(() => api.decline(a.id, reason))}>Отказаться от предложения</button>
            <button className="btn btn-ghost" onClick={() => setDeclining(false)}>Вернуться</button>
          </div>
        </div>
      )}
      {error instanceof ApiError && Object.keys(error.fields).length > 0
        ? <div className="alert alert-error">{Object.values(error.fields).join(' ')}</div>
        : <ErrorBox error={error} />}
      {d.schedule && <Schedule d={d} />}
    </section>
  )
}

function Schedule({ d }: { d: ApplicationDetails }) {
  const [all, setAll] = useState(false)
  const rows = all ? d.schedule! : d.schedule!.slice(0, 6)
  return (
    <details>
      <summary>График платежей</summary>
      <div className="table-wrap" style={{ marginTop: 8 }}>
        <table className="table">
          <thead><tr><th>№</th><th className="r">Платёж</th><th className="r">Основной долг</th><th className="r">Проценты</th><th className="r">Остаток</th></tr></thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.n}><td>{r.n}</td><td className="r num">{money(r.payment)}</td><td className="r num">{money(r.principal)}</td><td className="r num">{money(r.interest)}</td><td className="r num">{money(r.balance)}</td></tr>
            ))}
          </tbody>
        </table>
      </div>
      {!all && d.schedule!.length > 6 && <button className="btn btn-ghost btn-sm" onClick={() => setAll(true)}>Показать все {d.schedule!.length}</button>}
    </details>
  )
}
