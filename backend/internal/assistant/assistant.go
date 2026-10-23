// Package assistant — сводка досье заявки для кредитного эксперта.
//
// Задача: сократить время ручного рассмотрения. Эксперт получает связный пересказ
// проверок, скоринга и причин передачи на экспертизу вместо разбора пяти блоков.
//
// Принципы (docs/decisions.md, п. 4):
//   - модель только пересказывает факты заявки и не принимает решение;
//   - используется локальная модель с открытыми весами (Ollama), данные не уходят наружу;
//   - ответ модели проверяется: суммы, которых нет в фактах, считаются выдумкой,
//     и вместо ответа модели показывается детерминированная шаблонная сводка;
//   - без модели работает шаблонная сводка, основной сценарий от LLM не зависит.
package assistant

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"alfahack/backend/internal/domain"
)

// Facts — данные заявки, на которых строится сводка. Это единственное, что видит модель.
type Facts struct {
	Number         string
	Company        string
	CompanyKind    domain.CompanyKind
	INN            string
	OKVED          string
	OKVEDName      string
	Region         string
	RegisteredAt   time.Time
	AgeMonths      int
	Product        string
	Purpose        string
	PurposeComment string
	Amount         int64
	TermMonths     int
	Financials     domain.Financials
	Status         string
	Checks         []domain.CheckResult
	Score          *domain.ScoreResult
	Decision       *domain.Decision
	Documents      []string
	MissingDocs    []string
}

// Summary — сводка для эксперта.
type Summary struct {
	Text   string `json:"text"`
	Source string `json:"source"` // llm | template
	Model  string `json:"model,omitempty"`
	// ModelSections — какие секции написала модель (остальные составлены по данным заявки).
	ModelSections []string  `json:"model_sections,omitempty"`
	Warning       string    `json:"warning,omitempty"`
	GeneratedAt   time.Time `json:"generated_at"`
	DurationMS    int64     `json:"duration_ms"`
}

// Summarizer строит сводку по фактам.
type Summarizer interface {
	Summarize(ctx context.Context, f Facts) (Summary, error)
}

// Text — факты в виде текста: и вход модели, и эталон для проверки её ответа.
func (f Facts) Text() string {
	var b strings.Builder
	line := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	kind := "юридическое лицо"
	if f.CompanyKind == domain.CompanyIndividual {
		kind = "индивидуальный предприниматель"
	}
	line("Заявка: %s, статус: %s", f.Number, f.Status)
	line("Компания: %s (%s), ИНН %s", f.Company, kind, f.INN)
	line("Отрасль: ОКВЭД %s %s", f.OKVED, f.OKVEDName)
	line("Регион: %s", f.Region)
	if !f.RegisteredAt.IsZero() {
		line("Дата регистрации: %s, срок работы: %d мес.", f.RegisteredAt.Format("02.01.2006"), f.AgeMonths)
	}
	line("Продукт: %s", f.Product)
	line("Запрошено: %s на %d мес.", domain.FormatRub(f.Amount), f.TermMonths)
	line("Цель: %s", f.Purpose)
	if f.PurposeComment != "" {
		line("Комментарий клиента о цели: %s", f.PurposeComment)
	}
	fin := f.Financials
	line("Выручка за последний год: %s", domain.FormatRub(fin.AnnualRevenue))
	if fin.PrevAnnualRevenue > 0 {
		line("Выручка за предыдущий год: %s", domain.FormatRub(fin.PrevAnnualRevenue))
	}
	line("Чистая прибыль за год: %s", domain.FormatRub(fin.AnnualNetProfit))
	line("Платежи по текущим кредитам (заявлено клиентом): %s в месяц", domain.FormatRub(fin.MonthlyObligations))
	if fin.CollateralValue > 0 {
		line("Стоимость залога: %s", domain.FormatRub(fin.CollateralValue))
	}
	for _, c := range f.Checks {
		line("Проверка %s: %s — %s", sourceTitle(c.Source), checkStatusTitle(c.Status), c.Summary)
	}
	if f.Score != nil {
		line("Скоринг: %d баллов (база 600), рейтинг %s, вероятность дефолта %.1f%%", f.Score.Score, f.Score.Grade, f.Score.PD*100)
		for _, x := range f.Score.Factors {
			line("Фактор скоринга: %s = %s, вклад %+d", x.Title, x.Value, x.Points)
		}
		for _, s := range f.Score.StopFactors {
			line("Стоп-фактор: %s", s)
		}
	}
	if d := f.Decision; d != nil {
		for _, r := range d.Reasons {
			line("Основание решения системы: %s", r)
		}
		if o := d.Offer; o != nil {
			line("Предварительные условия системы: %s на %d мес. под %.1f%%, платёж %s в месяц",
				domain.FormatRub(o.Amount), o.TermMonths, o.Rate, domain.FormatRub(o.MonthlyPayment))
		}
	}
	for _, d := range f.Documents {
		line("Загружен документ: %s", d)
	}
	for _, d := range f.MissingDocs {
		line("Не предоставлен документ: %s", d)
	}
	return b.String()
}

