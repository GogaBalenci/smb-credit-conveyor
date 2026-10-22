// Package scoring — объяснимая балльная скоринговая модель (scorecard).
//
// Модель намеренно простая и прозрачная (кейс, стр. 14): каждому признаку
// соответствует вклад в балл, итог = база + сумма вкладов. Скоринг даёт только
// оценку риска; кредитное решение принимает пакет policy с учётом продукта,
// суммы, срока и правил банка.
package scoring

import (
	"fmt"
	"math"

	"alfahack/backend/internal/domain"
)

// ModelVersion фиксируется в каждом результате — для аудита и воспроизводимости.
const ModelVersion = "scorecard-v1.2"

const baseScore = 600

// CreditHistory — агрегат кредитной истории из БКИ.
type CreditHistory struct {
	Found           bool  // есть ли запись в БКИ
	ActiveLoans     int   // действующие кредиты
	ClosedLoans     int   // закрытые кредиты
	MonthlyPayments int64 // сумма ежемесячных платежей по действующим кредитам
	MaxOverdueDays  int   // максимальная просрочка за 24 мес.
	Inquiries30d    int   // запросы кредитной истории за 30 дней
}

// TaxProfile — сведения ФНС «Прозрачный бизнес».
type TaxProfile struct {
	TaxArrears           int64 // недоимка по налогам, ₽
	MassAddress          bool  // адрес массовой регистрации
	DisqualifiedDirector bool  // дисквалифицированный руководитель
	Bankruptcy           bool  // процедура банкротства
}

// Input — всё, что нужно модели. Собирается конвейером из заявки и внешних проверок.
type Input struct {
	RegistryActive   bool
	CompanyAgeMonths int
	OKVEDSection     string
	Financials       domain.Financials
	Amount           int64
	RequestedPayment int64 // платёж по запрашиваемому кредиту
	Credit           CreditHistory
	Tax              TaxProfile
}

// ExistingPayments — текущая долговая нагрузка: берём максимум из заявленного
// клиентом и найденного в БКИ (клиент мог что-то забыть указать).
func (in Input) ExistingPayments() int64 {
	if in.Credit.MonthlyPayments > in.Financials.MonthlyObligations {
		return in.Credit.MonthlyPayments
	}
	return in.Financials.MonthlyObligations
}

// DebtLoad — доля всех платежей (текущих + нового) в месячной выручке.
func (in Input) DebtLoad() float64 {
	monthly := float64(in.Financials.AnnualRevenue) / 12
	if monthly <= 0 {
		return math.Inf(1)
	}
	return float64(in.ExistingPayments()+in.RequestedPayment) / monthly
}

// Stop-коды — обстоятельства, при которых кредит не выдаётся независимо от балла.
const (
	StopRegistryInactive     = "STOP_REGISTRY_INACTIVE"
	StopBankruptcy           = "STOP_BANKRUPTCY"
	StopDisqualifiedDirector = "STOP_DISQUALIFIED_DIRECTOR"
	StopSevereOverdue        = "STOP_SEVERE_OVERDUE"
	StopTooYoung             = "STOP_COMPANY_TOO_YOUNG"
)

