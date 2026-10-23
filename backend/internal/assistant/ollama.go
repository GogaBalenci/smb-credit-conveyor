package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Гибридная сводка: факты — из правил, аналитика — от модели.
//
// Секции «Профиль», «Запрос», «Оценка» и «Почему у эксперта» всегда составляет
// код: это пересказ данных, и модель здесь может только ошибиться. Модель пишет
// аналитику — «Сильные стороны», «Риски», «Что проверить», — и каждый её пункт
// сверяется с факторами скоринга (см. groundItems). Непрошедший проверку пункт
// отбрасывается; если секция опустела, берётся её шаблонный вариант.

// SystemPrompt — инструкция модели. Модель формулирует, а не решает.
const SystemPrompt = `Ты помощник кредитного эксперта банка. По фактам заявки малого бизнеса напиши три строки на русском языке.

Правила:
- Используй только факты из сообщения. Ничего не придумывай.
- Сильные стороны — только из списка «Положительные факторы». Риски — только из списка «Отрицательные факторы».
- Пиши своими словами, коротко, без нумерации, без вступлений и заключений.
- Не меняй суммы. Не советуй одобрить или отказать — решение принимает эксперт.

Формат — ровно три строки:
Сильные стороны: 1–3 пункта через точку с запятой.
Риски: 1–3 пункта через точку с запятой.
Что проверить: 2–3 конкретных вопроса эксперту по рискам через точку с запятой.`

// Секции, которые пишет модель.
var modelSections = []string{"Сильные стороны", "Риски", "Что проверить"}

// Порядок секций в итоговой гибридной сводке.
var hybridOrder = []string{"Профиль", "Запрос", "Оценка", "Сильные стороны", "Риски", "Почему у эксперта", "Что проверить"}

// Ollama — сводка локальной моделью с открытыми весами через Ollama API.
// При любой проблеме (сервис недоступен, модель не загружена, таймаут,
// выдуманные числа, ни одного проверенного пункта) возвращается шаблонная сводка.
type Ollama struct {
	BaseURL  string
	Model    string
	Timeout  time.Duration
	Fallback Template
	HTTP     *http.Client
	Log      *slog.Logger
}

// NewOllama создаёт клиента.
func NewOllama(baseURL, model string, log *slog.Logger) *Ollama {
	if log == nil {
		log = slog.Default()
	}
	return &Ollama{
		BaseURL: strings.TrimRight(baseURL, "/"), Model: model, Timeout: 45 * time.Second,
		HTTP: &http.Client{}, Log: log,
	}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string         `json:"model"`
	Messages []chatMessage  `json:"messages"`
	Stream   bool           `json:"stream"`
	Options  map[string]any `json:"options"`
}

type chatResponse struct {
	Message chatMessage `json:"message"`
	Error   string      `json:"error"`
}

var (
	errHallucination = errors.New("model output contains numbers absent from facts")
	errBadFormat     = errors.New("model output has no usable sections")
	errUngrounded    = errors.New("no model statement is backed by scoring factors")
	errModelMissing  = errors.New("model is not loaded")
)

// Summarize строит гибридную сводку.
func (o *Ollama) Summarize(ctx context.Context, f Facts) (Summary, error) {
	start := time.Now()
	base, err := o.Fallback.Summarize(ctx, f)
	if err != nil {
		return base, err
	}
	baseSections := parseSections(base.Text)

	facts := f.Text()
	raw, err := o.chat(ctx, facts+factorLists(f))
	var model map[string][]string
	if err == nil {
		model = parseSections(cleanOutput(raw))
		if bad := UnsupportedNumbers(joinAll(model, modelSections), facts); len(bad) > 0 {
			err = fmt.Errorf("%w: %s", errHallucination, strings.Join(bad, ", "))
		}
	}
	var used []string
	if err == nil {
		model, used = groundItems(model, f)
		if len(used) == 0 {
			err = errUngrounded
		}
	}
	if err != nil {
		o.Log.WarnContext(ctx, "llm summary rejected, using template", "err", err, "model", o.Model)
		base.Warning = warningFor(err)
		return base, nil
	}

	var lines []string
	for _, label := range hybridOrder {
		items := baseSections[label]
		if contains(used, label) {
			items = model[label]
			// Вопросов должно быть 2–3: если модель дала меньше, добавляем вопросы из правил.
			if label == "Что проверить" {
				items = appendUnique(items, baseSections[label], 3)
			}
		}
		if len(items) > 0 {
			lines = append(lines, renderLine(label, items))
		}
	}
	return Summary{
		Text: strings.Join(lines, "\n"), Source: "llm", Model: o.Model, ModelSections: used,
		GeneratedAt: time.Now(), DurationMS: time.Since(start).Milliseconds(),
	}, nil
}

