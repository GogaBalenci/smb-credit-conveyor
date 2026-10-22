import { describe, expect, it } from 'vitest'
import { annuity, debtLoad } from './finance'

describe('finance', () => {
  it('matches backend annuity (1 000 000 ₽, 12%, 12 мес. = 88 849 ₽)', () => {
    expect(annuity(1_000_000, 12, 12)).toBe(88_849)
    expect(annuity(1200, 0, 12)).toBe(100)
  })
  it('computes debt load', () => {
    expect(debtLoad(12_000_000, 100_000, 100_000)).toBeCloseTo(0.2)
    expect(debtLoad(0, 1, 1)).toBeNull()
  })
})
