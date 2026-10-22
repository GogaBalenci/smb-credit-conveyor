import type { Status } from '../api/types'

const rub = new Intl.NumberFormat('ru-RU', { maximumFractionDigits: 0 })

/** 1 500 000 ₽ */
export function money(v: number | undefined | null): string {
  if (v === undefined || v === null || Number.isNaN(v)) return '—'
  return `${rub.format(v)} ₽`
}

/** Короткая сумма для заголовков: 1,5 млн ₽ */
export function moneyShort(v: number): string {
  if (Math.abs(v) >= 1_000_000) return `${trim(v / 1_000_000)} млн ₽`
  if (Math.abs(v) >= 1_000) return `${trim(v / 1_000)} тыс. ₽`
  return money(v)
}

function trim(x: number): string {
  return new Intl.NumberFormat('ru-RU', { maximumFractionDigits: 1 }).format(x)
}

/** Склонение: plural(5, ['месяц','месяца','месяцев']) */
export function plural(n: number, forms: [string, string, string]): string {
  const m10 = n % 10, m100 = n % 100
  if (m10 === 1 && m100 !== 11) return forms[0]
  if (m10 >= 2 && m10 <= 4 && (m100 < 12 || m100 > 14)) return forms[1]
  return forms[2]
}

export function months(n: number): string {
  return `${n} ${plural(n, ['месяц', 'месяца', 'месяцев'])}`
}

export function dateTime(iso: string): string {
  return new Date(iso).toLocaleString('ru-RU', { day: 'numeric', month: 'long', hour: '2-digit', minute: '2-digit' })
}

export function timeOnly(iso: string): string {
  return new Date(iso).toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

export function date(iso: string): string {
  return new Date(iso).toLocaleDateString('ru-RU', { day: 'numeric', month: 'long', year: 'numeric' })
}

/** Разбор суммы из поля ввода: «1 500 000» → 1500000 */
export function parseMoney(s: string): number {
  const n = Number(s.replace(/[^\d-]/g, ''))
  return Number.isFinite(n) ? n : 0
}

/** Станции конвейера, которые видит клиент. */
export const STATIONS = ['Заявка', 'Проверки', 'Оценка', 'Решение', 'Деньги'] as const

/** Тон статуса для цветового кодирования. */
export function tone(s: Status): 'neutral' | 'progress' | 'attention' | 'good' | 'bad' {
  switch (s) {
    case 'APPROVED': case 'SIGNED': case 'ISSUED': return 'good'
    case 'COUNTER_OFFER': case 'NEEDS_INFO': case 'DRAFT': return 'attention'
    case 'REJECTED': return 'bad'
    case 'DECLINED': case 'CANCELLED': return 'neutral'
    default: return 'progress'
  }
}

/** Статусы, в которых конвейер работает и стоит обновлять карточку. */
export const LIVE_STATUSES: Status[] = ['SUBMITTED', 'CHECKING', 'SCORING', 'SIGNED']

export const SOURCE_TITLES: Record<string, string> = {
  REGISTRY: 'ЕГРЮЛ / ЕГРИП', FNS: 'ФНС «Прозрачный бизнес»', BKI: 'Бюро кредитных историй',
}
