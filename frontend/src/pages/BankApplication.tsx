import { useEffect, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { api, ApiError, downloadDocument } from '../api/client'
import type { ApplicationDetails, DocKind, DossierSummary, ScoreResult } from '../api/types'
import { useSession } from '../session'
import { Conveyor } from '../components/Conveyor'
import { ErrorBox, Field, MoneyInput, Spinner, StatusBadge, useAsync, usePolling } from '../components/ui'
import { date, dateTime, LIVE_STATUSES, money, months, SOURCE_TITLES, timeOnly } from '../lib/format'

const ACTORS = { client: 'Клиент', system: 'Система', underwriter: 'Эксперт' }

export function BankApplication() {
  const { id = '' } = useParams()
  const { user, meta } = useSession()
  const statusTitle = (s: string) => meta?.statuses.find((x) => x.status === s)?.title ?? s
  const q = useAsync(() => api.get(id), [id])
  const d = q.data
  usePolling(() => void q.reload(true), !!d && LIVE_STATUSES.includes(d.application.status))

  if (q.loading && !d) return <main className="page"><Spinner /></main>
  if (!d) return <main className="page"><ErrorBox error={q.error} /></main>
  const a = d.application
  const c = d.company
  const f = a.financials

  return (
    <main className="page">
      <div className="page-head">
        <div>
          <div className="small muted"><Link to="/bank">Очередь</Link> / № {a.number}</div>
          <h1 style={{ marginTop: 6 }}>{c.name}</h1>
          <p>{d.product.name}: {money(a.amount)} на {months(a.term_months)}</p>
        </div>
        <div className="row">
          <StatusBadge status={a.status} />
          {d.actions.includes('take') && a.assignee_id !== user?.id && (
            <button className="btn btn-secondary btn-sm" onClick={async () => { await api.take(a.id); await q.reload(true) }}>Взять в работу</button>
          )}
          {a.assignee_id === user?.id && <span className="small muted">В работе у вас</span>}
        </div>
      </div>

      <div className="grid-main">
        <div>
          <section className="panel"><Conveyor status={a.status} /></section>
          {d.pipeline && (
            <div className="alert alert-warn" role="status" style={{ marginTop: 16 }}>
              <b>{d.pipeline.state === 'failed' ? 'Конвейер исчерпал попытки. ' : 'Повтор после сбоя источника. '}</b>
              Попытка {d.pipeline.attempts} из {d.pipeline.max_attempts}
              {d.pipeline.next_attempt_at && <>, следующая в {timeOnly(d.pipeline.next_attempt_at)}</>}.
              {d.pipeline.technical_error && <pre className="json" style={{ marginTop: 8, marginBottom: 0 }}>{d.pipeline.technical_error}</pre>}
            </div>
          )}
          {a.status !== 'DRAFT' && <SummaryPanel appId={a.id} version={a.version} />}
          {a.status === 'MANUAL_REVIEW' && <DecisionForm d={d} onDone={() => q.reload(true)} />}
          {a.decision && a.status !== 'MANUAL_REVIEW' && (
            <section className="panel stack">
              <h2>Решение</h2>
              <p className="muted small">{a.decision.decided_by === 'system' ? 'Автоматически по кредитной политике' : 'Кредитный эксперт'} · {a.decision.policy_version} · {dateTime(a.decision.decided_at)}</p>
              <ul style={{ margin: 0, paddingLeft: 20 }}>{a.decision.reasons.map((r) => <li key={r}>{r}</li>)}</ul>
              {a.decision.offer && <p>Предложение: <b>{money(a.decision.offer.amount)}</b> на {months(a.decision.offer.term_months)} под {a.decision.offer.rate}%, платёж {money(a.decision.offer.monthly_payment)}</p>}
            </section>
          )}
          {a.score && <Scorecard s={a.score} />}
          <section className="panel">
            <h2 style={{ marginBottom: 16 }}>Финансовые показатели</h2>
            <dl className="kv">
              <dt>Выручка за год</dt><dd className="num">{money(f.annual_revenue)}</dd>
              <dt>Выручка за предыдущий год</dt><dd className="num">{money(f.prev_annual_revenue)}</dd>
              <dt>Чистая прибыль</dt><dd className="num">{money(f.annual_net_profit)}</dd>
              <dt>Платежи по кредитам, заявлено</dt><dd className="num">{money(f.monthly_obligations)} в месяц</dd>
              {f.collateral_value > 0 && <><dt>Залог</dt><dd className="num">{money(f.collateral_value)}</dd></>}
              <dt>Цель</dt><dd>{a.purpose_comment || '—'}</dd>
            </dl>
          </section>
          {d.checks.length > 0 && (
            <section className="panel">
              <h2 style={{ marginBottom: 16 }}>Внешние проверки</h2>
              <div className="checks">
                {d.checks.map((ch) => (
                  <div key={ch.source} className="check-row">
                    <span className={`check-icon ${ch.status}`}>{ch.status === 'ok' ? '✓' : '!'}</span>
                    <div>
                      <b style={{ fontWeight: 500 }}>{SOURCE_TITLES[ch.source]}</b> <span className="small muted">{dateTime(ch.checked_at)}</span>
                      <div className="small">{ch.summary}</div>
                      <details><summary>Ответ источника</summary><pre className="json">{JSON.stringify(ch.payload, null, 2)}</pre></details>
                    </div>
                  </div>
                ))}
              </div>
            </section>
          )}
        </div>

        <aside>
          <section className="panel">
            <h2 style={{ marginBottom: 16 }}>Компания</h2>
            <dl className="kv small">
              <dt>ИНН</dt><dd>{c.inn}</dd>
              <dt>ОГРН</dt><dd>{c.ogrn}</dd>
              <dt>Регистрация</dt><dd>{c.registered_at ? date(c.registered_at) : '—'}</dd>
              <dt>Статус</dt><dd>{c.registry_status === 'ACTIVE' ? 'Действует' : c.registry_status}</dd>
              <dt>ОКВЭД</dt><dd>{c.okved} {c.okved_name}</dd>
              <dt>Регион</dt><dd>{c.region}</dd>
              <dt>Руководитель</dt><dd>{c.director}</dd>
            </dl>
          </section>
          {d.documents.length > 0 && (
            <section className="panel">
              <h2 style={{ marginBottom: 12 }}>Документы</h2>
              {d.documents.map((doc) => (
                <div key={doc.id} className="row small" style={{ justifyContent: 'space-between', padding: '6px 0' }}>
                  <span>{doc.filename}<br /><span className="muted">{(doc.size_bytes / 1024).toFixed(0)} КБ · SHA-256 {doc.sha256.slice(0, 10)}…</span></span>
                  <button className="btn btn-ghost btn-sm" onClick={() => downloadDocument(a.id, doc)}>Скачать</button>
                </div>
              ))}
            </section>
          )}
          <section className="panel">
            <h2 style={{ marginBottom: 16 }}>Журнал</h2>
            <ol className="timeline">
              {d.events.map((e) => (
                <li key={e.id}>
                  <b>{e.from === e.to ? 'Комментарий' : e.from ? `${statusTitle(e.from)} → ${statusTitle(e.to)}` : statusTitle(e.to)}</b>
                  <div className="small muted">{ACTORS[e.actor_kind]} · {dateTime(e.created_at)}</div>
                  {e.comment && <div className="small">{e.comment}</div>}
                </li>
              ))}
            </ol>
          </section>
        </aside>
      </div>
    </main>
  )
}

/** Сводка досье: локальная модель с открытыми весами пересказывает эксперту факты заявки. */
function SummaryPanel({ appId, version }: { appId: string; version: number }) {
  const [sum, setSum] = useState<DossierSummary | null>(null)
  const [error, setError] = useState<unknown>()
  const [busy, setBusy] = useState(false)
  useEffect(() => { setSum(null) }, [appId, version])

  async function load(refresh: boolean) {
    setBusy(true)
    setError(undefined)
    try { setSum(await api.summary(appId, refresh)) } catch (e) { setError(e) } finally { setBusy(false) }
  }

  return (
    <section className="panel stack" aria-labelledby="sum-h" aria-busy={busy}>
      <div className="panel-head" style={{ marginBottom: 0 }}>
        <h2 id="sum-h">Сводка досье</h2>
        {sum && !busy && <button className="btn btn-ghost btn-sm" onClick={() => load(true)}>Составить заново</button>}
      </div>
      {!sum && !busy && (
        <div className="row">
          <button className="btn btn-secondary" onClick={() => load(false)}>Составить сводку</button>
          <span className="small muted">Коротко: профиль, сильные стороны, риски и что проверить</span>
        </div>
      )}
      {busy && <Spinner label="Модель изучает досье — это может занять до минуты…" />}
      <ErrorBox error={error} />
      {sum && !busy && (
        <>
          {sum.warning && <div className="alert alert-warn">{sum.warning}</div>}
          <dl className="summary">
            {sum.text.split('\n').map((line, i) => {
              const at = line.indexOf(':')
              if (at <= 0 || at >= 30) return <div key={i}><dd>{line}</dd></div>
              const label = line.slice(0, at)
              const byModel = sum.model_sections?.includes(label)
              return (
                <div key={i}>
                  <dt>{label}{byModel && <span className="ai-tag" title="Сформулировано моделью, сверено с факторами скоринга">ИИ</span>}</dt>
                  <dd>{line.slice(at + 1).trim()}</dd>
                </div>
              )
            })}
          </dl>
          <p className="small muted">
            {sum.source === 'llm'
              ? `Факты — из данных заявки. Строки с пометкой «ИИ» сформулировала локальная модель ${sum.model} (открытые веса) за ${(sum.duration_ms / 1000).toFixed(1)} с; каждое утверждение сверено с факторами скоринга.`
              : 'Составлено по правилам из данных заявки.'}
            {' '}Решение принимает эксперт.
          </p>
        </>
      )}
    </section>
  )
}

function Scorecard({ s }: { s: ScoreResult }) {
  const max = Math.max(...s.factors.map((f) => Math.abs(f.points)), 1)
  return (
    <section className="panel">
      <div className="panel-head">
        <h2>Скоринг</h2>
        <span className="small muted">{s.model_version}</span>
      </div>
      <div className="row" style={{ gap: 24, marginBottom: 16 }}>
        <div><div className="grade">{s.grade}</div><div className="small muted">рейтинг</div></div>
        <div><div className="amount" style={{ fontSize: 24 }}>{s.score}</div><div className="small muted">баллов, база 600</div></div>
        <div><div className="amount" style={{ fontSize: 24 }}>{(s.pd * 100).toFixed(1)}%</div><div className="small muted">вероятность дефолта</div></div>
      </div>
      {s.stop_factors.length > 0 && <div className="alert alert-error" style={{ marginBottom: 12 }}>Стоп-факторы: {s.stop_factors.join(', ')}</div>}
      {s.factors.map((f) => (
        <div key={f.code} className="factor">
          <div>{f.title}<div className="small muted">{f.value}</div></div>
          <div className="factor-bar" aria-hidden="true">
            <i className={f.points >= 0 ? 'pos' : 'neg'} style={{ width: `${(Math.abs(f.points) / max) * 50}%` }} />
          </div>
          <div className="r num" style={{ textAlign: 'right', color: f.points > 0 ? 'var(--green)' : f.points < 0 ? 'var(--red-ink)' : 'var(--ink-3)' }}>
            {f.points > 0 ? `+${f.points}` : f.points}
          </div>
        </div>
      ))}
    </section>
  )
}

function DecisionForm({ d, onDone }: { d: ApplicationDetails; onDone: () => Promise<void> }) {
  const { meta } = useSession()
  const a = d.application
  const pre = a.decision?.offer
  const [action, setAction] = useState<'approve' | 'reject' | 'request_info'>('approve')
  const [amount, setAmount] = useState(pre?.amount ?? a.amount)
  const [term, setTerm] = useState(pre?.term_months ?? a.term_months)
  const [rate, setRate] = useState(pre?.rate ?? d.product.base_rate)
  const [comment, setComment] = useState('')
  const [docs, setDocs] = useState<DocKind[]>([])
  const [error, setError] = useState<unknown>()
  const [busy, setBusy] = useState(false)
  useEffect(() => { setError(undefined) }, [action])

  async function submit() {
    setBusy(true)
    setError(undefined)
    try {
      await api.decide(a.id, action === 'approve' ? { action, amount, term_months: term, rate, comment } : action === 'reject' ? { action, comment } : { action, comment, documents: docs })
      await onDone()
    } catch (e) { setError(e) } finally { setBusy(false) }
  }

  const fe = error instanceof ApiError ? error.fields : {}
  const counter = amount !== a.amount || term !== a.term_months
  return (
    <section className="panel stack">
      <h2>Решение эксперта</h2>
      {a.decision && (
        <div className="alert alert-info">
          <b>Почему на ручном рассмотрении:</b> {a.decision.reasons.join(' ')}
          {pre && <> Рекомендация системы: {money(pre.amount)} на {months(pre.term_months)} под {pre.rate}%.</>}
        </div>
      )}
      <div className="tabs" role="tablist" style={{ marginBottom: 0 }}>
        <button role="tab" aria-selected={action === 'approve'} onClick={() => setAction('approve')}>Одобрить</button>
        <button role="tab" aria-selected={action === 'request_info'} onClick={() => setAction('request_info')}>Запросить документы</button>
        <button role="tab" aria-selected={action === 'reject'} onClick={() => setAction('reject')}>Отказать</button>
      </div>

      {action === 'approve' && (
        <div className="fields-2">
          <Field id="d-amount" label="Сумма" error={fe.amount}><MoneyInput id="d-amount" value={amount} onChange={setAmount} invalid={!!fe.amount} /></Field>
          <Field id="d-term" label="Срок, месяцев" error={fe.term_months}>
            <input id="d-term" className="input" type="number" value={term} onChange={(e) => setTerm(Number(e.target.value))} />
          </Field>
          <Field id="d-rate" label="Ставка, % годовых" error={fe.rate}>
            <input id="d-rate" className="input" type="number" step="0.1" value={rate} onChange={(e) => setRate(Number(e.target.value))} />
          </Field>
          <div className="field" style={{ justifyContent: 'flex-end' }}>
            <span className="small muted">{counter ? 'Условия отличаются от запроса — клиент получит контрпредложение' : 'Клиент получит одобрение на запрошенных условиях'}</span>
          </div>
        </div>
      )}
      {action === 'request_info' && (
        <div className="stack" style={{ gap: 8 }}>
          {meta?.doc_kinds.map((k) => (
            <label key={k.code} className="check">
              <input type="checkbox" checked={docs.includes(k.code)} onChange={(e) => setDocs(e.target.checked ? [...docs, k.code] : docs.filter((x) => x !== k.code))} />
              <span>{k.title}</span>
            </label>
          ))}
          {fe.documents && <span className="small" style={{ color: 'var(--red-ink)' }}>{fe.documents}</span>}
        </div>
      )}
      <Field id="d-comment" label={action === 'reject' ? 'Причина отказа — клиент увидит её' : 'Комментарий для клиента'} error={fe.comment}>
        <textarea id="d-comment" className={`input ${fe.comment ? 'invalid' : ''}`} rows={3} value={comment} onChange={(e) => setComment(e.target.value)} />
      </Field>
      {!Object.keys(fe).length && <ErrorBox error={error} />}
      <div>
        <button className={`btn ${action === 'reject' ? 'btn-dark' : 'btn-primary'}`} disabled={busy} onClick={submit}>
          {action === 'approve' ? (counter ? 'Отправить контрпредложение' : 'Одобрить') : action === 'reject' ? 'Отказать' : 'Запросить документы'}
        </button>
      </div>
    </section>
  )
}
