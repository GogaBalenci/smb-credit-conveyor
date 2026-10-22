// Package policy — кредитная политика: превращает оценку риска в решение.
//
// Скоринг и решение разделены намеренно (кейс, стр. 14): одинаковый балл может
// давать разные условия для разных продуктов. Политика учитывает лимиты продукта,
// допустимую долговую нагрузку, полномочия автоматического решения и подбирает
// контрпредложение, если запрос клиента не проходит.
package policy

import (
	"fmt"
	"math"
	"time"

	"alfahack/backend/internal/domain"
	"alfahack/backend/internal/scoring"
)

// Version фиксируется в каждом решении.
const Version = "credit-policy-2026.10"

// ratePremium — надбавка к базовой ставке продукта по грейду, п.п.
var ratePremium = map[string]float64{"A": 0, "B": 1.5, "C": 3, "D": 5}

// Rate — ставка для продукта и грейда.
func Rate(p domain.Product, grade string) float64 {
	return p.BaseRate + ratePremium[grade]
}

// Request — параметры, по которым принимается решение.
type Request struct {
	Product          domain.Product
	Amount           int64
	TermMonths       int
	Financials       domain.Financials
	ExistingPayments int64 // текущие платежи (max из заявленных и БКИ)
	Score            domain.ScoreResult
}

// Reason-коды решения.
const (
	ReasonStopFactor     = "STOP_FACTOR"
	ReasonHighRisk       = "HIGH_RISK"
	ReasonBorderlineRisk = "BORDERLINE_RISK"
	ReasonOverAutoLimit  = "OVER_AUTO_LIMIT"
	ReasonLowCollateral  = "LOW_COLLATERAL"
	ReasonNoCapacity     = "NO_REPAYMENT_CAPACITY"
	ReasonTermExtended   = "TERM_EXTENDED"
	ReasonAmountReduced  = "AMOUNT_REDUCED"
	ReasonWithinPolicy   = "WITHIN_POLICY"
)

// Decide принимает автоматическое решение.
func Decide(req Request, now time.Time) domain.Decision {
	d := domain.Decision{DecidedBy: "system", PolicyVersion: Version, DecidedAt: now,
		ReasonCodes: []string{}, Reasons: []string{}, Suggestions: []string{}}
	p := req.Product
	sc := req.Score

	// 1. Стоп-факторы — отказ без вариантов.
	if len(sc.StopFactors) > 0 {
		d.Outcome = domain.OutcomeRejected
		d.ReasonCodes = append(d.ReasonCodes, ReasonStopFactor)
		for _, s := range sc.StopFactors {
			d.ReasonCodes = append(d.ReasonCodes, s)
			d.Reasons = append(d.Reasons, scoring.StopFactorText(s))
		}
		d.Suggestions = []string{"Если данные в реестрах неактуальны — обратитесь в ФНС для их исправления и подайте заявку повторно."}
		return d
	}

	// 2. Высокий риск.
	if sc.Grade == "E" {
		d.Outcome = domain.OutcomeRejected
		d.ReasonCodes = []string{ReasonHighRisk}
		d.Reasons = append([]string{"Кредитный рейтинг компании ниже минимально допустимого."}, negativeDrivers(sc, 3)...)
		d.Suggestions = []string{
			"Погасите текущие просрочки и снизьте долговую нагрузку.",
			"Повторную заявку можно подать через 3 месяца.",
			"Посмотрите программы господдержки МСП в ленте новостей.",
		}
		return d
	}

	rate := Rate(p, sc.Grade)
	monthlyRevenue := req.Financials.AnnualRevenue / 12
	maxPayment := int64(float64(monthlyRevenue)*p.MaxDebtLoad) - req.ExistingPayments

	// 3. Подбор условий, которые компания способна обслуживать.
	offer, adjust := fitOffer(p, req.Amount, req.TermMonths, rate, maxPayment)
	if offer == nil {
		d.Outcome = domain.OutcomeRejected
		d.ReasonCodes = []string{ReasonNoCapacity}
		d.Reasons = []string{fmt.Sprintf(
			"Текущая выручка не позволяет обслуживать кредит даже на минимальную сумму продукта (%s): платежи по кредитам превысят %.0f%% выручки.",
			domain.FormatRub(p.MinAmount), p.MaxDebtLoad*100)}
		d.Suggestions = []string{
			"Уменьшите текущую долговую нагрузку или рассмотрите рефинансирование.",
			"Если выручка выросла — обновите финансовые показатели и подайте заявку снова.",
		}
		return d
	}
	d.Offer = offer

	// 4. Ручное рассмотрение: пограничный риск, превышение полномочий, слабый залог.
	var manual []string
	if sc.Grade == "D" {
		d.ReasonCodes = append(d.ReasonCodes, ReasonBorderlineRisk)
		manual = append(manual, "Пограничная оценка риска — нужно экспертное заключение.")
	}
	if offer.Amount > p.AutoDecisionLimit {
		d.ReasonCodes = append(d.ReasonCodes, ReasonOverAutoLimit)
		manual = append(manual, fmt.Sprintf("Сумма выше лимита автоматического решения (%s).", domain.FormatRub(p.AutoDecisionLimit)))
	}
	if p.RequiresCollateral && float64(req.Financials.CollateralValue) < 0.7*float64(offer.Amount) {
		d.ReasonCodes = append(d.ReasonCodes, ReasonLowCollateral)
		manual = append(manual, "Стоимость залога ниже 70% суммы кредита.")
	}
	if len(manual) > 0 {
		d.Outcome = domain.OutcomeManualReview
		d.Reasons = manual
		d.Suggestions = []string{"Решение примет кредитный эксперт. Предварительные условия рассчитаны автоматически."}
		return d
	}

	// 5. Одобрение или контрпредложение.
	switch adjust {
	case "":
		d.Outcome = domain.OutcomeApproved
		d.ReasonCodes = []string{ReasonWithinPolicy}
		d.Reasons = []string{"Заявка соответствует кредитной политике банка."}
	case ReasonTermExtended:
		d.Outcome = domain.OutcomeCounterOffer
		d.ReasonCodes = []string{ReasonTermExtended}
		d.Reasons = []string{fmt.Sprintf(
			"На сроке %d мес. платёж был бы слишком высоким для вашей выручки. Предлагаем ту же сумму на %d мес. — платёж %s в месяц.",
			req.TermMonths, offer.TermMonths, domain.FormatRub(offer.MonthlyPayment))}
	case ReasonAmountReduced:
		d.Outcome = domain.OutcomeCounterOffer
		d.ReasonCodes = []string{ReasonAmountReduced}
		d.Reasons = []string{fmt.Sprintf(
			"Запрошенная сумма превышает возможности обслуживания долга. Можем одобрить %s на %d мес.",
			domain.FormatRub(offer.Amount), offer.TermMonths)}
		d.Suggestions = []string{"Если нужна полная сумма — добавьте залог и подайте заявку на инвестиционный кредит."}
	}
	return d
}