// Template — детерминированная сводка без модели. Используется по умолчанию и как запасной вариант.
type Template struct{ Now func() time.Time }

// Summarize строит сводку по правилам.
func (t Template) Summarize(_ context.Context, f Facts) (Summary, error) {
	now := time.Now
	if t.Now != nil {
		now = t.Now
	}
	start := now()
	var lines []string
	add := func(label, text string) {
		if strings.TrimSpace(text) != "" {
			lines = append(lines, label+": "+text)
		}
	}

	add("Профиль", fmt.Sprintf("%s, %s, %s; работает %d мес.", f.Company, lowerFirst(f.OKVEDName), f.Region, f.AgeMonths))
	req := fmt.Sprintf("%s — %s на %d мес.; цель — %s", f.Product, domain.FormatRub(f.Amount), f.TermMonths, lowerFirst(f.Purpose))
	if f.PurposeComment != "" {
		req += fmt.Sprintf(" («%s»)", f.PurposeComment)
	}
	add("Запрос", req+".")

	fin := f.Financials
	finText := "выручка " + domain.FormatRub(fin.AnnualRevenue)
	if fin.PrevAnnualRevenue > 0 && fin.AnnualRevenue > 0 {
		g := (float64(fin.AnnualRevenue)/float64(fin.PrevAnnualRevenue) - 1) * 100
		finText += fmt.Sprintf(" (%+.0f%% к прошлому году)", g)
	}
	finText += ", чистая прибыль " + domain.FormatRub(fin.AnnualNetProfit)
	if fin.AnnualRevenue > 0 {
		finText += fmt.Sprintf(" (рентабельность %.1f%%)", float64(fin.AnnualNetProfit)/float64(fin.AnnualRevenue)*100)
	}
	finText += ", текущие платежи по кредитам " + domain.FormatRub(fin.MonthlyObligations) + " в месяц"
	add("Финансы", finText+".")

	if f.Score != nil {
		add("Оценка", fmt.Sprintf("%d баллов, рейтинг %s, вероятность дефолта %.1f%%.", f.Score.Score, f.Score.Grade, f.Score.PD*100))
		pos, neg := splitFactors(f.Score.Factors)
		add("Сильные стороны", joinFactors(pos, 3))
		add("Риски", joinFactors(neg, 3))
	}

	var checks []string
	for _, c := range f.Checks {
		if c.Status != domain.CheckOK {
			checks = append(checks, sourceTitle(c.Source)+" — "+lowerFirst(c.Summary))
		}
	}
	if len(checks) == 0 && len(f.Checks) > 0 {
		add("Проверки", "ЕГРЮЛ/ЕГРИП, ФНС и БКИ без замечаний.")
	} else {
		add("Проверки", strings.Join(checks, "; ")+".")
	}

	if d := f.Decision; d != nil && len(d.Reasons) > 0 {
		add("Почему у эксперта", strings.Join(d.Reasons, " "))
	}
	if len(f.MissingDocs) > 0 {
		add("Не хватает", strings.Join(f.MissingDocs, "; ")+".")
	}
	add("Что проверить", strings.Join(whatToCheck(f), "; ")+".")

	return Summary{
		Text: strings.Join(lines, "\n"), Source: "template", GeneratedAt: now(),
		DurationMS: now().Sub(start).Milliseconds(),
	}, nil
}

