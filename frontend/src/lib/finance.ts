/** Аннуитетный платёж — та же формула, что в backend/internal/policy (для предпросмотра в мастере). */
export function annuity(principal: number, annualRate: number, months: number): number {
  if (months <= 0) return principal
  const r = annualRate / 100 / 12
  if (r === 0) return Math.ceil(principal / months)
  return Math.ceil(principal * (r / (1 - Math.pow(1 + r, -months))))
}

/** Доля платежей по кредитам в месячной выручке. */
export function debtLoad(annualRevenue: number, existingMonthly: number, newPayment: number): number | null {
  if (annualRevenue <= 0) return null
  return (existingMonthly + newPayment) / (annualRevenue / 12)
}