// Score рассчитывает балл, грейд и вклад каждого признака.
func Score(in Input) domain.ScoreResult {
	var f []domain.ScoreFactor
	add := func(code, title, value string, pts int) {
		f = append(f, domain.ScoreFactor{Code: code, Title: title, Value: value, Points: pts})
	}

	// Стоп-факторы.
	stops := []string{}
	if !in.RegistryActive {
		stops = append(stops, StopRegistryInactive)
	}
	if in.Tax.Bankruptcy {
		stops = append(stops, StopBankruptcy)
	}
	if in.Tax.DisqualifiedDirector {
		stops = append(stops, StopDisqualifiedDirector)
	}
	if in.Credit.MaxOverdueDays >= 90 {
		stops = append(stops, StopSevereOverdue)
	}
	if in.CompanyAgeMonths < 3 {
		stops = append(stops, StopTooYoung)
	}

	// 1. Возраст бизнеса.
	age := in.CompanyAgeMonths
	ageVal := fmt.Sprintf("%d мес.", age)
	switch {
	case age < 6:
		add("AGE", "Срок работы бизнеса", ageVal, -60)
	case age < 12:
		add("AGE", "Срок работы бизнеса", ageVal, -35)
	case age < 24:
		add("AGE", "Срок работы бизнеса", ageVal, -10)
	case age < 60:
		add("AGE", "Срок работы бизнеса", ageVal, 15)
	default:
		add("AGE", "Срок работы бизнеса", ageVal, 30)
	}

	// 2. Кредитная история.
	ch := in.Credit
	switch {
	case !ch.Found || ch.ActiveLoans+ch.ClosedLoans == 0:
		add("CREDIT_HISTORY", "Кредитная история", "нет истории", -10)
	case ch.MaxOverdueDays == 0:
		pts := 25
		if ch.ClosedLoans >= 2 {
			pts = 40
		}
		add("CREDIT_HISTORY", "Кредитная история", fmt.Sprintf("без просрочек, закрыто кредитов: %d", ch.ClosedLoans), pts)
	case ch.MaxOverdueDays < 30:
		add("CREDIT_HISTORY", "Кредитная история", fmt.Sprintf("просрочка до %d дн.", ch.MaxOverdueDays), -30)
	default:
		add("CREDIT_HISTORY", "Кредитная история", fmt.Sprintf("просрочка до %d дн.", ch.MaxOverdueDays), -90)
	}

	// 3. Кредитная активность (много запросов — признак «кредитного голода»).
	switch {
	case ch.Inquiries30d >= 5:
		add("INQUIRIES", "Запросы в БКИ за 30 дней", fmt.Sprint(ch.Inquiries30d), -25)
	case ch.Inquiries30d >= 3:
		add("INQUIRIES", "Запросы в БКИ за 30 дней", fmt.Sprint(ch.Inquiries30d), -10)
	default:
		add("INQUIRIES", "Запросы в БКИ за 30 дней", fmt.Sprint(ch.Inquiries30d), 0)
	}

	// 4. Долговая нагрузка с учётом нового кредита.
	dl := in.DebtLoad()
	dlVal := "нет выручки"
	if !math.IsInf(dl, 1) {
		dlVal = fmt.Sprintf("%.0f%% выручки", dl*100)
	}
	switch {
	case dl < 0.10:
		add("DEBT_LOAD", "Платежи по кредитам к выручке", dlVal, 40)
	case dl < 0.20:
		add("DEBT_LOAD", "Платежи по кредитам к выручке", dlVal, 15)
	case dl < 0.30:
		add("DEBT_LOAD", "Платежи по кредитам к выручке", dlVal, -10)
	case dl < 0.40:
		add("DEBT_LOAD", "Платежи по кредитам к выручке", dlVal, -45)
	default:
		add("DEBT_LOAD", "Платежи по кредитам к выручке", dlVal, -90)
	}

	// 5. Рентабельность.
	fin := in.Financials
	if fin.AnnualRevenue <= 0 {
		add("MARGIN", "Рентабельность", "нет выручки", -80)
	} else {
		m := float64(fin.AnnualNetProfit) / float64(fin.AnnualRevenue)
		val := fmt.Sprintf("%.1f%%", m*100)
		switch {
		case m < 0:
			add("MARGIN", "Рентабельность", val, -60)
		case m < 0.03:
			add("MARGIN", "Рентабельность", val, -15)
		case m < 0.10:
			add("MARGIN", "Рентабельность", val, 10)
		default:
			add("MARGIN", "Рентабельность", val, 25)
		}
	}

	// 6. Динамика выручки.
	if fin.PrevAnnualRevenue > 0 && fin.AnnualRevenue > 0 {
		g := float64(fin.AnnualRevenue)/float64(fin.PrevAnnualRevenue) - 1
		val := fmt.Sprintf("%+.0f%% к прошлому году", g*100)
		switch {
		case g >= 0.10:
			add("REVENUE_TREND", "Динамика выручки", val, 15)
		case g <= -0.25:
			add("REVENUE_TREND", "Динамика выручки", val, -35)
		case g <= -0.10:
			add("REVENUE_TREND", "Динамика выручки", val, -15)
		default:
			add("REVENUE_TREND", "Динамика выручки", val, 0)
		}
	} else {
		add("REVENUE_TREND", "Динамика выручки", "нет данных за прошлый год", 0)
	}

	// 7. Налоговый профиль.
	if in.Tax.TaxArrears > 0 {
		add("TAX_ARREARS", "Задолженность по налогам", domain.FormatRub(in.Tax.TaxArrears), -40)
	}
	if in.Tax.MassAddress {
		add("MASS_ADDRESS", "Адрес массовой регистрации", "да", -30)
	}

	// 8. Отраслевой риск.
	if pts, name, ok := industryAdjustment(in.OKVEDSection); ok {
		add("INDUSTRY", "Отраслевой риск", name, pts)
	}

	// 9. Обеспечение.
	if fin.CollateralValue > 0 && in.Amount > 0 {
		cov := float64(fin.CollateralValue) / float64(in.Amount)
		val := fmt.Sprintf("покрытие %.0f%%", cov*100)
		switch {
		case cov >= 1:
			add("COLLATERAL", "Залог", val, 25)
		case cov >= 0.5:
			add("COLLATERAL", "Залог", val, 10)
		default:
			add("COLLATERAL", "Залог", val, 0)
		}
	}

	score := baseScore
	for _, x := range f {
		score += x.Points
	}
	return domain.ScoreResult{
		Score:        score,
		Grade:        Grade(score),
		PD:           PD(score),
		Factors:      f,
		StopFactors:  stops,
		ModelVersion: ModelVersion,
	}
}

