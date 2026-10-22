import { useState, type FormEvent } from 'react'
import { Link, Navigate, useNavigate } from 'react-router-dom'
import { api, ApiError } from '../api/client'
import type { Company } from '../api/types'
import { useSession } from '../session'
import { Conveyor } from '../components/Conveyor'
import { ErrorBox, Field } from '../components/ui'

function AuthLayout({ children }: { children: React.ReactNode }) {
  return (
    <div className="auth">
      <aside className="auth-side">
        <div className="brand" style={{ color: '#fff' }}>
          <span className="brand-mark" aria-hidden="true">
            <svg width="16" height="16" viewBox="0 0 16 16"><path d="M2 10h12M2 5h7" stroke="#fff" strokeWidth="2.2" strokeLinecap="round" /></svg>
          </span>
          Кредитный кабинет
        </div>
        <div>
          <h1>Кредит для бизнеса без визита в банк</h1>
          <p>Расскажите, на что нужны деньги, — подберём продукт, заполним данные о компании по ИНН и покажем, на каком этапе заявка.</p>
        </div>
        <Conveyor status="SCORING" />
      </aside>
      <main className="auth-main">{children}</main>
    </div>
  )
}

export function LoginPage() {
  const { user, meta, signIn } = useSession()
  const nav = useNavigate()
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<unknown>()
  const [busy, setBusy] = useState(false)

  if (user) return <Navigate to={user.role === 'underwriter' ? '/bank' : '/'} replace />

  async function login(e?: FormEvent, creds?: { email: string; password: string }) {
    e?.preventDefault()
    setBusy(true)
    setError(undefined)
    try {
      const s = await api.login(creds?.email ?? email, creds?.password ?? password)
      signIn(s)
      nav(s.user.role === 'underwriter' ? '/bank' : '/')
    } catch (err) {
      setError(err)
    } finally {
      setBusy(false)
    }
  }

  return (
    <AuthLayout>
      <h1 style={{ fontSize: 24 }}>Вход</h1>
      <form className="stack" style={{ marginTop: 24 }} onSubmit={login}>
        <Field id="email" label="Email">
          <input id="email" className="input" type="email" autoComplete="username" value={email} onChange={(e) => setEmail(e.target.value)} required />
        </Field>
        <Field id="password" label="Пароль">
          <input id="password" className="input" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} required />
        </Field>
        <ErrorBox error={error} />
        <button className="btn btn-primary" disabled={busy}>Войти</button>
        <p className="small muted">Нет учётной записи? <Link to="/register">Зарегистрировать компанию</Link></p>
      </form>

      {meta?.demo_accounts && (
        <section style={{ marginTop: 32 }} aria-labelledby="demo-h">
          <h2 id="demo-h" style={{ fontSize: 15 }}>Демо-учётки для проверки</h2>
          <p className="small muted">Каждая компания приводит к своему исходу конвейера. Нажмите, чтобы войти.</p>
          <div className="demo-list">
            {meta.demo_accounts.map((a) => (
              <button key={a.email} type="button" disabled={busy}
                onClick={() => login(undefined, { email: a.email, password: meta.demo_password ?? '' })}>
                <b>{a.role === 'underwriter' ? 'Кредитный эксперт банка' : a.full_name}</b>
                <span>{a.scenario}</span>
              </button>
            ))}
          </div>
        </section>
      )}
    </AuthLayout>
  )
}

export function RegisterPage() {
  const { signIn } = useSession()
  const nav = useNavigate()
  const [form, setForm] = useState({ inn: '', full_name: '', email: '', password: '' })
  const [company, setCompany] = useState<Company | null>(null)
  const [fields, setFields] = useState<Record<string, string>>({})
  const [error, setError] = useState<unknown>()
  const [busy, setBusy] = useState(false)

  const set = (k: keyof typeof form) => (e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, [k]: e.target.value })

  async function lookup() {
    setFields({})
    setCompany(null)
    if (!/^\d{10}(\d{2})?$/.test(form.inn)) {
      setFields({ inn: 'ИНН — 10 цифр для организации или 12 для ИП' })
      return
    }
    try {
      const c = await api.lookupCompany(form.inn)
      setCompany(c)
      if (!form.full_name) setForm((f) => ({ ...f, full_name: c.director }))
    } catch (e) {
      if (e instanceof ApiError && e.fields.inn) setFields({ inn: e.fields.inn })
      else setError(e)
    }
  }

  async function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError(undefined)
    setFields({})
    try {
      signIn(await api.register(form))
      nav('/')
    } catch (err) {
      if (err instanceof ApiError && Object.keys(err.fields).length) setFields(err.fields)
      else setError(err)
    } finally {
      setBusy(false)
    }
  }

  return (
    <AuthLayout>
      <h1 style={{ fontSize: 24 }}>Регистрация компании</h1>
      <p className="muted" style={{ marginTop: 8 }}>Введите ИНН — название, адрес, ОКВЭД и руководителя мы возьмём из ЕГРЮЛ или ЕГРИП.</p>
      <form className="stack" style={{ marginTop: 24 }} onSubmit={submit}>
        <Field id="inn" label="ИНН компании или ИП" error={fields.inn}>
          <div className="row" style={{ flexWrap: 'nowrap' }}>
            <input id="inn" className={`input ${fields.inn ? 'invalid' : ''}`} inputMode="numeric" maxLength={12} value={form.inn}
              onChange={set('inn')} onBlur={() => form.inn && lookup()} required />
            <button type="button" className="btn btn-secondary" onClick={lookup}>Найти</button>
          </div>
        </Field>
        {company && (
          <div className="alert alert-info">
            <b>{company.name}</b><br />
            {company.okved} {company.okved_name}<br />
            {company.address}
          </div>
        )}
        <Field id="full_name" label="Ваше ФИО" error={fields.full_name}>
          <input id="full_name" className="input" value={form.full_name} onChange={set('full_name')} required />
        </Field>
        <Field id="reg-email" label="Email" error={fields.email}>
          <input id="reg-email" className="input" type="email" autoComplete="username" value={form.email} onChange={set('email')} required />
        </Field>
        <Field id="reg-password" label="Пароль" hint="Не короче 8 символов" error={fields.password}>
          <input id="reg-password" className="input" type="password" autoComplete="new-password" value={form.password} onChange={set('password')} required minLength={8} />
        </Field>
        <ErrorBox error={error} />
        <button className="btn btn-primary" disabled={busy}>Зарегистрироваться</button>
        <p className="small muted">Уже есть учётная запись? <Link to="/login">Войти</Link></p>
      </form>
    </AuthLayout>
  )
}
