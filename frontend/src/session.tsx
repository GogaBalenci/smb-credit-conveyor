import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from 'react'
import { api, auth } from './api/client'
import type { Company, Meta, Session, User } from './api/types'

interface SessionState {
  user: User | null
  company: Company | null
  meta: Meta | null
  ready: boolean
  signIn: (s: Session) => void
  signOut: () => void
}

const Ctx = createContext<SessionState | null>(null)

export function SessionProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null)
  const [company, setCompany] = useState<Company | null>(null)
  const [meta, setMeta] = useState<Meta | null>(null)
  const [ready, setReady] = useState(false)

  const signOut = useCallback(() => {
    auth.set(null)
    setUser(null)
    setCompany(null)
  }, [])

  useEffect(() => {
    auth.onUnauthorized(signOut)
    const loadMe = auth.token
      ? api.me().then((r) => { setUser(r.user); setCompany(r.company) }).catch(() => auth.set(null))
      : Promise.resolve()
    Promise.all([api.meta().then(setMeta).catch(() => undefined), loadMe]).finally(() => setReady(true))
  }, [signOut])

  const signIn = useCallback((s: Session) => {
    auth.set(s.token)
    setUser(s.user)
    setCompany(s.company ?? null)
  }, [])

  return <Ctx.Provider value={{ user, company, meta, ready, signIn, signOut }}>{children}</Ctx.Provider>
}

export function useSession(): SessionState {
  const v = useContext(Ctx)
  if (!v) throw new Error('useSession outside provider')
  return v
}
