package domain

import "time"

// ScoreFactor — вклад одного признака в скоринговый балл (объяснимость модели).
type ScoreFactor struct {
	Code   string `json:"code"`
	Title  string `json:"title"`
	Value  string `json:"value"`  // значение признака в читаемом виде
	Points int    `json:"points"` // вклад в балл (+/-)
}

// ScoreResult — результат скоринга: оценка риска, но ещё не решение.
type ScoreResult struct {
	Score        int           `json:"score"`
	Grade        string        `json:"grade"` // A..E
	PD           float64       `json:"pd"`    // ориентировочная вероятность дефолта
	Factors      []ScoreFactor `json:"factors"`
	StopFactors  []string      `json:"stop_factors"`
	ModelVersion string        `json:"model_version"`
}

// Outcome — итог кредитного рассмотрения. Не бинарный (кейс, стр. 15).
type Outcome string

const (
	OutcomeApproved     Outcome = "approved"
	OutcomeCounterOffer Outcome = "counter_offer"
	OutcomeManualReview Outcome = "manual_review"
	OutcomeNeedsInfo    Outcome = "needs_info"
	OutcomeRejected     Outcome = "rejected"
)

// Offer — условия, которые банк готов предложить.
type Offer struct {
	ProductCode    string  `json:"product_code"`
	Amount         int64   `json:"amount"`
	TermMonths     int     `json:"term_months"`
	Rate           float64 `json:"rate"`
	MonthlyPayment int64   `json:"monthly_payment"`
	TotalPayment   int64   `json:"total_payment"`
}

// Decision — кредитное решение.
type Decision struct {
	Outcome       Outcome   `json:"outcome"`
	ReasonCodes   []string  `json:"reason_codes"`
	Reasons       []string  `json:"reasons"`     // для клиента, человеческим языком
	Suggestions   []string  `json:"suggestions"` // что можно сделать дальше
	Offer         *Offer    `json:"offer,omitempty"`
	DecidedBy     string    `json:"decided_by"` // "system" или id андеррайтера
	Comment       string    `json:"comment,omitempty"`
	PolicyVersion string    `json:"policy_version"`
	DecidedAt     time.Time `json:"decided_at"`
}

// TargetStatus — в какой статус переводит заявку решение.
func (o Outcome) TargetStatus() Status {
	switch o {
	case OutcomeApproved:
		return StatusApproved
	case OutcomeCounterOffer:
		return StatusCounterOffer
	case OutcomeManualReview:
		return StatusManualReview
	case OutcomeNeedsInfo:
		return StatusNeedsInfo
	default:
		return StatusRejected
	}
}