// whatToCheck — вопросы эксперту, выведенные из факторов риска.
func whatToCheck(f Facts) []string {
	var out []string
	codes := map[string]domain.ScoreFactor{}
	if f.Score != nil {
		for _, x := range f.Score.Factors {
			codes[x.Code] = x
		}
	}
	if x, ok := codes["CREDIT_HISTORY"]; ok && x.Points <= -10 {
		if strings.Contains(x.Value, "нет истории") {
			out = append(out, "регулярность поступлений по выписке — кредитной истории нет")
		} else {
			out = append(out, "причины просрочек и их текущий статус")
		}
	}
	if x, ok := codes["AGE"]; ok && x.Points < 0 {
		out = append(out, "устойчивость выручки помесячно: бизнес молодой")
	}
	if x, ok := codes["DEBT_LOAD"]; ok && x.Points < 0 {
		out = append(out, "обслуживание долга при снижении выручки на 20%")
	}
	if x, ok := codes["REVENUE_TREND"]; ok && x.Points < 0 {
		out = append(out, "причины падения выручки")
	}
	if _, ok := codes["TAX_ARREARS"]; ok {
		out = append(out, "погашение налоговой недоимки")
	}
	if f.Decision != nil {
		for _, c := range f.Decision.ReasonCodes {
			switch c {
			case "OVER_AUTO_LIMIT":
				out = append(out, "соответствие суммы обороту и цели кредита")
			case "LOW_COLLATERAL":
				out = append(out, "оценку и ликвидность залога")
			}
		}
	}
	if len(out) == 0 {
		out = append(out, "соответствие суммы кредита заявленной цели")
	}
	return out
}

func splitFactors(fs []domain.ScoreFactor) (pos, neg []domain.ScoreFactor) {
	for _, x := range fs {
		switch {
		case x.Points > 0:
			pos = append(pos, x)
		case x.Points < 0:
			neg = append(neg, x)
		}
	}
	sort.SliceStable(pos, func(i, j int) bool { return pos[i].Points > pos[j].Points })
	sort.SliceStable(neg, func(i, j int) bool { return neg[i].Points < neg[j].Points })
	return pos, neg
}

func joinFactors(fs []domain.ScoreFactor, n int) string {
	if len(fs) == 0 {
		return ""
	}
	var parts []string
	for i, x := range fs {
		if i == n {
			break
		}
		parts = append(parts, fmt.Sprintf("%s — %s (%+d)", lowerFirst(x.Title), x.Value, x.Points))
	}
	return strings.Join(parts, "; ") + "."
}

func sourceTitle(s domain.CheckSource) string {
	switch s {
	case domain.SourceRegistry:
		return "ЕГРЮЛ/ЕГРИП"
	case domain.SourceFNS:
		return "ФНС"
	case domain.SourceBKI:
		return "БКИ"
	}
	return string(s)
}

func checkStatusTitle(s domain.CheckStatus) string {
	switch s {
	case domain.CheckOK:
		return "пройдена"
	case domain.CheckWarning:
		return "есть замечания"
	}
	return "не пройдена"
}

func lowerFirst(s string) string {
	r := []rune(s)
	if len(r) > 1 && r[1] >= 'а' && r[1] <= 'я' { // не трогаем аббревиатуры вроде «ФНС»
		r[0] = []rune(strings.ToLower(string(r[0])))[0]
	}
	return string(r)
}

var (
	digitGroups = regexp.MustCompile(`(\d)[\s\x{00a0}\x{202f}](\d{3})`)
	bigNumber   = regexp.MustCompile(`\d{4,}`)
)

// normalizeDigits склеивает разряды «1 500 000» → «1500000».
func normalizeDigits(s string) string {
	for {
		n := digitGroups.ReplaceAllString(s, "$1$2")
		if n == s {
			return s
		}
		s = n
	}
}

// UnsupportedNumbers возвращает числа из ответа (от 4 знаков: суммы, годы, ИНН),
// которых нет в фактах. Непустой результат означает, что модель что-то выдумала.
func UnsupportedNumbers(output, facts string) []string {
	known := map[string]bool{}
	for _, n := range bigNumber.FindAllString(normalizeDigits(facts), -1) {
		known[n] = true
	}
	var bad []string
	for _, n := range bigNumber.FindAllString(normalizeDigits(output), -1) {
		if !known[n] {
			bad = append(bad, n)
		}
	}
	return bad
}