func warningFor(err error) string {
	switch {
	case errors.Is(err, errHallucination):
		return "Ответ модели не прошёл проверку фактов (суммы не совпали с данными заявки), поэтому показана шаблонная сводка."
	case errors.Is(err, errUngrounded), errors.Is(err, errBadFormat):
		return "Ответ модели не подтвердился факторами скоринга, поэтому показана шаблонная сводка."
	case errors.Is(err, errModelMissing):
		return "Модель ещё загружается — показана шаблонная сводка. Попробуйте через пару минут."
	case errors.Is(err, context.DeadlineExceeded):
		return "Модель не ответила вовремя — показана шаблонная сводка."
	default:
		return "Модель недоступна — показана шаблонная сводка."
	}
}

// factorLists — явные списки факторов по знаку, чтобы маленькой модели было
// проще не перепутать сильные стороны с рисками.
func factorLists(f Facts) string {
	if f.Score == nil {
		return ""
	}
	pos, neg := splitFactors(f.Score.Factors)
	var b strings.Builder
	b.WriteString("\nПоложительные факторы:\n")
	for _, x := range pos {
		fmt.Fprintf(&b, "- %s: %s\n", x.Title, x.Value)
	}
	b.WriteString("Отрицательные факторы:\n")
	for _, x := range neg {
		fmt.Fprintf(&b, "- %s: %s\n", x.Title, x.Value)
	}
	for _, c := range f.Checks {
		if c.Status != "ok" {
			fmt.Fprintf(&b, "- Проверка %s: %s\n", sourceTitle(c.Source), c.Summary)
		}
	}
	return b.String()
}

