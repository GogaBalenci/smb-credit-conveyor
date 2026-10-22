package domain

import (
	"errors"
	"testing"
	"time"
)

func TestCanTransition_HappyPath(t *testing.T) {
	path := []struct {
		from, to Status
		actor    ActorKind
	}{
		{StatusDraft, StatusSubmitted, ActorClient},
		{StatusSubmitted, StatusChecking, ActorSystem},
		{StatusChecking, StatusScoring, ActorSystem},
		{StatusScoring, StatusApproved, ActorSystem},
		{StatusApproved, StatusSigned, ActorClient},
		{StatusSigned, StatusIssued, ActorSystem},
	}
	for _, p := range path {
		if !CanTransition(p.from, p.to, p.actor) {
			t.Errorf("expected %s → %s by %s to be allowed", p.from, p.to, p.actor)
		}
	}
}

func TestCanTransition_ForbidsWrongActor(t *testing.T) {
	cases := []struct {
		from, to Status
		actor    ActorKind
	}{
		{StatusScoring, StatusApproved, ActorClient},      // клиент не может одобрить сам себе
		{StatusManualReview, StatusApproved, ActorSystem}, // ручное решение — только андеррайтер
		{StatusApproved, StatusSigned, ActorUnderwriter},  // подписывает только клиент
		{StatusDraft, StatusApproved, ActorUnderwriter},   // нельзя перепрыгнуть этапы
		{StatusIssued, StatusCancelled, ActorClient},      // из терминального статуса выхода нет
		{StatusRejected, StatusSubmitted, ActorClient},    // отказ окончателен
	}
	for _, c := range cases {
		if CanTransition(c.from, c.to, c.actor) {
			t.Errorf("expected %s → %s by %s to be forbidden", c.from, c.to, c.actor)
		}
		if err := ValidateTransition(c.from, c.to, c.actor); !errors.Is(err, ErrInvalidTransition) {
			t.Errorf("ValidateTransition: want ErrInvalidTransition, got %v", err)
		}
	}
}

func TestStateMachine_Consistency(t *testing.T) {
	// Каждый статус имеет описание для клиента.
	for _, s := range AllStatuses {
		if s.Info().Title == "" {
			t.Errorf("status %s has no client description", s)
		}
	}
	// Терминальные статусы — ровно ожидаемые.
	terminal := map[Status]bool{StatusRejected: true, StatusIssued: true, StatusDeclined: true, StatusCancelled: true}
	for _, s := range AllStatuses {
		if s.IsTerminal() != terminal[s] {
			t.Errorf("status %s: IsTerminal=%v, want %v", s, s.IsTerminal(), terminal[s])
		}
	}
	// Все статусы достижимы из DRAFT.
	reach := map[Status]bool{StatusDraft: true}
	for changed := true; changed; {
		changed = false
		for _, tr := range Transitions() {
			if reach[tr.From] && !reach[tr.To] {
				reach[tr.To] = true
				changed = true
			}
		}
	}
	for _, s := range AllStatuses {
		if !reach[s] {
			t.Errorf("status %s is unreachable from DRAFT", s)
		}
	}
}

func TestOutcomeTargetStatus(t *testing.T) {
	for o, want := range map[Outcome]Status{
		OutcomeApproved: StatusApproved, OutcomeCounterOffer: StatusCounterOffer,
		OutcomeManualReview: StatusManualReview, OutcomeNeedsInfo: StatusNeedsInfo, OutcomeRejected: StatusRejected,
	} {
		if got := o.TargetStatus(); got != want {
			t.Errorf("%s: got %s want %s", o, got, want)
		}
		// решение системы из SCORING всегда допустимо, кроме needs_info (он из CHECKING)
		if o != OutcomeNeedsInfo && !CanTransition(StatusScoring, want, ActorSystem) {
			t.Errorf("scoring cannot lead to %s", want)
		}
	}
}

func TestApplicationInputValidate(t *testing.T) {
	ok := ApplicationInput{
		ProductCode: "EXPRESS", Purpose: PurposeWorkingCapital, Amount: 1_000_000, TermMonths: 12,
		Financials: Financials{AnnualRevenue: 20_000_000},
	}
	if err := ok.Validate(true); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}

	bad := ok
	bad.Amount = 50_000_000
	bad.TermMonths = 100
	err := bad.Validate(true)
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Fields["amount"] == "" || ve.Fields["term_months"] == "" {
		t.Fatalf("expected amount and term errors, got %v", err)
	}

	// Черновик можно сохранить пустым.
	if err := (ApplicationInput{ProductCode: "EXPRESS"}).Validate(false); err != nil {
		t.Fatalf("empty draft should be valid: %v", err)
	}

	// Инвесткредит требует залог.
	inv := ok
	inv.ProductCode = "INVESTMENT"
	inv.Purpose = PurposeEquipment
	inv.Amount = 3_000_000
	err = inv.Validate(true)
	if !errors.As(err, &ve) || ve.Fields["financials.collateral_value"] == "" {
		t.Fatalf("expected collateral error, got %v", err)
	}
}

func TestCompanyAgeMonths(t *testing.T) {
	c := Company{RegisteredAt: time.Date(2024, 3, 15, 0, 0, 0, 0, time.UTC)}
	if got := c.AgeMonths(time.Date(2026, 3, 14, 0, 0, 0, 0, time.UTC)); got != 23 {
		t.Errorf("got %d want 23", got)
	}
	if got := c.AgeMonths(time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)); got != 24 {
		t.Errorf("got %d want 24", got)
	}
}

func TestFormatRub(t *testing.T) {
	for in, want := range map[int64]string{0: "0 ₽", 999: "999 ₽", 1000: "1 000 ₽", 1500000: "1 500 000 ₽", -2500: "−2 500 ₽"} {
		if got := FormatRub(in); got != want {
			t.Errorf("FormatRub(%d)=%q want %q", in, got, want)
		}
	}
}

func TestDocs(t *testing.T) {
	a := &Application{RequiredDocs: []DocRequest{{Kind: DocBankStatement}, {Kind: DocFinancialStatements}}}
	a.MarkDocProvided(DocBankStatement)
	if m := a.MissingDocs(); len(m) != 1 || m[0].Kind != DocFinancialStatements {
		t.Fatalf("unexpected missing docs: %+v", m)
	}
}
