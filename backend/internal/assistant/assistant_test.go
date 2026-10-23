package assistant

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"alfahack/backend/internal/domain"
)

var silent = slog.New(slog.NewTextHandler(io.Discard, nil))

// Досье пекарни из демо-сценария: молодой бизнес, нет кредитной истории, рейтинг D.
func bakery() Facts {
	return Facts{
		Number: "КЗ-2026-1002", Company: "ООО «Хлебный дом»", CompanyKind: domain.CompanyLegal, INN: "5403456780",
		OKVED: "56.10", OKVEDName: "Деятельность ресторанов", Region: "Новосибирская область",
		RegisteredAt: time.Date(2025, 12, 19, 0, 0, 0, 0, time.UTC), AgeMonths: 10,
		Product: "Экспресс-кредит без залога", Purpose: "Закрыть кассовый разрыв",
		Amount: 1_000_000, TermMonths: 24, Status: "На рассмотрении у специалиста",
		Financials: domain.Financials{AnnualRevenue: 9_600_000, AnnualNetProfit: 580_000},
		Checks: []domain.CheckResult{
			{Source: domain.SourceRegistry, Status: domain.CheckOK, Summary: "Действующая компания"},
			{Source: domain.SourceBKI, Status: domain.CheckWarning, Summary: "Кредитная история не найдена"},
		},
		Score: &domain.ScoreResult{Score: 595, Grade: "D", PD: 0.085, Factors: []domain.ScoreFactor{
			{Code: "AGE", Title: "Срок работы бизнеса", Value: "10 мес.", Points: -35},
			{Code: "CREDIT_HISTORY", Title: "Кредитная история", Value: "нет истории", Points: -10},
			{Code: "DEBT_LOAD", Title: "Платежи по кредитам к выручке", Value: "6% выручки", Points: 40},
			{Code: "MARGIN", Title: "Рентабельность", Value: "6.0%", Points: 10},
		}},
		Decision: &domain.Decision{Outcome: domain.OutcomeManualReview, ReasonCodes: []string{"BORDERLINE_RISK"},
			Reasons: []string{"Пограничная оценка риска — нужно экспертное заключение."},
			Offer:   &domain.Offer{Amount: 1_000_000, TermMonths: 24, Rate: 26.5, MonthlyPayment: 54_235}},
		Documents: []string{"Выписка по расчётному счёту за 6 месяцев"},
	}
}

func TestTemplate_CoversDossier(t *testing.T) {
	s, err := Template{}.Summarize(context.Background(), bakery())
	if err != nil {
		t.Fatal(err)
	}
	if s.Source != "template" {
		t.Fatalf("source %q", s.Source)
	}
	for _, want := range []string{
		"Профиль: ООО «Хлебный дом»", "работает 10 мес.",
		"Запрос: Экспресс-кредит без залога — 1 000 000 ₽ на 24 мес.",
		"рейтинг D", "Сильные стороны: платежи по кредитам к выручке",
		"Риски: срок работы бизнеса — 10 мес. (-35)",
		"БКИ — кредитная история не найдена",
		"Почему у эксперта: Пограничная оценка риска",
		"регулярность поступлений по выписке", "устойчивость выручки помесячно",
	} {
		if !strings.Contains(s.Text, want) {
			t.Errorf("summary misses %q:\n%s", want, s.Text)
		}
	}
	// Шаблон не выдумывает чисел по определению.
	if bad := UnsupportedNumbers(s.Text, bakery().Text()); len(bad) > 0 {
		t.Errorf("template contains numbers absent from facts: %v", bad)
	}
}

func TestUnsupportedNumbers(t *testing.T) {
	facts := bakery().Text()
	if bad := UnsupportedNumbers("Запрос: 1 000 000 ₽ на 24 мес., выручка 9600000 ₽, ИНН 5403456780", facts); len(bad) != 0 {
		t.Fatalf("known numbers flagged: %v", bad)
	}
	bad := UnsupportedNumbers("Выручка 12 400 000 ₽, прибыль 580 000 ₽", facts)
	if len(bad) != 1 || bad[0] != "12400000" {
		t.Fatalf("invented revenue must be flagged, got %v", bad)
	}
	// Короткие числа (сроки, проценты) не проверяем — они слишком часто совпадают случайно.
	if bad := UnsupportedNumbers("рост на 15%, 3 пункта", facts); len(bad) != 0 {
		t.Fatalf("small numbers must be ignored: %v", bad)
	}
}

