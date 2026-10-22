import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api, ApiError } from '../api/client'
import type { ApplicationInput, Financials, Prefill, Product, Purpose, Recommendation } from '../api/types'
import { useSession } from '../session'
import { ErrorBox, Field, MoneyInput, Spinner } from '../components/ui'
import { money, months } from '../lib/format'
import { annuity, debtLoad } from '../lib/finance'

const EMPTY_FIN: Financials = { annual_revenue: 0, prev_annual_revenue: 0, annual_net_profit: 0, monthly_obligations: 0, collateral_value: 0, employees: 0 }
const STEPS = ['Потребность', 'Данные о бизнесе', 'Отправка']

export function ApplyWizard() {
  const { meta } = useSession()
  const nav = useNavigate()
  const [step, setStep] = useState(0)
  const [need, setNeed] = useState({ purpose: '' as Purpose | '', amount: 1_000_000, term_months: 12, has_collateral: false, urgent: false })
  const [recs, setRecs] = useState<Recommendation[] | null>(null)
  const [product, setProduct] = useState<Product | null>(null)
  const [input, setInput] = useState<ApplicationInput>({ product_code: '', purpose: '', purpose_comment: '', amount: 0, term_months: 0, financials: EMPTY_FIN })
  const [prefill, setPrefill] = useState<Prefill | null>(null)
  const [consent, setConsent] = useState(false)
  const [fields, setFields] = useState<Record<string, string>>({})
  const [error, setError] = useState<unknown>()
  const [busy, setBusy] = useState(false)

  // Финансовые показатели подтягиваем сразу — к шагу 2 они уже будут готовы.
  useEffect(() => {
    api.prefill().then((p) => {
      setPrefill(p)
      setInput((i) => ({ ...i, financials: p.financials }))
    }).catch(() => setPrefill({ financials: EMPTY_FIN, sources: [], warnings: ['Не удалось получить данные ФНС и БКИ — заполните показатели вручную'] }))
  }, [])

  async function findProducts() {
    if (!need.purpose) { setFields({ purpose: 'Выберите, на что нужны деньги' }); return }
    setFields({})
    setBusy(true)
    try {
      setRecs((await api.recommend({ ...need, purpose: need.purpose })).items)
    } catch (e) { setError(e) } finally { setBusy(false) }
  }

  function choose(r: Recommendation) {
    setProduct(r.product)
    setInput((i) => ({ ...i, product_code: r.product.code, purpose: need.purpose, amount: r.suggested_amount, term_months: r.suggested_term,
      financials: { ...i.financials, collateral_value: r.product.requires_collateral ? i.financials.collateral_value : 0 } }))
    setStep(1)
  }

  async function send(submit: boolean) {
    setBusy(true)
    setError(undefined)
    setFields({})
    try {
      const app = await api.create(input)
      if (submit) await api.submit(app.id)
      nav(`/applications/${app.id}`)
    } catch (e) {
      if (e instanceof ApiError && Object.keys(e.fields).length) {
        setFields(e.fields)
        setStep(1)
      } else setError(e)
    } finally { setBusy(false) }
  }

  const setFin = (k: keyof Financials) => (v: number) => setInput((i) => ({ ...i, financials: { ...i.financials, [k]: v } }))
  const payment = product ? annuity(input.amount, product.base_rate, input.term_months) : 0
  const load = debtLoad(input.financials.annual_revenue, input.financials.monthly_obligations, payment)

  return (
    <main className="page" style={{ maxWidth: 880 }}>
      <div className="page-head"><h1>Новая заявка</h1></div>
      <ol className="steps" aria-label="Шаги">
        {STEPS.map((s, i) => (
          <li key={s} className={i === step ? 'active' : i < step ? 'done' : ''} aria-current={i === step ? 'step' : undefined}>
            <b>{i < step ? '✓' : i + 1}</b>{s}
          </li>
        ))}
      </ol>

      {step === 0 && (
        <div className="stack">
          <section className="panel stack">
            <h2>На что нужны деньги?</h2>
            <div className="purpose-grid" role="group" aria-label="Цель финансирования">
              {meta?.purposes.map((p) => (
                <button key={p.code} type="button" className="purpose" aria-pressed={need.purpose === p.code}
                  onClick={() => { setNeed({ ...need, purpose: p.code }); setRecs(null) }}>
                  <b>{p.title}</b><span>{p.hint}</span>
                </button>
              ))}
            </div>
            {fields.purpose && <span className="error small" style={{ color: 'var(--red-ink)' }}>{fields.purpose}</span>}
            <div className="fields-2">
              <Field id="need-amount" label="Сколько нужно">
                <MoneyInput id="need-amount" value={need.amount} onChange={(v) => { setNeed({ ...need, amount: v }); setRecs(null) }} />
              </Field>
              <Field id="need-term" label="На какой срок, месяцев">
                <input id="need-term" className="input" type="number" min={1} max={120} value={need.term_months || ''}
                  onChange={(e) => { setNeed({ ...need, term_months: Number(e.target.value) }); setRecs(null) }} />
              </Field>
            </div>
            <label className="check"><input type="checkbox" checked={need.has_collateral} onChange={(e) => { setNeed({ ...need, has_collateral: e.target.checked }); setRecs(null) }} />
              <span>Могу предложить залог — оборудование, транспорт или недвижимость</span></label>
            <label className="check"><input type="checkbox" checked={need.urgent} onChange={(e) => { setNeed({ ...need, urgent: e.target.checked }); setRecs(null) }} />
              <span>Деньги нужны срочно</span></label>
            <div><button className="btn btn-dark" onClick={findProducts} disabled={busy}>Подобрать кредит</button></div>
            <ErrorBox error={error} />
          </section>

          {recs && (
            <section className="stack" aria-label="Подходящие продукты">
              {recs.length === 0 && <div className="alert alert-info">Под эту цель нет продуктов — попробуйте выбрать другую.</div>}
              {recs.map((r) => (
                <article key={r.product.code} className={`rec ${r.best ? 'best' : ''}`}>
                  <div>
                    {r.best && <div className="small" style={{ color: 'var(--red-ink)', fontWeight: 600, marginBottom: 4 }}>Лучше всего подходит</div>}
                    <h3>{r.product.name}</h3>
                    <p className="small muted" style={{ marginTop: 4 }}>{r.product.description}</p>
                    <ul>{r.reasons.map((x) => <li key={x}>{x}</li>)}</ul>
                    {r.warnings.length > 0 && <ul style={{ color: 'var(--amber)' }}>{r.warnings.map((x) => <li key={x}>{x}</li>)}</ul>}
                  </div>
                  <div className="rec-side">
                    <div className="small muted">Платёж примерно</div>
                    <div className="amount" style={{ fontSize: 22 }}>{money(r.estimated_payment)}<span className="small muted" style={{ fontFamily: 'var(--font)', fontWeight: 400 }}> /мес.</span></div>
                    <div className="small muted">{money(r.suggested_amount)} на {months(r.suggested_term)}<br />решение: {r.product.decision_time}</div>
                    <button className={`btn ${r.best ? 'btn-primary' : 'btn-secondary'}`} onClick={() => choose(r)}>Выбрать</button>
                  </div>
                </article>
              ))}
            </section>
          )}
        </div>
      )}

      {step === 1 && product && (
        <section className="panel stack">
          <div className="panel-head" style={{ marginBottom: 0 }}>
            <h2>{product.name}</h2>
            <button className="btn btn-ghost btn-sm" onClick={() => setStep(0)}>Сменить продукт</button>
          </div>
          <div className="fields-2">
            <Field id="amount" label="Сумма" hint={`от ${money(product.min_amount)} до ${money(product.max_amount)}`} error={fields.amount}>
              <MoneyInput id="amount" value={input.amount} invalid={!!fields.amount} onChange={(v) => setInput({ ...input, amount: v })} />
            </Field>
            <Field id="term" label="Срок, месяцев" hint={`от ${product.min_term_months} до ${product.max_term_months}`} error={fields.term_months}>
              <input id="term" className={`input ${fields.term_months ? 'invalid' : ''}`} type="number" min={product.min_term_months} max={product.max_term_months}
                value={input.term_months || ''} onChange={(e) => setInput({ ...input, term_months: Number(e.target.value) })} />
            </Field>
          </div>
          <Field id="comment" label="Что именно планируете оплатить" hint="Необязательно, но помогает быстрее принять решение" error={fields.purpose_comment}>
            <textarea id="comment" className="input" rows={2} maxLength={500} value={input.purpose_comment} onChange={(e) => setInput({ ...input, purpose_comment: e.target.value })} />
          </Field>

          <div style={{ marginTop: 8 }}>
            <h3>Показатели бизнеса</h3>
            {!prefill ? <Spinner label="Запрашиваем данные ФНС и бюро кредитных историй…" /> : (
              <div className="stack" style={{ gap: 8, marginTop: 8 }}>
                {prefill.sources.length > 0 && <div className="alert alert-info">Заполнили за вас из открытых источников: {prefill.sources.join('; ')}. Проверьте и исправьте, если данные устарели.</div>}
                {prefill.warnings.map((w) => <div key={w} className="alert alert-warn">{w}</div>)}
              </div>
            )}
          </div>
          <div className="fields-2">
            <Field id="rev" label="Выручка за прошлый год" error={fields['financials.annual_revenue']}>
              <MoneyInput id="rev" value={input.financials.annual_revenue} invalid={!!fields['financials.annual_revenue']} onChange={setFin('annual_revenue')} />
            </Field>
            <Field id="prev" label="Выручка за позапрошлый год">
              <MoneyInput id="prev" value={input.financials.prev_annual_revenue} onChange={setFin('prev_annual_revenue')} />
            </Field>
            <Field id="profit" label="Чистая прибыль за год" hint="Убыток — со знаком минус">
              <MoneyInput id="profit" value={input.financials.annual_net_profit} onChange={setFin('annual_net_profit')} allowNegative />
            </Field>
            <Field id="obl" label="Платежи по текущим кредитам в месяц">
              <MoneyInput id="obl" value={input.financials.monthly_obligations} onChange={setFin('monthly_obligations')} />
            </Field>
            {product.requires_collateral && (
              <Field id="coll" label="Стоимость залога" error={fields['financials.collateral_value']}>
                <MoneyInput id="coll" value={input.financials.collateral_value} invalid={!!fields['financials.collateral_value']} onChange={setFin('collateral_value')} />
              </Field>
            )}
          </div>

          <div className="next-step">
            <div>
              Ориентировочный платёж — <b className="num">{money(payment)}</b> в месяц по ставке от {product.base_rate}%.
              {load !== null && <> Вместе с текущими кредитами это {Math.round(load * 100)}% месячной выручки{load > product.max_debt_load
                ? <> — <b>больше {Math.round(product.max_debt_load * 100)}%</b>, поэтому банк может предложить более длинный срок или меньшую сумму.</> : '.'}</>}
            </div>
          </div>
          <ErrorBox error={error} />
          <div className="row">
            <button className="btn btn-dark" onClick={() => { setFields({}); setStep(2) }}>Продолжить</button>
            <button className="btn btn-ghost" onClick={() => send(false)} disabled={busy}>Сохранить черновик</button>
          </div>
        </section>
      )}

      {step === 2 && product && (
        <section className="panel stack">
          <h2>Проверьте заявку</h2>
          <dl className="kv">
            <dt>Продукт</dt><dd>{product.name}</dd>
            <dt>Сумма и срок</dt><dd>{money(input.amount)} на {months(input.term_months)}</dd>
            <dt>Платёж примерно</dt><dd>{money(payment)} в месяц</dd>
            <dt>Выручка за год</dt><dd>{money(input.financials.annual_revenue)}</dd>
            <dt>Срок решения</dt><dd>{product.decision_time}</dd>
          </dl>
          <div className="next-step">
            <div>После отправки банк сверит данные с ЕГРЮЛ/ЕГРИП и ФНС, запросит кредитную историю и рассчитает условия. За статусом можно следить на странице заявки — ничего обновлять не нужно.</div>
          </div>
          <label className="check"><input type="checkbox" checked={consent} onChange={(e) => setConsent(e.target.checked)} />
            <span>Согласен на запрос кредитной истории компании в бюро кредитных историй и обработку данных для рассмотрения заявки</span></label>
          <ErrorBox error={error} />
          <div className="row">
            <button className="btn btn-primary" disabled={!consent || busy} onClick={() => send(true)}>{busy ? 'Отправляем…' : 'Отправить заявку'}</button>
            <button className="btn btn-ghost" onClick={() => setStep(1)}>Изменить данные</button>
          </div>
        </section>
      )}
    </main>
  )
}
