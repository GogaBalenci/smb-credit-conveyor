import { describe, expect, it } from 'vitest'
import { money, moneyShort, months, parseMoney, plural, tone } from './format'

describe('format', () => {
  it('formats rubles with spaces', () => {
    expect(money(1500000).replace(/\s/g, ' ')).toBe('1 500 000 ₽')
    expect(money(undefined)).toBe('—')
  })
  it('shortens big amounts', () => {
    expect(moneyShort(1500000)).toBe('1,5 млн ₽')
    expect(moneyShort(250000)).toBe('250 тыс. ₽')
  })
  it('declines russian plurals', () => {
    expect(months(1)).toBe('1 месяц')
    expect(months(3)).toBe('3 месяца')
    expect(months(11)).toBe('11 месяцев')
    expect(months(24)).toBe('24 месяца')
    expect(plural(112, ['a', 'b', 'c'])).toBe('c')
  })
  it('parses user-typed money', () => {
    expect(parseMoney('1 500 000 ₽')).toBe(1500000)
    expect(parseMoney('')).toBe(0)
  })
  it('maps statuses to tones', () => {
    expect(tone('APPROVED')).toBe('good')
    expect(tone('REJECTED')).toBe('bad')
    expect(tone('NEEDS_INFO')).toBe('attention')
    expect(tone('CHECKING')).toBe('progress')
  })
})