func fakeOllama(t *testing.T, reply string, status int, seen *chatRequest) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if seen != nil {
			_ = json.NewDecoder(r.Body).Decode(seen)
		}
		w.WriteHeader(status)
		if status == http.StatusOK {
			_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"role": "assistant", "content": reply}, "done": true})
		} else {
			_ = json.NewEncoder(w).Encode(map[string]string{"error": reply})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Хороший ответ модели: три секции, каждая опирается на факторы скоринга.
const goodReply = `**Сильные стороны:** низкая долговая нагрузка — платежи около 6% выручки; бизнес прибыльный.
Риски: компания работает всего 10 мес.; кредитной истории нет; отраслевой риск общепита.
Что проверить:
1. Регулярность поступлений по выписке за 6 месяцев;
2. Сезонность выручки ресторана.`

func lines(s string) []string { return strings.Split(s, "\n") }

func TestOllama_HybridSummary(t *testing.T) {
	var seen chatRequest
	srv := fakeOllama(t, goodReply, http.StatusOK, &seen)
	s, err := NewOllama(srv.URL, "qwen2.5:1.5b-instruct", silent).Summarize(context.Background(), bakery())
	if err != nil {
		t.Fatal(err)
	}
	if s.Source != "llm" || s.Model != "qwen2.5:1.5b-instruct" || s.Warning != "" {
		t.Fatalf("expected llm summary, got %+v", s)
	}
	if strings.Join(s.ModelSections, ",") != "Сильные стороны,Риски,Что проверить" {
		t.Fatalf("model sections: %v", s.ModelSections)
	}
	got := lines(s.Text)
	want := []string{"Профиль: ", "Запрос: ", "Оценка: ", "Сильные стороны: низкая долговая нагрузка",
		"Риски: компания работает всего 10 мес.", "Почему у эксперта: Пограничная оценка риска", "Что проверить: Регулярность поступлений"}
	if len(got) != len(want) {
		t.Fatalf("want %d lines, got:\n%s", len(want), s.Text)
	}
	for i := range want {
		if !strings.HasPrefix(got[i], want[i]) {
			t.Errorf("line %d: want prefix %q, got %q", i, want[i], got[i])
		}
	}
	// Факты (профиль, запрос, причина) — из данных заявки, а не от модели.
	if !strings.Contains(s.Text, "Запрос: Экспресс-кредит без залога — 1 000 000 ₽ на 24 мес.") {
		t.Errorf("request line must come from facts:\n%s", s.Text)
	}
	if seen.Stream || len(seen.Messages) != 2 || strings.Contains(seen.Messages[0].Content, "Пример ответа") {
		t.Fatalf("bad request %+v", seen)
	}
	if !strings.Contains(seen.Messages[1].Content, "Положительные факторы:\n- Платежи по кредитам к выручке") ||
		!strings.Contains(seen.Messages[1].Content, "Отрицательные факторы:\n- Срок работы бизнеса") {
		t.Fatal("prompt must list factors by sign")
	}
}

// Реальный ответ qwen2.5:1.5b со стенда: «низкая рентабельность» как сильная
// сторона и пропавшие риски. Такие пункты не должны дойти до эксперта.
const realBadReply = `Сильные стороны: Потребительский кредит без залога; Динамичная выручка (9 600 000 ₽ за год); Низкая рентабельность (6.0%).
Почему у эксперта: Кредитная история не найдена.
Что проверить: Проверка БКИ, ФНС, ЕГРЮЛ/ЕГРИП.`

func TestOllama_DropsUngroundedStatements(t *testing.T) {
	srv := fakeOllama(t, realBadReply, http.StatusOK, nil)
	s, err := NewOllama(srv.URL, "m", silent).Summarize(context.Background(), bakery())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(s.Text, "Низкая рентабельность") || strings.Contains(s.Text, "Динамичная выручка") ||
		strings.Contains(s.Text, "Потребительский кредит") {
		t.Fatalf("ungrounded strengths must be dropped:\n%s", s.Text)
	}
	// Секции, где у модели ничего не подтвердилось, берутся из шаблона.
	if !strings.Contains(s.Text, "Сильные стороны: платежи по кредитам к выручке") || !strings.Contains(s.Text, "Риски: срок работы бизнеса") {
		t.Fatalf("template sections must fill the gaps:\n%s", s.Text)
	}
	// «Почему у эксперта» модели игнорируется — причина берётся из решения системы.
	if !strings.Contains(s.Text, "Почему у эксперта: Пограничная оценка риска") {
		t.Fatalf("reason must come from the decision:\n%s", s.Text)
	}
	if strings.Join(s.ModelSections, ",") != "Что проверить" {
		t.Fatalf("only checked sections are attributed to the model: %v", s.ModelSections)
	}
}

// Первый реальный ответ: сильные стороны перечисляют цифры, а не выводы.
func TestOllama_CopiedExampleIsRejected(t *testing.T) {
	reply := "Сильные стороны: кредитная история без просрочек; выручка растёт.\nРиски: платежи по кредитам занимают заметную долю выручки.\nЧто проверить: ок."
	srv := fakeOllama(t, reply, http.StatusOK, nil)
	s, _ := NewOllama(srv.URL, "m", silent).Summarize(context.Background(), bakery())
	// У пекарни нет кредитной истории (−10), выручка без динамики (0), нагрузка низкая (+40):
	// ни один пункт не подтверждается — сводка целиком шаблонная.
	if s.Source != "template" || !strings.Contains(s.Warning, "не подтвердился") {
		t.Fatalf("contradicting statements must fall back to template: %+v", s)
	}
}

func TestOllama_RejectsHallucinatedNumbers(t *testing.T) {
	reply := strings.Replace(goodReply, "6% выручки", "6% выручки, это 48 000 ₽ в месяц", 1)
	srv := fakeOllama(t, reply, http.StatusOK, nil)
	s, err := NewOllama(srv.URL, "m", silent).Summarize(context.Background(), bakery())
	if err != nil {
		t.Fatal(err)
	}
	if s.Source != "template" || !strings.Contains(s.Warning, "не прошёл проверку фактов") {
		t.Fatalf("hallucinated amount must fall back to template, got %+v", s)
	}
}

func TestOllama_FallbacksOnFailures(t *testing.T) {
	cases := map[string]struct {
		srv  func(t *testing.T) string
		warn string
	}{
		"model not pulled": {func(t *testing.T) string {
			return fakeOllama(t, `model "qwen2.5:1.5b-instruct" not found, try pulling it first`, http.StatusNotFound, nil).URL
		}, "загружается"},
		"server error": {func(t *testing.T) string { return fakeOllama(t, "boom", 500, nil).URL }, "недоступна"},
		"unreachable":  {func(t *testing.T) string { return "http://127.0.0.1:1" }, "недоступна"},
		"wrong format": {func(t *testing.T) string {
			return fakeOllama(t, "Привет! Чем помочь?", http.StatusOK, nil).URL
		}, "не подтвердился"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s, err := NewOllama(c.srv(t), "m", silent).Summarize(context.Background(), bakery())
			if err != nil || s.Source != "template" || !strings.Contains(s.Warning, c.warn) {
				t.Fatalf("want template with %q warning, got %+v %v", c.warn, s, err)
			}
		})
	}
}

