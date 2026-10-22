// Package recommend подбирает кредитные продукты под потребность клиента.
//
// Клиент без банковской экспертизы не обязан знать, чем овердрафт отличается от
// оборотного кредита (кейс, стр. 4–5). Поэтому путь начинается с вопроса
// «на что нужны деньги?», а система ранжирует продукты и объясняет выбор.
// Логика детерминированная и тестируемая; LLM (если включена) лишь пересказывает её.
package recommend

import (
	"fmt"
	"sort"

	"alfahack/backend/internal/domain"
	"alfahack/backend/internal/policy"
)

// Need — потребность клиента, собранная мастером.
type Need struct {
	Purpose       domain.Purpose `json:"purpose"`
	Amount        int64          `json:"amount"`
	TermMonths    int            `json:"term_months"`
	HasCollateral bool           `json:"has_collateral"`
	Urgent        bool           `json:"urgent"`
}

// Recommendation — продукт с оценкой соответствия и пояснениями.
type Recommendation struct {
	Product          domain.Product `json:"product"`
	FitScore         int            `json:"fit_score"`
	Best             bool           `json:"best"`
	Reasons          []string       `json:"reasons"`
	Warnings         []string       `json:"warnings"`
	SuggestedAmount  int64          `json:"suggested_amount"`
	SuggestedTerm    int            `json:"suggested_term"`
	EstimatedPayment int64          `json:"estimated_payment"` // по базовой ставке, ориентир
}

// Recommend возвращает подходящие продукты, лучший — первым.
func Recommend(need Need) []Recommendation {
	out := []Recommendation{}
	for _, p := range domain.Products {
		if need.Purpose != "" && !p.SupportsPurpose(need.Purpose) {
			continue
		}
		r := Recommendation{Product: p, FitScore: 50, Reasons: []string{}, Warnings: []string{}}

		if len(p.Purposes) <= 2 && need.Purpose != "" {
			r.FitScore += 15
			r.Reasons = append(r.Reasons, "Создан специально для этой цели")
		}

		amount := need.Amount
		switch {
		case amount == 0:
			amount = p.MinAmount
		case amount < p.MinAmount:
			r.FitScore -= 25
			r.Warnings = append(r.Warnings, "Минимальная сумма — "+domain.FormatRub(p.MinAmount))
			amount = p.MinAmount
		case amount > p.MaxAmount:
			r.FitScore -= 40
			r.Warnings = append(r.Warnings, "Максимальная сумма — "+domain.FormatRub(p.MaxAmount))
			amount = p.MaxAmount
		default:
			r.FitScore += 20
			r.Reasons = append(r.Reasons, "Подходит по сумме")
		}

		term := need.TermMonths
		switch {
		case term == 0:
			term = clamp(12, p.MinTermMonths, p.MaxTermMonths)
		case term < p.MinTermMonths || term > p.MaxTermMonths:
			r.FitScore -= 15
			r.Warnings = append(r.Warnings, fmt.Sprintf("Срок по продукту — от %d до %d мес.", p.MinTermMonths, p.MaxTermMonths))
			term = clamp(term, p.MinTermMonths, p.MaxTermMonths)
		default:
			r.FitScore += 10
		}

		if p.RequiresCollateral {
			if need.HasCollateral {
				r.FitScore += 10
				r.Reasons = append(r.Reasons, "Залог снижает ставку и позволяет взять больше")
			} else {
				r.FitScore -= 30
				r.Warnings = append(r.Warnings, "Нужен залог (обычно — приобретаемое имущество)")
			}
		} else {
			r.Reasons = append(r.Reasons, "Без залога")
		}

		if need.Urgent && p.DecisionTime == "несколько минут" {
			r.FitScore += 15
			r.Reasons = append(r.Reasons, "Решение за несколько минут")
		}

		// Чем ниже ставка, тем лучше (до +10).
		r.FitScore += int(25 - p.BaseRate)
		r.Reasons = append(r.Reasons, fmt.Sprintf("Ставка от %.1f%% годовых", p.BaseRate))

		r.SuggestedAmount = amount
		r.SuggestedTerm = term
		r.EstimatedPayment = policy.Annuity(amount, p.BaseRate, term)
		out = append(out, r)
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].FitScore > out[j].FitScore })
	if len(out) > 0 && len(out[0].Warnings) == 0 {
		out[0].Best = true
	}
	return out
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
