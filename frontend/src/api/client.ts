import type {
  Application, ApplicationDetails, ApplicationInput, Company, DocKind, Document, Feed, Meta,
  Prefill, Recommendation, Session, Stats, Status, User,
} from './types'

/** Ошибка API в едином формате бэкенда. */
export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public fields: Record<string, string> = {},
    public requestId?: string,
  ) {
    super(message)
  }
}

const TOKEN_KEY = 'cc.token'

function readToken(): string | null {
  try { return localStorage.getItem(TOKEN_KEY) } catch { return null }
}

let token: string | null = readToken()
let onUnauthorized: () => void = () => {}

export const auth = {
  get token() { return token },
  set(t: string | null) {
    token = t
    try { t ? localStorage.setItem(TOKEN_KEY, t) : localStorage.removeItem(TOKEN_KEY) } catch { /* приватный режим */ }
  },
  onUnauthorized(fn: () => void) { onUnauthorized = fn },
}

function idempotencyKey(): string {
  return crypto.randomUUID ? crypto.randomUUID() : `${Date.now()}-${Math.random()}`
}

async function request<T>(method: string, path: string, body?: unknown, opts: { idempotent?: boolean; headers?: Record<string, string> } = {}): Promise<T> {
  const headers: Record<string, string> = { ...opts.headers }
  let payload: BodyInit | undefined
  if (body instanceof FormData) payload = body
  else if (body !== undefined) {
    headers['Content-Type'] = 'application/json'
    payload = JSON.stringify(body)
  }
  if (token) headers.Authorization = `Bearer ${token}`
  if (opts.idempotent) headers['Idempotency-Key'] = idempotencyKey()

  let res: Response
  try {
    res = await fetch(`/api/v1${path}`, { method, headers, body: payload })
  } catch {
    throw new ApiError(0, 'network', 'Нет связи с сервером. Проверьте подключение и попробуйте ещё раз.')
  }
  const text = await res.text()
  const data = text ? JSON.parse(text) : undefined
  if (!res.ok) {
    const e = data?.error ?? {}
    if (res.status === 401 && token) onUnauthorized()
    throw new ApiError(res.status, e.code ?? 'error', e.message ?? `Ошибка ${res.status}`, e.fields ?? {}, e.request_id)
  }
  return data as T
}

export const api = {
  meta: () => request<Meta>('GET', '/meta'),
  login: (email: string, password: string) => request<Session>('POST', '/auth/login', { email, password }),
  register: (input: { email: string; password: string; full_name: string; inn: string }) => request<Session>('POST', '/auth/register', input),
  me: () => request<{ user: User; company: Company | null }>('GET', '/auth/me'),
  lookupCompany: (inn: string) => request<Company>('GET', `/companies/lookup?inn=${encodeURIComponent(inn)}`),

  recommend: (need: { purpose: string; amount: number; term_months: number; has_collateral: boolean; urgent: boolean }) =>
    request<{ items: Recommendation[] }>('POST', '/recommendations', need),
  prefill: () => request<Prefill>('GET', '/prefill'),
  feed: () => request<Feed>('GET', '/feed'),

  list: (params: { status?: Status[]; q?: string; assignee?: 'me' } = {}) => {
    const qs = new URLSearchParams()
    if (params.status?.length) qs.set('status', params.status.join(','))
    if (params.q) qs.set('q', params.q)
    if (params.assignee) qs.set('assignee', params.assignee)
    return request<{ items: Application[]; total: number }>('GET', `/applications?${qs}`)
  },
  create: (input: ApplicationInput) => request<Application>('POST', '/applications', input, { idempotent: true }),
  get: (id: string) => request<ApplicationDetails>('GET', `/applications/${id}`),
  update: (id: string, version: number, input: ApplicationInput) =>
    request<Application>('PUT', `/applications/${id}`, input, { headers: { 'If-Match': `"${version}"` } }),
  submit: (id: string) => request<Application>('POST', `/applications/${id}/submit`, undefined, { idempotent: true }),
  cancel: (id: string) => request<Application>('POST', `/applications/${id}/cancel`),
  upload: (id: string, kind: DocKind, file: File) => {
    const fd = new FormData()
    fd.set('kind', kind)
    fd.set('file', file)
    return request<Document>('POST', `/applications/${id}/documents`, fd)
  },
  documentUrl: (appId: string, docId: string) => `/api/v1/applications/${appId}/documents/${docId}`,
  signCode: (id: string) => request<{ expires_in_sec: number; demo_code?: string; hint: string }>('POST', `/applications/${id}/sign-code`),
  sign: (id: string, code: string) => request<Application>('POST', `/applications/${id}/sign`, { code }, { idempotent: true }),
  decline: (id: string, reason: string) => request<Application>('POST', `/applications/${id}/decline`, { reason }),

  take: (id: string) => request<Application>('POST', `/applications/${id}/take`),
  decide: (id: string, d: { action: 'approve' | 'reject' | 'request_info'; amount?: number; term_months?: number; rate?: number; comment: string; documents?: DocKind[] }) =>
    request<Application>('POST', `/applications/${id}/decision`, d, { idempotent: true }),
  stats: () => request<Stats>('GET', '/stats'),
  faults: () => request<{ failure_rate: number; latency_ms: number }>('GET', '/admin/faults'),
  setFaults: (f: { failure_rate?: number; latency_ms?: number }) => request<{ failure_rate: number; latency_ms: number }>('POST', '/admin/faults', f),
}

/** Скачивание документа с авторизацией (обычная ссылка не передаст токен). */
export async function downloadDocument(appId: string, doc: Document) {
  const res = await fetch(api.documentUrl(appId, doc.id), { headers: token ? { Authorization: `Bearer ${token}` } : {} })
  if (!res.ok) throw new ApiError(res.status, 'download', 'Не удалось скачать документ')
  const url = URL.createObjectURL(await res.blob())
  const a = document.createElement('a')
  a.href = url
  a.download = doc.filename
  a.click()
  URL.revokeObjectURL(url)
}
