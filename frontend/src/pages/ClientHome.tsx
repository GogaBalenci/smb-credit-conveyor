import { Link, useNavigate } from 'react-router-dom'
import { api } from '../api/client'
import type { Feed } from '../api/types'
import { useSession } from '../session'
import { Conveyor } from '../components/Conveyor'
import { ErrorBox, Spinner, StatusBadge, useAsync, usePolling } from '../components/ui'
import { dateTime, LIVE_STATUSES, months, moneyShort } from '../lib/format'

const STAGE_TITLES: Record<string, string> = {
  none: 'заявок пока нет', draft: 'черновик заявки', in_review: 'заявка на рассмотрении', needs_info: 'банк ждёт документы',
  offer: 'есть предложение банка', rejected: 'получен отказ', issued: 'кредит выдан',
}

export function ClientHome() {
  const { company, meta } = useSession()
  const apps = useAsync(() => api.list(), [])
  const feed = useAsync(() => api.feed(), [])
  const live = !!apps.data?.items.some((a) => LIVE_STATUSES.includes(a.status))
  usePolling(() => { void apps.reload(true); void feed.reload(true) }, live)

  const productName = (code: string) => meta?.products.find((p) => p.code === code)?.name ?? code

  return (
    <main className="page">
      <div className="page-head">
        <div>
          <h1>Заявки на кредит</h1>
          {company && <p>{company.name} · ИНН {company.inn}</p>}
        </div>
        <Link to="/apply" className="btn btn-primary">Подать заявку</Link>
      </div>

      <div className="grid-main">
        <section className="panel" aria-label="Мои заявки">
          {apps.loading ? <Spinner /> : <ErrorBox error={apps.error} />}
          {apps.data && apps.data.items.length === 0 && (
            <div className="empty">
              <h2>Здесь появятся ваши заявки</h2>
              <p>Ответьте на пару вопросов о том, на что нужны деньги, — подберём подходящий кредит и заполним данные о компании за вас.</p>
              <Link to="/apply" className="btn btn-primary">Подобрать кредит</Link>
            </div>
          )}
          <div className="app-list">
            {apps.data?.items.map((a) => (
              <Link key={a.id} to={`/applications/${a.id}`} className="app-row">
                <div>
                  <div className="row" style={{ gap: 10, marginBottom: 4 }}>
                    <span className="app-row-title">{productName(a.product_code)}</span>
                    <StatusBadge status={a.status} />
                  </div>
                  <div className="amount" style={{ fontSize: 20 }}>
                    {a.amount ? moneyShort(a.decision?.offer?.amount ?? a.amount) : 'Сумма не указана'}
                    {a.term_months ? <span className="muted small" style={{ fontFamily: 'var(--font)', fontWeight: 400 }}> на {months(a.decision?.offer?.term_months ?? a.term_months)}</span> : null}
                  </div>
                  <div className="small muted">№ {a.number} · обновлена {dateTime(a.updated_at)}</div>
                </div>
                <Conveyor status={a.status} compact />
              </Link>
            ))}
          </div>
        </section>

        <aside className="panel" aria-labelledby="feed-h">
          <div className="panel-head"><h2 id="feed-h">Для вашего бизнеса</h2></div>
          {feed.loading && !feed.data ? <Spinner /> : <ErrorBox error={feed.error} />}
          {feed.data && <FeedList feed={feed.data} />}
        </aside>
      </div>
    </main>
  )
}

function FeedList({ feed }: { feed: Feed }) {
  const nav = useNavigate()
  const p = feed.profile
  return (
    <>
      <p className="feed-profile">
        Подборка учитывает отрасль ({p.industry.trim()}), регион ({p.region}), срок работы бизнеса ({p.age_months} мес.)
        и то, что сейчас: {STAGE_TITLES[p.stage] ?? p.stage}.
      </p>
      {feed.items.map((it) => {
        const appId = it.action?.startsWith('open_application:') ? it.action.split(':')[1] : null
        return (
          <article key={it.id} className={`feed-item ${it.type === 'event' ? 'event' : ''}`}>
            <div className="small muted">{it.category} · {dateTime(it.published_at)}</div>
            <h3>{it.title}</h3>
            <p>{it.body}</p>
            {it.type === 'news' && <div className="why" aria-label="Почему вы видите эту новость">{it.reasons.map((r) => <span key={r}>{r}</span>)}</div>}
            {appId && <button className="btn btn-secondary btn-sm" style={{ marginTop: 10 }} onClick={() => nav(`/applications/${appId}`)}>Открыть заявку</button>}
            {it.action === 'new_application' && <button className="btn btn-secondary btn-sm" style={{ marginTop: 10 }} onClick={() => nav('/apply')}>Подобрать кредит</button>}
          </article>
        )
      })}
    </>
  )
}
