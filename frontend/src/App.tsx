import { Navigate, NavLink, Outlet, Route, Routes, useNavigate } from 'react-router-dom'
import { useSession } from './session'
import { Spinner } from './components/ui'
import { LoginPage, RegisterPage } from './pages/Auth'
import { ClientHome } from './pages/ClientHome'
import { ApplyWizard } from './pages/ApplyWizard'
import { ApplicationPage } from './pages/ApplicationPage'
import { BankQueue } from './pages/BankQueue'
import { BankApplication } from './pages/BankApplication'

function Shell() {
  const { user, company, signOut } = useSession()
  const nav = useNavigate()
  if (!user) return <Navigate to="/login" replace />
  const isBank = user.role === 'underwriter'
  return (
    <>
      <header className="topbar">
        <div className="topbar-inner">
          <NavLink to={isBank ? '/bank' : '/'} className="brand" end>
            <span className="brand-mark" aria-hidden="true">
              <svg width="16" height="16" viewBox="0 0 16 16"><path d="M2 10h12M2 5h7" stroke="#fff" strokeWidth="2.2" strokeLinecap="round" /></svg>
            </span>
            {isBank ? 'Кредитный конвейер' : 'Кредитный кабинет'}
          </NavLink>
          <nav className="nav" aria-label="Разделы">
            {isBank ? (
              <NavLink to="/bank" end>Заявки</NavLink>
            ) : (
              <>
                <NavLink to="/" end>Главная</NavLink>
                <NavLink to="/apply">Новая заявка</NavLink>
              </>
            )}
          </nav>
          <div className="spacer" />
          <div className="who">
            <b>{user.full_name}</b>
            <span>{isBank ? 'Кредитный эксперт' : company?.name}</span>
          </div>
          <button className="btn btn-ghost btn-sm" onClick={() => { signOut(); nav('/login') }}>Выйти</button>
        </div>
      </header>
      <Outlet />
    </>
  )
}

function RoleHome() {
  const { user } = useSession()
  return user?.role === 'underwriter' ? <Navigate to="/bank" replace /> : <ClientHome />
}

export function App() {
  const { ready } = useSession()
  if (!ready) return <div className="page"><Spinner /></div>
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route path="/register" element={<RegisterPage />} />
      <Route element={<Shell />}>
        <Route index element={<RoleHome />} />
        <Route path="apply" element={<ApplyWizard />} />
        <Route path="applications/:id" element={<ApplicationPage />} />
        <Route path="bank" element={<BankQueue />} />
        <Route path="bank/applications/:id" element={<BankApplication />} />
      </Route>
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  )
}
