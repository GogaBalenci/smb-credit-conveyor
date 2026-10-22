package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"alfahack/backend/internal/domain"
	"alfahack/backend/internal/integrations"
	"alfahack/backend/internal/metrics"
	"alfahack/backend/internal/policy"
	"alfahack/backend/internal/scoring"
	"alfahack/backend/internal/store"
)

// Пороги дозапроса документов.
const (
	DocsAmountThreshold  = 5_000_000 // свыше — нужна бухгалтерская отчётность
	DocsYoungBusinessAge = 12        // младше (мес.) — нужна выписка по счёту
)

// RequiredDocuments — какие документы нужны по правилам банка. Банк сначала
// пытается обойтись данными из реестров и ФНС и запрашивает документы только
// там, где их нельзя получить автоматически.
func RequiredDocuments(app domain.Application, c domain.Company, now time.Time) []domain.DocRequest {
	var out []domain.DocRequest
	if c.AgeMonths(now) < DocsYoungBusinessAge {
		out = append(out, domain.DocRequest{Kind: domain.DocBankStatement,
			Reason: "Бизнес работает меньше года — по выписке банк увидит реальные обороты."})
	}
	if app.Amount > DocsAmountThreshold {
		out = append(out, domain.DocRequest{Kind: domain.DocFinancialStatements,
			Reason: "Для суммы свыше " + domain.FormatRub(DocsAmountThreshold) + " нужна полная отчётность за год."})
	}
	switch app.ProductCode {
	case "TENDER":
		out = append(out, domain.DocRequest{Kind: domain.DocContract, Reason: "Кредит выдаётся под конкретный госконтракт."})
	case "INVESTMENT":
		out = append(out, domain.DocRequest{Kind: domain.DocCollateral, Reason: "Нужны документы на имущество, предлагаемое в залог."})
	}
	return out
}

// mergeDocs объединяет требования правил с уже существующими (в т.ч. от андеррайтера),
// сохраняя отметки о предоставлении.
func mergeDocs(existing, required []domain.DocRequest, uploaded map[domain.DocKind]bool) []domain.DocRequest {
	out := append([]domain.DocRequest{}, existing...)
	for _, r := range required {
		found := false
		for _, e := range out {
			found = found || e.Kind == r.Kind
		}
		if !found {
			out = append(out, r)
		}
	}
	for i := range out {
		if uploaded[out[i].Kind] {
			out[i].Provided = true
		}
	}
	return out
}

// ProcessTask выполняет одну задачу конвейера. Обработчики идемпотентны:
// если заявка уже не в ожидаемом статусе (повтор задачи), они ничего не делают.
func (s *Service) ProcessTask(ctx context.Context, t store.Task) error {
	switch t.Kind {
	case TaskChecks:
		return s.runChecks(ctx, t.ApplicationID)
	case TaskScore:
		return s.runScoring(ctx, t.ApplicationID)
	case TaskDisburse:
		return s.runDisbursement(ctx, t.ApplicationID)
	default:
		return fmt.Errorf("unknown task kind %q", t.Kind)
	}
}

// sourceData — ответы трёх источников.
type sourceData struct {
	registry    integrations.RegistryRecord
	registryErr error
	fns         integrations.FNSProfile
	fnsErr      error
	bki         integrations.CreditReport
	bkiErr      error
}

func (s *Service) fetchSources(ctx context.Context, inn string) sourceData {
	var d sourceData
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); d.registry, d.registryErr = s.src.Registry(ctx, inn) }()
	go func() { defer wg.Done(); d.fns, d.fnsErr = s.src.FNS(ctx, inn) }()
	go func() { defer wg.Done(); d.bki, d.bkiErr = s.src.CreditReport(ctx, inn) }()
	wg.Wait()
	return d
}

