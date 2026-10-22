import type { Status } from '../api/types'
import { STATIONS } from '../lib/format'

type StationState = 'moving' | 'wait' | 'good' | 'stopped'

/** Положение заявки на конвейере: номер станции и её состояние. */
export function position(status: Status): { index: number; state: StationState } {
  switch (status) {
    case 'DRAFT': return { index: 0, state: 'wait' }
    case 'CANCELLED': return { index: 0, state: 'stopped' }
    case 'SUBMITTED': case 'CHECKING': return { index: 1, state: 'moving' }
    case 'NEEDS_INFO': return { index: 1, state: 'wait' }
    case 'SCORING': return { index: 2, state: 'moving' }
    case 'MANUAL_REVIEW': return { index: 2, state: 'wait' }
    case 'APPROVED': return { index: 3, state: 'good' }
    case 'COUNTER_OFFER': return { index: 3, state: 'wait' }
    case 'REJECTED': case 'DECLINED': return { index: 3, state: 'stopped' }
    case 'SIGNED': return { index: 4, state: 'moving' }
    case 'ISSUED': return { index: 4, state: 'good' }
  }
}

/**
 * Конвейер — главный визуальный образ продукта: заявка едет по станциям
 * «Заявка → Проверки → Оценка → Решение → Деньги». Лента движется, пока
 * работает банк, и останавливается, когда нужен ответ клиента.
 */
export function Conveyor({ status, compact = false }: { status: Status; compact?: boolean }) {
  const { index, state } = position(status)
  const finished = status === 'ISSUED'
  const fill = finished ? 100 : (index / (STATIONS.length - 1)) * 100
  return (
    <div
      className={`conveyor ${compact ? 'conveyor-compact' : ''} ${state === 'moving' ? 'is-moving' : ''}`}
      role="img"
      aria-label={`Этап ${index + 1} из ${STATIONS.length}: ${STATIONS[index]}`}
    >
      <div className="conveyor-track" />
      <div className="conveyor-fill" style={{ width: `${0.8 * fill}%` }} />
      <ol className="stations" style={{ listStyle: 'none', margin: 0, padding: 0 }}>
        {STATIONS.map((label, i) => {
          const cls = i < index || (finished && i === index) ? 'done' : i === index ? `current ${state}` : ''
          return (
            <li key={label} className={`station ${cls}`}>
              <span className="station-dot" aria-hidden="true">{i < index || finished ? '✓' : i + 1}</span>
              <span className="station-label">{label}</span>
            </li>
          )
        })}
      </ol>
    </div>
  )
}