// Grade переводит балл в рейтинговую категорию.
func Grade(score int) string {
	switch {
	case score >= 700:
		return "A"
	case score >= 650:
		return "B"
	case score >= 600:
		return "C"
	case score >= 550:
		return "D"
	default:
		return "E"
	}
}

// PD — ориентировочная вероятность дефолта (логистическая калибровка балла).
// Калибровка условная: в реальном банке оценивается на исторических данных.
func PD(score int) float64 {
	pd := 1 / (1 + math.Exp(float64(score-500)/40))
	return math.Round(pd*10000) / 10000
}

func industryAdjustment(section string) (int, string, bool) {
	switch section {
	case "41", "42", "43":
		return -15, "строительство — повышенный", true
	case "55", "56":
		return -10, "общепит и гостиницы — повышенный", true
	case "01", "02", "03":
		return -5, "сельское хозяйство — умеренный", true
	case "62", "63":
		return 10, "IT — пониженный", true
	case "86":
		return 10, "медицина — пониженный", true
	case "":
		return 0, "", false
	default:
		return 0, "средний", true
	}
}

// StopFactorText — объяснение стоп-фактора для клиента.
func StopFactorText(code string) string {
	switch code {
	case StopRegistryInactive:
		return "По данным ЕГРЮЛ/ЕГРИП компания не действует (ликвидация или прекращение деятельности)."
	case StopBankruptcy:
		return "В отношении компании ведётся процедура банкротства."
	case StopDisqualifiedDirector:
		return "Руководитель компании дисквалифицирован по данным ФНС."
	case StopSevereOverdue:
		return "В кредитной истории есть просрочка более 90 дней."
	case StopTooYoung:
		return "Бизнес зарегистрирован менее 3 месяцев назад."
	default:
		return code
	}
}
