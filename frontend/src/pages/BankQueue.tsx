import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api } from '../api/client'
import type { Status } from '../api/types'
import { useSession } from '../session'
import { ErrorBox, Spinner, StatusBadge, useAsync, usePolling } from '../components/ui'
import { dateTime, money } from '../lib/format'

const TABS: { id: string; title: string; statuses?: Status[]; mine?: boolean }[] = [
  { id: 'review', title: 'Ждут решения', statuses: ['MANUAL_REVIEW'] },
  { id: 'mine', title: 'Мои', statuses: ['MANUAL_REVIEW'], mine: true },
  { id: 'pipeline', title: 'В конвейере', statuses: ['SUBMITTED', 'CHECKING', 'SCORING'] },
  { id: 'client', title: 'Ждут клиента', statuses: ['NEEDS_INFO', 'APPROVED', 'COUNTER_OFFER'] },
  { id: 'done', title: 'Завершены', statuses: ['ISSUED', 'SIGNED', 'REJECTED', 'DECLINED'] },
  { id: 'all', title: 'Все' },
]

export function BankQueue() {
  const { meta } = useSession()
  const nav = useNavigate()
  const [tab, setTab] = useState(TABS[0])
  const [q, setQ] = useState('')
  const list = useAsync(() => api.list({ status: tab.statuses, q, assignee: tab.mine ? 'me' : undefined }), [tab.id, q])
  const stats = useAsync(() => api.stats(), [])
  usePolling(() => { void list.reload(true); void stats.reload(true) }, true, 3000)

  const s = stats.data
  const count = (st?: Status[]) => (st && s ? st.reduce((n, x) => n + (s.by_status[x] ?? 0), 0) : s?.total)
  const productName = (code: string) => meta?.products.find((p) => p.code === code)?.name ?? code

  return (
    <main className="page">
      <div className="page-head">
        <div>
          <h1>Кредитный конвейер</h1>
          <p>Заявки малого бизнеса: автоматические решения и очередь на экспертизу</p>
        </div>
      </div>

      {s && (
        <div className="kpis">
          <div><b>{s.total}</b><span>заявок всего</span></div>
          <div><b>{s.by_status.MANUAL_REVIEW ?? 0}</b><span>ждут решения эксперта</span></div>
          <div><b>{Math.round(s.automation_rate * 100)}%</b><span>решений без участия человека</span></div>
          <div><b>{s.avg_auto_decision_sec ? `${Math.round(s.avg_auto_decision_sec)} с` : '—'}</b><span>среднее время авторешения</span></div>
        </div>
      )}

      <section className="panel">
        <div className="tabs" role="tablist">
          {TABS.map((t) => (
            <button key={t.id} role="tab" aria-selected={t.id === tab.id} onClick={() => setTab(t)}>
              {t.title}{!t.mine && <span className="count">{count(t.statuses) ?? ''}</span>}
            </button>
          ))}
        </div>
        <div className="row" style={{ marginBottom: 12 }}>
          <label htmlFor="search" className="sr-only">Поиск</label>
          <input id="search" className="input" style={{ maxWidth: 360 }} placeholder="Номер заявки, компания или ИНН" value={q} onChange={(e) => setQ(e.target.value)} />
        </div>
        {list.loading && !list.data ? <Spinner /> : <ErrorBox error={list.error} />}
        {list.data && list.data.items.length === 0 && <p className="muted" style={{ padding: '24px 0' }}>В этом разделе заявок нет.</p>}
        {list.data && list.data.items.length > 0 && (
          <div className="table-wrap">
            <table className="table">
              <thead><tr><th>№</th><th>Компания</th><th>Продукт</th><th className="r">Сумма</th><th>Рейтинг</th><th>Статус</th><th>Обновлена</th></tr></thead>
              <tbody>
                {list.data.items.map((a) => (
                  <tr key={a.id} className="clickable" tabIndex={0} onClick={() => nav(`/bank/applications/${a.id}`)}
                    onKeyDown={(e) => e.key === 'Enter' && nav(`/bank/applications/${a.id}`)}>
                    <td className="num">{a.number}</td>
                    <td>{a.company_name}</td>
                    <td>{productName(a.product_code)}</td>
                    <td className="r num">{money(a.amount)}</td>
                    <td>{a.score ? <b>{a.score.grade}</b> : <span className="muted">—</span>}{a.score && <span className="muted small"> {a.score.score}</span>}</td>
                    <td><StatusBadge status={a.status} /></td>
                    <td className="small muted">{dateTime(a.updated_at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      {s && (
        <section className="panel" style={{ marginTop: 16 }}>
          <div className="panel-head"><h2>Очередь конвейера</h2></div>
          <p className="small muted">
            Задач в очереди: {s.queue.pending}, повторяются после сбоя источника: {s.queue.retrying}, исчерпали попытки: {s.queue.failed}.
          </p>
          <FaultControl />
        </section>
      )}
    </main>
  )
}

/** Демонстрация устойчивости: включаем сбои внешних источников и смотрим, как конвейер повторяет запросы. */
function FaultControl() {
  const f = useAsync(() => api.faults(), [])
  const [error, setError] = useState<unknown>()
  const set = async (rate: number) => {
    try { setError(undefined); f.setData(await api.setFaults({ failure_rate: rate })) } catch (e) { setError(e) }
  }
  if (!f.data) return <ErrorBox error={f.error} />
  return (
    <div className="stack" style={{ gap: 8, marginTop: 12 }}>
      <div className="small">Сбои внешних источников (ЕГРЮЛ, ФНС, БКИ) для проверки устойчивости:</div>
      <div className="row">
        {[0, 0.3, 1].map((r) => (
          <button key={r} className={`btn btn-sm ${f.data!.failure_rate === r ? 'btn-dark' : 'btn-secondary'}`} onClick={() => set(r)}>
            {r === 0 ? 'Без сбоев' : r === 1 ? 'Источники недоступны' : '30% запросов с ошибкой'}
          </button>
        ))}
      </div>
      <ErrorBox error={error} />
    </div>
  )
}
