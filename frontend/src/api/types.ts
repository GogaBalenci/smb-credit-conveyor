// Типы повторяют контракт API (backend/api/openapi.yaml).

export type Status =
  | 'DRAFT' | 'SUBMITTED' | 'CHECKING' | 'NEEDS_INFO' | 'SCORING' | 'MANUAL_REVIEW'
  | 'APPROVED' | 'COUNTER_OFFER' | 'REJECTED' | 'SIGNED' | 'ISSUED' | 'DECLINED' | 'CANCELLED'

export type Role = 'client' | 'underwriter'
export type Purpose = 'working_capital' | 'cash_gap' | 'equipment' | 'expansion' | 'tender' | 'refinance'
export type DocKind = 'financial_statements' | 'bank_statement' | 'collateral' | 'contract' | 'other'

export interface User { id: string; email: string; full_name: string; role: Role; company_id?: string }

export interface Company {
  id: string; inn: string; ogrn: string; name: string; kind: 'LEGAL' | 'INDIVIDUAL'
  okved: string; okved_name: string; region: string; address: string; director: string
  registered_at: string; registry_status: string
}

export interface Session { token: string; expires_at: string; user: User; company?: Company }

export interface Product {
  code: string; name: string; description: string; purposes: Purpose[]
  min_amount: number; max_amount: number; min_term_months: number; max_term_months: number
  base_rate: number; auto_decision_limit: number; requires_collateral: boolean
  max_debt_load: number; decision_time: string
}

export interface StatusInfo {
  status: Status; title: string; description: string; next_step: string; client_action: boolean; stage: number
}

export interface Meta {
  products: Product[]
  purposes: { code: Purpose; title: string; hint: string }[]
  statuses: StatusInfo[]
  transitions: { from: Status; to: Status; actors: string[] }[]
  doc_kinds: { code: DocKind; title: string }[]
  demo_accounts?: { email: string; full_name: string; inn?: string; role: Role; scenario: string }[]
  demo_password?: string
}

export interface Financials {
  annual_revenue: number; prev_annual_revenue: number; annual_net_profit: number
  monthly_obligations: number; collateral_value: number; employees: number
}

export interface DocRequest { kind: DocKind; reason: string; provided: boolean }

export interface ScoreFactor { code: string; title: string; value: string; points: number }
export interface ScoreResult {
  score: number; grade: string; pd: number; factors: ScoreFactor[]; stop_factors: string[]; model_version: string
}

export interface Offer {
  product_code: string; amount: number; term_months: number; rate: number; monthly_payment: number; total_payment: number
}

export interface Decision {
  outcome: 'approved' | 'counter_offer' | 'manual_review' | 'needs_info' | 'rejected'
  reason_codes: string[]; reasons: string[]; suggestions: string[]; offer?: Offer
  decided_by: string; comment?: string; policy_version: string; decided_at: string
}

export interface Application {
  id: string; number: string; company_id: string; company_name?: string; applicant_id: string
  product_code: string; purpose: Purpose; purpose_comment: string; amount: number; term_months: number
  financials: Financials; status: Status; required_docs: DocRequest[]; score?: ScoreResult; decision?: Decision
  assignee_id?: string; version: number; created_at: string; updated_at: string; submitted_at?: string
}

export interface ApplicationInput {
  product_code: string; purpose: Purpose | ''; purpose_comment: string; amount: number; term_months: number; financials: Financials
}

export interface AppEvent {
  id: number; application_id: string; from?: Status; to: Status; actor_kind: 'client' | 'system' | 'underwriter'
  actor_id?: string; comment?: string; created_at: string
}

export interface CheckResult {
  source: 'REGISTRY' | 'FNS' | 'BKI'; status: 'ok' | 'warning' | 'fail'; summary: string; payload?: unknown; checked_at: string
}

export interface Document {
  id: string; kind: DocKind; filename: string; content_type: string; size_bytes: number; sha256: string; uploaded_at: string
}

export interface ScheduleItem { n: number; payment: number; principal: number; interest: number; balance: number }

export type Action = 'edit' | 'upload_document' | 'submit' | 'cancel' | 'sign' | 'decline' | 'take' | 'approve' | 'reject' | 'request_info'

/** Задержка конвейера из-за сбоя внешнего источника. */
export interface PipelineNotice {
  state: 'retrying' | 'failed'; message: string; attempts: number; max_attempts: number
  next_attempt_at?: string; technical_error?: string
}

export interface ApplicationDetails {
  application: Application; status_info: StatusInfo; product: Product; company: Company
  events: AppEvent[]; checks: CheckResult[]; documents: Document[]; schedule?: ScheduleItem[]; actions: Action[]
  pipeline?: PipelineNotice
}

export interface Recommendation {
  product: Product; fit_score: number; best: boolean; reasons: string[]; warnings: string[]
  suggested_amount: number; suggested_term: number; estimated_payment: number
}

export interface Prefill { financials: Financials; sources: string[]; warnings: string[] }

export interface FeedItem {
  id: string; type: 'news' | 'event'; title: string; body: string; category: string
  reasons: string[]; relevance: number; action?: string; published_at: string
}

export interface Feed {
  profile: { industry: string; region: string; kind: string; age_months: number; stage: string; products: string[] }
  items: FeedItem[]
}

export interface Stats {
  by_status: Partial<Record<Status, number>>; total: number
  avg_auto_decision_sec: number; avg_manual_decision_sec: number
  queue: { pending: number; failed: number; retrying: number }; automation_rate: number
}