func (o *Ollama) chat(ctx context.Context, userContent string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	body, _ := json.Marshal(chatRequest{
		Model:  o.Model,
		Stream: false,
		Messages: []chatMessage{
			{Role: "system", Content: SystemPrompt},
			{Role: "user", Content: "Факты по заявке:\n" + userContent},
		},
		// Низкая температура — пересказ, а не творчество.
		Options: map[string]any{"temperature": 0.1, "num_predict": 250},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	var out chatResponse
	_ = json.Unmarshal(raw, &out)
	if resp.StatusCode == http.StatusNotFound || strings.Contains(out.Error, "not found") {
		return "", errModelMissing
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama status %d: %s", resp.StatusCode, out.Error)
	}
	if strings.TrimSpace(out.Message.Content) == "" {
		return "", errBadFormat
	}
	return out.Message.Content, nil
}

// --- Проверка утверждений модели ---

// concept — тема фактора скоринга: как модель может её назвать (match)
// и какие слова означают, что тема подана как недостаток (bad).
// Полярность зависит от темы: «низкая нагрузка» — плюс, «низкая рентабельность» — минус.
// (\b в RE2 работает только для латиницы, поэтому границы слов не используются.)
type concept struct {
	match *regexp.Regexp
	bad   *regexp.Regexp
}

func re(s string) *regexp.Regexp { return regexp.MustCompile("(?i)" + s) }

var concepts = map[string]concept{
	"AGE":            {re(`срок\S* работы|возраст|молод|недавн|работает[^;]{0,15}\d|\d+\s*мес\S*\s*(работы|на рынке)|начинающ|опыт\S* работы`), re(`молод|недавн|всего|мал|небольш|начинающ`)},
	"CREDIT_HISTORY": {re(`кредитн\S* истори|бки|просроч|без истории|истори\S* (нет|отсутств)`), re(`(^|\s)нет(\s|$)|отсутств|не найден|просрочк[^и]|с просроч|наруш`)},
	"INQUIRIES":      {re(`запрос\S* в бки|кредитн\S* активност|много запрос`), re(`много|част|высок`)},
	"DEBT_LOAD":      {re(`нагрузк|долг|платеж\S* по кредит|обязательств`), re(`высок|значит|больш|заметн|существенн`)},
	"MARGIN":         {re(`рентабельн|прибыл|маржин`), re(`низк|убыт|отрицат|слаб|невысок`)},
	"REVENUE_TREND":  {re(`динамик\S* выручк|рост\S* выручк|выручк\S* (рас|сниж|пад)|падени\S* выручк|снижени\S* выручк`), re(`сниж|пад|нет данных|отсутств`)},
	"TAX_ARREARS":    {re(`налог|недоимк`), re(`недоимк|задолж|долг`)},
	"MASS_ADDRESS":   {re(`массов\S* адрес|адрес\S* массов`), re(`массов`)},
	"INDUSTRY":       {re(`отрасл|общепит|строител|сельск|it-|айти`), re(`повышенн|риск|высок`)},
	"COLLATERAL":     {re(`залог|обеспечен`), re(`недостат|низк|мал|нет`)},
}

// groundItems оставляет только пункты, подтверждённые факторами скоринга:
// сильная сторона должна ссылаться на фактор с плюсом и не содержать негатива,
// риск — на фактор с минусом (или стоп-фактор). «Что проверить» — вопросы,
// их не сверяем по знаку, но ограничиваем тремя пунктами.
// Возвращает отфильтрованные секции и список секций, где остался хоть один пункт.
func groundItems(sections map[string][]string, f Facts) (map[string][]string, []string) {
	points := map[string]int{}
	stops := f.Score != nil && len(f.Score.StopFactors) > 0
	if f.Score != nil {
		for _, x := range f.Score.Factors {
			points[x.Code] = x.Points
		}
	}
	// strength: пункт называет фактор с плюсом и не подаёт его как недостаток;
	// risk: называет фактор с минусом; contra: подаёт фактор с плюсом как недостаток.
	classify := func(item string) (strength, risk, contra bool) {
		for code, c := range concepts {
			if !c.match.MatchString(item) {
				continue
			}
			p, known := points[code]
			if !known {
				continue
			}
			badWording := c.bad.MatchString(item)
			switch {
			case p > 0 && !badWording:
				strength = true
			case p > 0 && badWording:
				contra = true
			case p < 0:
				risk = true
			}
		}
		return
	}

	out := map[string][]string{}
	var used []string
	for _, label := range modelSections {
		var kept []string
		for _, item := range sections[label] {
			strength, risk, contra := classify(item)
			ok := false
			switch label {
			case "Сильные стороны":
				ok = strength && !risk && !contra
			case "Риски":
				ok = risk || (stops && strings.Contains(strings.ToLower(item), "стоп"))
			case "Что проверить":
				ok = utf8.RuneCountInString(item) >= 8
			}
			if ok && len(kept) < 3 {
				kept = append(kept, item)
			}
		}
		if len(kept) > 0 {
			out[label] = kept
			used = append(used, label)
		}
	}
	return out, used
}

// --- Разбор ответа ---

// cleanOutput убирает markdown-разметку, которую модели любят добавлять.
func cleanOutput(s string) string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(strings.NewReplacer("**", "", "__", "", "### ", "", "## ", "", "# ", "").Replace(l))
		l = strings.TrimLeft(l, "-*• ")
		if l != "" {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "\n")
}

// Все метки, которые встречаются в сводках (шаблон и модель).
var knownLabels = []string{
	"Профиль", "Запрос", "Финансы", "Оценка", "Сильные стороны", "Риски", "Проверки",
	"Почему у эксперта", "Не хватает", "Что проверить",
}

// Синонимы, которыми маленькие модели подменяют метки.
var sectionAliases = map[string]string{
	"Продукт": "Запрос", "Запрошено": "Запрос", "Цель": "Запрос", "Сумма": "Запрос", "Срок": "Запрос",
	"Плюсы": "Сильные стороны", "Преимущества": "Сильные стороны", "Минусы": "Риски", "Вопросы": "Что проверить",
}

var listMarker = regexp.MustCompile(`^(\d+[.)]|[-*•])\s*`)

// parseSections разбирает текст «Метка: пункт; пункт» в секции. Нумерованные
// списки под меткой и синонимы меток приводятся к общему виду; пункты делятся по «;».
func parseSections(s string) map[string][]string {
	parts := map[string][]string{}
	current := ""
	addItems := func(text string) {
		for _, it := range strings.Split(text, ";") {
			it = scoringJargon.ReplaceAllString(it, "")
			it = trimSentenceEnd(strings.TrimSpace(listMarker.ReplaceAllString(strings.TrimSpace(it), "")))
			if it != "" {
				parts[current] = append(parts[current], it)
			}
		}
	}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if label, rest, ok := splitLabel(line); ok {
			current = label
			addItems(rest)
			continue
		}
		if current != "" {
			addItems(line)
		}
	}
	return parts
}