// fitOffer ищет условия в рамках продукта: сначала запрошенные, затем с
// удлинённым сроком (сумма важнее клиенту), затем с уменьшенной суммой.
func fitOffer(p domain.Product, amount int64, term int, rate float64, maxPayment int64) (*domain.Offer, string) {
	if maxPayment <= 0 {
		return nil, ""
	}
	if Annuity(amount, rate, term) <= maxPayment {
		return makeOffer(p, amount, term, rate), ""
	}
	for t := term + 1; t <= p.MaxTermMonths; t++ {
		if Annuity(amount, rate, t) <= maxPayment {
			return makeOffer(p, amount, t, rate), ReasonTermExtended
		}
	}
	t := p.MaxTermMonths
	maxAmount := MaxPrincipal(maxPayment, rate, t)
	maxAmount = maxAmount / 10_000 * 10_000 // округляем вниз до 10 тыс.
	if maxAmount > amount {
		maxAmount = amount
	}
	if maxAmount < p.MinAmount {
		return nil, ""
	}
	return makeOffer(p, maxAmount, t, rate), ReasonAmountReduced
}

func makeOffer(p domain.Product, amount int64, term int, rate float64) *domain.Offer {
	pay := Annuity(amount, rate, term)
	return &domain.Offer{
		ProductCode: p.Code, Amount: amount, TermMonths: term, Rate: rate,
		MonthlyPayment: pay, TotalPayment: pay * int64(term),
	}
}

// MakeOffer — публичная обёртка для ручного решения андеррайтера.
func MakeOffer(p domain.Product, amount int64, term int, rate float64) *domain.Offer {
	return makeOffer(p, amount, term, rate)
}

// Annuity — аннуитетный платёж, ₽ (округление вверх до рубля).
func Annuity(principal int64, annualRate float64, months int) int64 {
	if months <= 0 {
		return principal
	}
	r := annualRate / 100 / 12
	if r == 0 {
		return int64(math.Ceil(float64(principal) / float64(months)))
	}
	k := r / (1 - math.Pow(1+r, -float64(months)))
	return int64(math.Ceil(float64(principal) * k))
}

// MaxPrincipal — максимальная сумма кредита при заданном платеже.
func MaxPrincipal(payment int64, annualRate float64, months int) int64 {
	r := annualRate / 100 / 12
	if r == 0 {
		return payment * int64(months)
	}
	return int64(float64(payment) * (1 - math.Pow(1+r, -float64(months))) / r)
}

// ScheduleItem — строка графика платежей.
type ScheduleItem struct {
	N         int   `json:"n"`
	Payment   int64 `json:"payment"`
	Principal int64 `json:"principal"`
	Interest  int64 `json:"interest"`
	Balance   int64 `json:"balance"`
}

// Schedule строит аннуитетный график платежей по офферу.
func Schedule(o domain.Offer) []ScheduleItem {
	r := o.Rate / 100 / 12
	balance := float64(o.Amount)
	out := make([]ScheduleItem, 0, o.TermMonths)
	for i := 1; i <= o.TermMonths; i++ {
		interest := math.Round(balance * r)
		pay := float64(o.MonthlyPayment)
		principal := pay - interest
		if i == o.TermMonths || principal > balance {
			principal = balance
			pay = principal + interest
		}
		balance -= principal
		out = append(out, ScheduleItem{N: i, Payment: int64(pay), Principal: int64(principal), Interest: int64(interest), Balance: int64(math.Max(balance, 0))})
	}
	return out
}

// negativeDrivers — самые негативные факторы скоринга в клиентских формулировках.
func negativeDrivers(sc domain.ScoreResult, n int) []string {
	var out []string
	for i := 0; i < n; i++ {
		worst := -1
		for j, f := range sc.Factors {
			if f.Points >= 0 || contains(out, factorText(f)) {
				continue
			}
			if worst == -1 || f.Points < sc.Factors[worst].Points {
				worst = j
			}
		}
		if worst == -1 {
			break
		}
		out = append(out, factorText(sc.Factors[worst]))
	}
	return out
}

func factorText(f domain.ScoreFactor) string { return f.Title + ": " + f.Value }

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
