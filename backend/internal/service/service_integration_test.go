package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"alfahack/backend/internal/auth"
	"alfahack/backend/internal/domain"
	"alfahack/backend/internal/service"
	"alfahack/backend/internal/store"
	"alfahack/backend/internal/testutil"
)

var ctx = context.Background()

func login(t *testing.T, env *testutil.Env, email string) auth.Principal {
	t.Helper()
	sess, err := env.Service.Login(ctx, email, service.DemoPassword)
	if err != nil {
		t.Fatalf("login %s: %v", email, err)
	}
	p, err := env.Tokens.Parse(sess.Token)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// apply — клиент проходит мастер: автозаполнение → черновик → отправка.
func apply(t *testing.T, env *testutil.Env, p auth.Principal, product string, purpose domain.Purpose, amount int64, term int) domain.Application {
	t.Helper()
	pre, err := env.Service.GetPrefill(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if pre.Financials.AnnualRevenue == 0 {
		t.Fatalf("prefill must fill revenue from FNS: %+v", pre)
	}
	in := domain.ApplicationInput{ProductCode: product, Purpose: purpose, Amount: amount, TermMonths: term, Financials: pre.Financials}
	app, err := env.Service.CreateDraft(ctx, p, in)
	if err != nil {
		t.Fatal(err)
	}
	if app.Status != domain.StatusDraft || app.Number == "" {
		t.Fatalf("unexpected draft %+v", app)
	}
	if app, err = env.Service.Submit(ctx, p, app.ID); err != nil {
		t.Fatal(err)
	}
	return app
}

func status(t *testing.T, env *testutil.Env, id string) domain.Application {
	t.Helper()
	a, err := env.Store.GetApplication(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func eventPath(t *testing.T, env *testutil.Env, id string) []domain.Status {
	t.Helper()
	evs, err := env.Store.ListEvents(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var out []domain.Status
	for _, e := range evs {
		if e.From != e.To {
			out = append(out, e.To)
		}
	}
	return out
}

func assertPath(t *testing.T, got []domain.Status, want ...domain.Status) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("path %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("path %v, want %v", got, want)
		}
	}
}

func TestE2E_ApprovedSignedIssued(t *testing.T) {
	env := testutil.Setup(t)
	p := login(t, env, "approve@demo.ru")
	app := apply(t, env, p, "EXPRESS", domain.PurposeWorkingCapital, 2_000_000, 24)
	env.Drain(t)

	app = status(t, env, app.ID)
	if app.Status != domain.StatusApproved {
		t.Fatalf("want APPROVED, got %s; decision %+v score %+v", app.Status, app.Decision, app.Score)
	}
	if app.Score == nil || app.Score.Grade != "A" || app.Decision.Offer.Amount != 2_000_000 {
		t.Fatalf("unexpected score/decision: %+v %+v", app.Score, app.Decision)
	}
	details, err := env.Service.GetDetails(ctx, p, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(details.Checks) != 3 || len(details.Schedule) != 24 || !contains(details.Actions, "sign") {
		t.Fatalf("details incomplete: checks=%d schedule=%d actions=%v", len(details.Checks), len(details.Schedule), details.Actions)
	}
	if details.Checks[0].Payload != nil {
		t.Fatal("client must not see raw source payloads")
	}

	// Подписание: неверный код отклоняется, верный — переводит в SIGNED, затем выдача.
	ch, err := env.Service.RequestSignCode(ctx, p, app.ID)
	if err != nil || len(ch.DemoCode) != 6 {
		t.Fatalf("sign challenge: %+v %v", ch, err)
	}
	var ve *domain.ValidationError
	if _, err := env.Service.Sign(ctx, p, app.ID, "000000x"); !errors.As(err, &ve) {
		t.Fatalf("wrong code must be rejected: %v", err)
	}
	if _, err := env.Service.Sign(ctx, p, app.ID, ch.DemoCode); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Service.Sign(ctx, p, app.ID, ch.DemoCode); err == nil {
		t.Fatal("sign code must be single-use")
	}
	env.Drain(t)
	assertPath(t, eventPath(t, env, app.ID),
		domain.StatusDraft, domain.StatusSubmitted, domain.StatusChecking, domain.StatusScoring,
		domain.StatusApproved, domain.StatusSigned, domain.StatusIssued)

	// Лента отражает выдачу кредита.
	feed, err := env.Service.GetFeed(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if feed.Profile.Stage != "issued" || feed.Items[0].Type != "event" {
		t.Fatalf("feed must react to issued loan: %+v", feed.Profile)
	}
	if !feedHas(feed, "n-stage-issued-repay") || feedHas(feed, "n-stage-none-prepare") {
		t.Fatal("feed content must change with stage")
	}
}

func TestE2E_CounterOfferExtendsTerm(t *testing.T) {
	env := testutil.Setup(t)
	p := login(t, env, "counter@demo.ru")
	app := apply(t, env, p, "EXPRESS", domain.PurposeWorkingCapital, 3_000_000, 12)
	env.Drain(t)
	app = status(t, env, app.ID)
	if app.Status != domain.StatusCounterOffer {
		t.Fatalf("want COUNTER_OFFER, got %s %+v", app.Status, app.Decision)
	}
	o := app.Decision.Offer
	if o.Amount != 3_000_000 || o.TermMonths <= 12 {
		t.Fatalf("counter offer must keep amount and extend term: %+v", o)
	}
	if _, err := env.Service.Decline(ctx, p, app.ID, "Нужен короткий срок"); err != nil {
		t.Fatal(err)
	}
	if status(t, env, app.ID).Status != domain.StatusDeclined {
		t.Fatal("decline must be terminal DECLINED")
	}
}

func TestE2E_RejectedByStopFactor(t *testing.T) {
	env := testutil.Setup(t)
	for _, email := range []string{"reject@demo.ru", "liquidated@demo.ru"} {
		p := login(t, env, email)
		app := apply(t, env, p, "EXPRESS", domain.PurposeWorkingCapital, 500_000, 12)
		env.Drain(t)
		app = status(t, env, app.ID)
		if app.Status != domain.StatusRejected || len(app.Score.StopFactors) == 0 || len(app.Decision.Reasons) == 0 {
			t.Fatalf("%s: want REJECTED with stop factor, got %s %+v", email, app.Status, app.Score)
		}
	}
}

func TestE2E_NeedsInfoThenManualReview(t *testing.T) {
	env := testutil.Setup(t)
	p := login(t, env, "review@demo.ru")
	app := apply(t, env, p, "EXPRESS", domain.PurposeWorkingCapital, 1_000_000, 24)
	env.Drain(t)

	app = status(t, env, app.ID)
	if app.Status != domain.StatusNeedsInfo || len(app.MissingDocs()) != 1 || app.MissingDocs()[0].Kind != domain.DocBankStatement {
		t.Fatalf("young business must be asked for a bank statement: %s %+v", app.Status, app.RequiredDocs)
	}
	// Без документа повторно отправить нельзя.
	var ve *domain.ValidationError
	if _, err := env.Service.Submit(ctx, p, app.ID); !errors.As(err, &ve) {
		t.Fatalf("submit without docs must fail validation: %v", err)
	}
	// Не-PDF отклоняется по содержимому, а не по имени файла.
	if _, err := env.Service.UploadDocument(ctx, p, app.ID, domain.DocBankStatement, "statement.pdf", []byte("plain text")); !errors.As(err, &ve) {
		t.Fatalf("non-pdf content must be rejected: %v", err)
	}
	if _, err := env.Service.UploadDocument(ctx, p, app.ID, domain.DocBankStatement, "../../statement.pdf", fakePDF()); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Service.Submit(ctx, p, app.ID); err != nil {
		t.Fatal(err)
	}
	env.Drain(t)
	app = status(t, env, app.ID)
	if app.Status != domain.StatusManualReview || app.Score.Grade != "D" || app.Decision.Offer == nil {
		t.Fatalf("want MANUAL_REVIEW grade D with preliminary offer, got %s %+v", app.Status, app.Score)
	}

	// Андеррайтер: клиент не может решать, андеррайтер — может.
	uw := login(t, env, "underwriter@demo.ru")
	if _, err := env.Service.Decide(ctx, p, app.ID, service.UnderwriterDecision{Action: "approve"}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("client must not decide: %v", err)
	}
	queue, _, err := env.Service.List(ctx, uw, store.ListFilter{Statuses: []domain.Status{domain.StatusManualReview}})
	if err != nil || len(queue) != 1 {
		t.Fatalf("underwriter queue: %d %v", len(queue), err)
	}
	if _, err := env.Service.Take(ctx, uw, app.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Service.Decide(ctx, uw, app.ID, service.UnderwriterDecision{Action: "reject"}); !errors.As(err, &ve) {
		t.Fatal("reject without reason must fail validation")
	}
	app, err = env.Service.Decide(ctx, uw, app.ID, service.UnderwriterDecision{Action: "approve", Amount: 800_000, TermMonths: 24, Comment: "Одобрено с учётом оборотов по выписке"})
	if err != nil {
		t.Fatal(err)
	}
	if app.Status != domain.StatusCounterOffer || app.Decision.DecidedBy != uw.UserID || app.Decision.Offer.Amount != 800_000 {
		t.Fatalf("underwriter counter-offer not applied: %s %+v", app.Status, app.Decision)
	}
	stats, err := env.Service.GetStats(ctx, uw)
	if err != nil || stats.Total == 0 {
		t.Fatalf("stats: %+v %v", stats, err)
	}
}

func TestE2E_LargeAmountRequiresStatementsAndManualDecision(t *testing.T) {
	env := testutil.Setup(t)
	p := login(t, env, "large@demo.ru")
	app := apply(t, env, p, "WORKING_CAPITAL", domain.PurposeWorkingCapital, 15_000_000, 24)
	env.Drain(t)
	app = status(t, env, app.ID)
	if app.Status != domain.StatusNeedsInfo || app.MissingDocs()[0].Kind != domain.DocFinancialStatements {
		t.Fatalf("want financial statements request, got %s %+v", app.Status, app.RequiredDocs)
	}
	if _, err := env.Service.UploadDocument(ctx, p, app.ID, domain.DocFinancialStatements, "otchet.pdf", fakePDF()); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Service.Submit(ctx, p, app.ID); err != nil {
		t.Fatal(err)
	}
	env.Drain(t)
	app = status(t, env, app.ID)
	if app.Status != domain.StatusManualReview || !contains(app.Decision.ReasonCodes, "OVER_AUTO_LIMIT") {
		t.Fatalf("want manual review over auto limit, got %s %+v", app.Status, app.Decision)
	}
}

func TestAccessControl_ClientCannotSeeForeignApplication(t *testing.T) {
	env := testutil.Setup(t)
	owner := login(t, env, "approve@demo.ru")
	other := login(t, env, "counter@demo.ru")
	app, err := env.Service.CreateDraft(ctx, owner, domain.ApplicationInput{ProductCode: "EXPRESS"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.Service.GetDetails(ctx, other, app.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("foreign application must look non-existent, got %v", err)
	}
	if _, err := env.Service.Submit(ctx, other, app.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("foreign submit must fail, got %v", err)
	}
	if _, err := env.Service.UploadDocument(ctx, other, app.ID, domain.DocOther, "x.pdf", fakePDF()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("foreign upload must fail, got %v", err)
	}
	list, _, _ := env.Service.List(ctx, other, store.ListFilter{})
	if len(list) != 0 {
		t.Fatal("client list must contain only own company applications")
	}
}

func TestOptimisticLocking(t *testing.T) {
	env := testutil.Setup(t)
	p := login(t, env, "approve@demo.ru")
	app, _ := env.Service.CreateDraft(ctx, p, domain.ApplicationInput{ProductCode: "EXPRESS"})
	in := domain.ApplicationInput{ProductCode: "EXPRESS", Amount: 1_000_000, TermMonths: 12}
	if _, err := env.Service.UpdateDraft(ctx, p, app.ID, app.Version, in); err != nil {
		t.Fatal(err)
	}
	// Вторая вкладка со старой версией.
	if _, err := env.Service.UpdateDraft(ctx, p, app.ID, app.Version, in); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale version must conflict, got %v", err)
	}
}

func TestPipeline_RetriesWhenSourceIsDown(t *testing.T) {
	env := testutil.Setup(t)
	p := login(t, env, "approve@demo.ru")
	app := apply(t, env, p, "EXPRESS", domain.PurposeWorkingCapital, 1_000_000, 12)

	env.Faults.FailureRate = 1 // все источники отвечают 503
	env.Service.RunOnce(ctx)
	app = status(t, env, app.ID)
	if app.Status != domain.StatusChecking {
		t.Fatalf("application must wait in CHECKING while sources are down, got %s", app.Status)
	}
	qs, _ := env.Store.TaskStats(ctx)
	if qs.Pending != 1 || qs.Retries != 1 {
		t.Fatalf("task must be scheduled for retry: %+v", qs)
	}

	// Клиент видит понятное объяснение задержки, андеррайтер — техническую причину.
	d, err := env.Service.GetDetails(ctx, p, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Pipeline == nil || d.Pipeline.State != "retrying" || d.Pipeline.TechnicalError != "" ||
		!strings.Contains(d.Pipeline.Message, "ЕГРЮЛ/ЕГРИП, ФНС, бюро кредитных историй") {
		t.Fatalf("client must see retry notice naming all sources: %+v", d.Pipeline)
	}
	uw := login(t, env, "underwriter@demo.ru")
	if d, _ := env.Service.GetDetails(ctx, uw, app.ID); d.Pipeline == nil || d.Pipeline.TechnicalError == "" {
		t.Fatalf("underwriter must see technical error: %+v", d.Pipeline)
	}

	env.Faults.FailureRate = 0
	if _, err := env.Store.DB.ExecContext(ctx, `UPDATE tasks SET run_at = now() WHERE status='pending'`); err != nil {
		t.Fatal(err)
	}
	env.Drain(t)
	if s := status(t, env, app.ID).Status; s != domain.StatusApproved {
		t.Fatalf("after recovery application must be decided, got %s", s)
	}
	if d, _ := env.Service.GetDetails(ctx, p, app.ID); d.Pipeline != nil {
		t.Fatalf("notice must disappear after recovery: %+v", d.Pipeline)
	}
}

// Повторный старт (сиды) не должен зависеть от доступности внешних источников.
func TestSeedIsIndependentOfSourcesOnRestart(t *testing.T) {
	env := testutil.Setup(t) // первый сид прошёл при работающих источниках
	env.Faults.FailureRate = 1
	if err := env.Service.Seed(ctx); err != nil {
		t.Fatalf("re-seed with sources down must succeed: %v", err)
	}
}

func TestInvalidTransitionsAreRejected(t *testing.T) {
	env := testutil.Setup(t)
	p := login(t, env, "approve@demo.ru")
	app := apply(t, env, p, "EXPRESS", domain.PurposeWorkingCapital, 1_000_000, 12)
	// Отправленную заявку нельзя отправить ещё раз или подписать до решения.
	if _, err := env.Service.Submit(ctx, p, app.ID); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("double submit: %v", err)
	}
	if _, err := env.Service.RequestSignCode(ctx, p, app.ID); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("sign before decision: %v", err)
	}
}

func TestRegisterWithINNLookup(t *testing.T) {
	env := testutil.Setup(t)
	sess, err := env.Service.Register(ctx, service.RegisterInput{Email: "new@client.ru", Password: "secret123", FullName: "Новый Клиент", INN: "7728168971"})
	if err != nil {
		t.Fatal(err)
	}
	if sess.Company == nil || sess.Company.Name == "" || sess.Company.OKVED == "" {
		t.Fatalf("company must be filled from registry: %+v", sess.Company)
	}
	var ve *domain.ValidationError
	if _, err := env.Service.Register(ctx, service.RegisterInput{Email: "new@client.ru", Password: "secret123", FullName: "X", INN: "7728168971"}); !errors.As(err, &ve) {
		t.Fatalf("duplicate email must be validation error: %v", err)
	}
	if _, err := env.Service.Register(ctx, service.RegisterInput{Email: "a@b.ru", Password: "secret123", FullName: "X", INN: "1234567890"}); !errors.As(err, &ve) {
		t.Fatalf("invalid INN must be rejected: %v", err)
	}
	if _, err := env.Service.Login(ctx, "new@client.ru", "wrong-password"); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatal("wrong password must be unauthorized")
	}
}

func fakePDF() []byte {
	return []byte("%PDF-1.4\n1 0 obj << /Type /Catalog >> endobj\ntrailer << >>\n%%EOF")
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func feedHas(f service.Feed, id string) bool {
	for _, it := range f.Items {
		if it.ID == id {
			return true
		}
	}
	return false
}