// normalizeSections приводит произвольный ответ к строкам «Метка: пункт; пункт.»
// в каноническом порядке (используется для проверки разбора).
func normalizeSections(s string) string {
	parts := parseSections(s)
	var out []string
	for _, label := range knownLabels {
		if items := parts[label]; len(items) > 0 {
			out = append(out, renderLine(label, items))
		}
	}
	return strings.Join(out, "\n")
}

func renderLine(label string, items []string) string {
	line := label + ": " + strings.Join(items, "; ")
	if !strings.HasSuffix(line, ".") {
		line += "."
	}
	return line
}

func splitLabel(line string) (label, rest string, ok bool) {
	i := strings.Index(line, ":")
	if i <= 0 || utf8.RuneCountInString(line[:i]) > 30 {
		return "", "", false
	}
	head := strings.TrimSpace(line[:i])
	rest = strings.TrimSpace(line[i+1:])
	for _, l := range knownLabels {
		if strings.EqualFold(head, l) {
			return l, rest, true
		}
	}
	if canon, ok := sectionAliases[head]; ok {
		return canon, rest, true
	}
	return "", "", false
}

// trimSentenceEnd убирает завершающие «;», «,» и точку, но сохраняет точку
// у сокращений («мес.», «тыс.»), чтобы не исказить текст.
func trimSentenceEnd(s string) string {
	s = strings.TrimRight(s, ";, ")
	if !strings.HasSuffix(s, ".") {
		return s
	}
	body := strings.TrimSuffix(s, ".")
	words := strings.Fields(body)
	if len(words) > 0 && abbreviations[strings.ToLower(words[len(words)-1])] {
		return s
	}
	return body
}

// abbreviations — сокращения, после которых точка остаётся.
var abbreviations = map[string]bool{"мес": true, "тыс": true, "млн": true, "млрд": true, "руб": true, "г": true, "гг": true, "т.д": true, "т.п": true, "др": true, "дн": true}

// scoringJargon — служебные пометки скоринга, которые модель иногда переносит в текст.
var scoringJargon = regexp.MustCompile(`\s*\(?\s*вклад\s*[+−-]?\d+\s*\)?`)

func joinAll(sections map[string][]string, labels []string) string {
	var b strings.Builder
	for _, l := range labels {
		b.WriteString(strings.Join(sections[l], " "))
		b.WriteString("\n")
	}
	return b.String()
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// appendUnique дополняет items пунктами из extra без повторов, пока их меньше limit.
func appendUnique(items, extra []string, limit int) []string {
	out := append([]string{}, items...)
	for _, e := range extra {
		if len(out) >= limit {
			break
		}
		dup := false
		for _, x := range out {
			dup = dup || strings.EqualFold(x, e)
		}
		if !dup {
			out = append(out, e)
		}
	}
	return out
}