// runChecks — этап проверок: SUBMITTED → CHECKING → NEEDS_INFO | SCORING.
func (s *Service) runChecks(ctx context.Context, appID string) error {
	app, err := s.store.GetApplication(ctx, appID)
	if err != nil {
		return err
	}
	switch app.Status {
	case domain.StatusSubmitted:
		if app, err = s.transition(ctx, transitionReq{appID: appID, to: domain.StatusChecking, actor: domain.ActorSystem,
			comment: "Запрошены данные ЕГРЮЛ/ЕГРИП, ФНС и БКИ", expect: []domain.Status{domain.StatusSubmitted}}); err != nil {
			return err
		}
	case domain.StatusChecking:
		// повтор после сбоя источника — продолжаем
	default:
		return nil
	}

	company, err := s.store.GetCompany(ctx, app.CompanyID)
	if err != nil {
		return err
	}
	d := s.fetchSources(ctx, company.INN)
	// Источник недоступен — задача будет повторена с задержкой, заявка остаётся в CHECKING.
	// В ошибку попадают все недоступные источники: по ней клиенту объясняется задержка.
	var failed []error
	for _, e := range []error{d.registryErr, d.fnsErr, d.bkiErr} {
		if e != nil && !errors.Is(e, integrations.ErrNotFound) {
			metrics.Default.Inc("conveyor_source_errors_total", "Ошибки внешних источников на этапе проверок")
			failed = append(failed, e)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("checks: %w", errors.Join(failed...))
	}

	checks := buildChecks(d, s.now())
	registryActive := d.registryErr == nil && d.registry.Status == "ACTIVE"
	if d.registryErr == nil {
		updated := companyFromRecord(d.registry)
		updated.ID = company.ID
		if company, err = s.store.UpsertCompany(ctx, updated); err != nil {
			return err
		}
	}
	docs, err := s.store.ListDocuments(ctx, appID)
	if err != nil {
		return err
	}
	uploaded := map[domain.DocKind]bool{}
	for _, doc := range docs {
		uploaded[doc.Kind] = true
	}
	required := mergeDocs(app.RequiredDocs, RequiredDocuments(app, company, s.now()), uploaded)
	var missing []domain.DocRequest
	for _, r := range required {
		if !r.Provided {
			missing = append(missing, r)
		}
	}

	// Сохраняем результаты проверок и решаем, куда идти дальше — атомарно.
	to, task, comment := domain.StatusScoring, TaskScore, "Проверки пройдены, заявка передана на скоринг"
	if registryActive && len(missing) > 0 {
		to, task, comment = domain.StatusNeedsInfo, "", "Нужны документы: "+docTitles(missing)
	} else if !registryActive {
		comment = "Компания не действует по данным реестра"
	}
	_, err = s.transition(ctx, transitionReq{
		appID: appID, to: to, actor: domain.ActorSystem, comment: comment, task: task,
		expect: []domain.Status{domain.StatusChecking},
		mutate: func(a *domain.Application) error {
			a.RequiredDocs = required
			return nil
		},
		extra: func(q *store.Queries) error {
			for _, c := range checks {
				if err := q.SaveCheck(ctx, appID, c); err != nil {
					return err
				}
			}
			return nil
		},
	})
	return err
}

func buildChecks(d sourceData, now time.Time) []domain.CheckResult {
	raw := func(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
	var out []domain.CheckResult

	reg := domain.CheckResult{Source: domain.SourceRegistry, CheckedAt: now}
	switch {
	case errors.Is(d.registryErr, integrations.ErrNotFound):
		reg.Status, reg.Summary, reg.Payload = domain.CheckFail, "Компания не найдена в ЕГРЮЛ/ЕГРИП", raw(map[string]string{})
	case d.registry.Status != "ACTIVE":
		reg.Status, reg.Summary, reg.Payload = domain.CheckFail, "Статус в реестре: "+d.registry.Status, raw(d.registry)
	default:
		reg.Status, reg.Payload = domain.CheckOK, raw(d.registry)
		reg.Summary = fmt.Sprintf("Действующая компания, регистрация %s, ОКВЭД %s", d.registry.RegisteredAt, d.registry.OKVED)
	}
	out = append(out, reg)

	fns := domain.CheckResult{Source: domain.SourceFNS, CheckedAt: now, Status: domain.CheckOK, Payload: raw(d.fns)}
	var notes []string
	if d.fnsErr != nil {
		fns.Status, notes = domain.CheckWarning, append(notes, "нет данных ФНС")
	} else {
		if len(d.fns.Reports) > 0 {
			r := d.fns.Reports[0]
			notes = append(notes, fmt.Sprintf("выручка за %d: %s", r.Year, domain.FormatRub(r.Revenue)))
		}
		if d.fns.TaxArrears > 0 {
			fns.Status = domain.CheckWarning
			notes = append(notes, "недоимка "+domain.FormatRub(d.fns.TaxArrears))
		}
		if d.fns.MassAddress {
			fns.Status = domain.CheckWarning
			notes = append(notes, "адрес массовой регистрации")
		}
		if d.fns.Bankruptcy || d.fns.DisqualifiedDirector {
			fns.Status = domain.CheckFail
			notes = append(notes, "банкротство или дисквалификация руководителя")
		}
	}
	fns.Summary = capitalize(strings.Join(notes, "; "))
	out = append(out, fns)

	bki := domain.CheckResult{Source: domain.SourceBKI, CheckedAt: now, Payload: raw(d.bki)}
	active, closed, monthly, maxOverdue := d.bki.Summary()
	switch {
	case d.bkiErr != nil || !d.bki.Found:
		bki.Status, bki.Summary = domain.CheckWarning, "Кредитная история не найдена"
	case maxOverdue >= 90:
		bki.Status, bki.Summary = domain.CheckFail, fmt.Sprintf("Просрочка %d дн.", maxOverdue)
	case maxOverdue > 0:
		bki.Status, bki.Summary = domain.CheckWarning, fmt.Sprintf("Были просрочки до %d дн.; действующих кредитов: %d", maxOverdue, active)
	default:
		bki.Status = domain.CheckOK
		bki.Summary = fmt.Sprintf("Без просрочек; действующих: %d, закрытых: %d, платежи %s/мес.", active, closed, domain.FormatRub(monthly))
	}
	out = append(out, bki)
	return out
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

// runScoring — этап оценки: SCORING → APPROVED | COUNTER_OFFER | MANUAL_REVIEW | REJECTED.
// Скоринг строится по сохранённым результатам проверок, а не по новым запросам —
// так решение воспроизводимо и объяснимо на тех же данных, что видит андеррайтер.
func (s *Service) runScoring(ctx context.Context, appID string) error {
	app, err := s.store.GetApplication(ctx, appID)
	if err != nil {
		return err
	}
	if app.Status != domain.StatusScoring {
		return nil
	}
	company, err := s.store.GetCompany(ctx, app.CompanyID)
	if err != nil {
		return err
	}
	checks, err := s.store.ListChecks(ctx, appID)
	if err != nil {
		return err
	}
	in, product, err := scoringInput(app, company, checks, s.now())
	if err != nil {
		return err
	}
	score := scoring.Score(in)
	decision := policy.Decide(policy.Request{
		Product: product, Amount: app.Amount, TermMonths: app.TermMonths, Financials: app.Financials,
		ExistingPayments: in.ExistingPayments(), Score: score,
	}, s.now())

	_, err = s.transition(ctx, transitionReq{
		appID: appID, to: decision.Outcome.TargetStatus(), actor: domain.ActorSystem,
		comment: fmt.Sprintf("Скоринг: %d баллов, рейтинг %s", score.Score, score.Grade),
		expect:  []domain.Status{domain.StatusScoring},
		mutate: func(a *domain.Application) error {
			a.Score, a.Decision = &score, &decision
			return nil
		},
	})
	if err == nil {
		metrics.Default.Inc("conveyor_decisions_total", "Автоматические решения конвейера", "outcome", string(decision.Outcome))
	}
	return err
}

// scoringInput собирает вход модели из заявки, профиля компании и сохранённых проверок.
func scoringInput(app domain.Application, c domain.Company, checks []domain.CheckResult, now time.Time) (scoring.Input, domain.Product, error) {
	product, ok := domain.ProductByCode(app.ProductCode)
	if !ok {
		return scoring.Input{}, product, fmt.Errorf("unknown product %s", app.ProductCode)
	}
	in := scoring.Input{
		RegistryActive:   c.RegistryStatus == "ACTIVE",
		CompanyAgeMonths: c.AgeMonths(now),
		OKVEDSection:     c.OKVEDSection(),
		Financials:       app.Financials,
		Amount:           app.Amount,
		RequestedPayment: policy.Annuity(app.Amount, product.BaseRate, app.TermMonths),
	}
	for _, ch := range checks {
		switch ch.Source {
		case domain.SourceRegistry:
			in.RegistryActive = in.RegistryActive && ch.Status == domain.CheckOK
		case domain.SourceFNS:
			var f integrations.FNSProfile
			if err := json.Unmarshal(ch.Payload, &f); err == nil {
				in.Tax = scoring.TaxProfile{TaxArrears: f.TaxArrears, MassAddress: f.MassAddress,
					DisqualifiedDirector: f.DisqualifiedDirector, Bankruptcy: f.Bankruptcy}
			}
		case domain.SourceBKI:
			var r integrations.CreditReport
			if err := json.Unmarshal(ch.Payload, &r); err == nil {
				active, closed, monthly, maxOverdue := r.Summary()
				in.Credit = scoring.CreditHistory{Found: r.Found, ActiveLoans: active, ClosedLoans: closed,
					MonthlyPayments: monthly, MaxOverdueDays: maxOverdue, Inquiries30d: r.Inquiries30d}
			}
		}
	}
	return in, product, nil
}

// runDisbursement — выдача средств (смоделирована, без реальных операций).
func (s *Service) runDisbursement(ctx context.Context, appID string) error {
	app, err := s.store.GetApplication(ctx, appID)
	if err != nil {
		return err
	}
	if app.Status != domain.StatusSigned {
		return nil
	}
	amount := app.Amount
	if app.Decision != nil && app.Decision.Offer != nil {
		amount = app.Decision.Offer.Amount
	}
	_, err = s.transition(ctx, transitionReq{
		appID: appID, to: domain.StatusIssued, actor: domain.ActorSystem, expect: []domain.Status{domain.StatusSigned},
		comment: "Зачислено на расчётный счёт: " + domain.FormatRub(amount) + " (операция смоделирована)",
	})
	return err
}

// RunWorker обрабатывает очередь задач до отмены контекста. Можно запускать
// несколько воркеров (в т.ч. в разных контейнерах) — SKIP LOCKED разведёт их.
func (s *Service) RunWorker(ctx context.Context, concurrency int, poll time.Duration) {
	if concurrency < 1 {
		concurrency = 1
	}
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for ctx.Err() == nil {
				if !s.RunOnce(ctx) {
					select {
					case <-ctx.Done():
					case <-time.After(poll):
					}
				}
			}
		}(i)
	}
	wg.Wait()
}

// RunOnce обрабатывает одну задачу; false — если очередь пуста.
func (s *Service) RunOnce(ctx context.Context) bool {
	t, err := s.store.ClaimTask(ctx, s.cfg.TaskLease)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Error("claim task", "err", err)
		}
		return false
	}
	if t == nil {
		return false
	}
	taskCtx, cancel := context.WithTimeout(ctx, s.cfg.TaskLease-5*time.Second)
	defer cancel()
	start := time.Now()
	err = s.ProcessTask(taskCtx, *t)
	log := s.log.With("task", t.ID, "kind", t.Kind, "app", t.ApplicationID, "attempt", t.Attempts, "ms", time.Since(start).Milliseconds())
	if err != nil {
		delay := time.Duration(1<<min(t.Attempts, 6)) * time.Second
		log.Warn("task failed, will retry", "err", err, "delay", delay)
		metrics.Default.Inc("conveyor_tasks_total", "Задачи конвейера", "kind", t.Kind, "result", "retry")
		if rerr := s.store.RetryTask(context.WithoutCancel(ctx), *t, err, delay); rerr != nil {
			log.Error("retry task", "err", rerr)
		}
		return true
	}
	if err := s.store.CompleteTask(context.WithoutCancel(ctx), t.ID); err != nil {
		log.Error("complete task", "err", err)
	}
	metrics.Default.Inc("conveyor_tasks_total", "Задачи конвейера", "kind", t.Kind, "result", "ok")
	log.Info("task done")
	return true
}