func TestOllama_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(300 * time.Millisecond):
		}
	}))
	defer srv.Close()
	o := NewOllama(srv.URL, "m", silent)
	o.Timeout = 50 * time.Millisecond
	s, _ := o.Summarize(context.Background(), bakery())
	if s.Source != "template" || !strings.Contains(s.Warning, "не ответила вовремя") {
		t.Fatalf("timeout must fall back: %+v", s)
	}
}

// Ответ с метками-синонимами и нумерованными списками разбирается корректно.
func TestParseSections_ListsAndAliases(t *testing.T) {
	raw := "Продукт: Экспресс-кредит\nЗапрошено: 1 000 000 ₽ на 24 мес.\nРиски:\n1. Нет кредитной истории.\n2. Пограничная оценка риска.\nПочему у эксперта:\n1. Рейтинг D."
	p := parseSections(cleanOutput(raw))
	if strings.Join(p["Запрос"], "|") != "Экспресс-кредит|1 000 000 ₽ на 24 мес." {
		t.Errorf("aliases: %q", p["Запрос"])
	}
	if strings.Join(p["Риски"], "|") != "Нет кредитной истории|Пограничная оценка риска" {
		t.Errorf("lists: %q", p["Риски"])
	}
	if len(p["Почему у эксперта"]) != 1 {
		t.Errorf("multi-byte label must be recognised: %v", p)
	}
}

// Реальный ответ после перехода на гибрид: служебные пометки скоринга и точка перед «;».
func TestOllama_CleansJargonAndPunctuation(t *testing.T) {
	reply := "Сильные стороны: Платежи по кредитам к выручке составляют 6% (вклад +40); Рентабельность составляет 6.0%.\n" +
		"Риски: Срок работы бизнеса: 10 мес.; Кредитная история: нет истории.\nЧто проверить: Проверка БКИ: кредитная история не найдена."
	srv := fakeOllama(t, reply, http.StatusOK, nil)
	s, _ := NewOllama(srv.URL, "m", silent).Summarize(context.Background(), bakery())
	if strings.Contains(s.Text, "вклад") || strings.Contains(s.Text, "%.;") {
		t.Fatalf("jargon and stray dots must be removed:\n%s", s.Text)
	}
	if !strings.Contains(s.Text, "Риски: Срок работы бизнеса: 10 мес.; Кредитная история: нет истории.") {
		t.Fatalf("abbreviation dot must stay:\n%s", s.Text)
	}
	for _, l := range lines(s.Text) {
		if strings.HasPrefix(l, "Что проверить:") && strings.Count(l, ";") < 1 {
			t.Fatalf("checks must be topped up to 2–3 items: %q", l)
		}
	}
}
