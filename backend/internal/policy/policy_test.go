package policy

import (
	"testing"
	"time"

	"alfahack/backend/internal/domain"
)

var now = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

func express() domain.Product { p, _ := domain.ProductByCode("EXPRESS"); return p }

func req(amount int64, term int, revenue int64, grade string) Request {
	return Request{
		Product: express(), Amount: amount, TermMonths: term,
		Financials: domain.Financials{AnnualRevenue: revenue},
		Score:      domain.ScoreResult{Grade: grade, Score: 680},
	}
}

func TestAnnuity(t *testing.T) {
	// 1 000 000 ₽, 12% годовых, 12 мес. → 88 849 ₽ (классический пример)
	if got := Annuity(1_000_000, 12, 12); got != 88_849 {
		t.Fatalf("annuity %d, want 88849", got)
	}
	// MaxPrincipal — обратная функция
	if p := MaxPrincipal(88_849, 12, 12); p < 999_990 || p > 1_000_010 {
		t.Fatalf("max principal %d", p)
	}
	if Annuity(1200, 0, 12) != 100 {
		t.Fatal("zero rate annuity")
	}
}

func TestSchedule_RepaysPrincipal(t *testing.T) {
	o := MakeOffer(express(), 1_000_000, 12, 21.5)
	s := Schedule(*o)
	if len(s) != 12 {
		t.Fatalf("schedule length %d", len(s))
	}
	var principal int64
	for _, it := range s {
		principal += it.Principal
	}
	if principal != 1_000_000 || s[11].Balance != 0 {
		t.Fatalf("schedule must repay principal exactly: %d, last balance %d", principal, s[11].Balance)
	}
}

func TestDecide_Approved(t *testing.T) {
	d := Decide(req(1_000_000, 24, 60_000_000, "A"), now)
	if d.Outcome != domain.OutcomeApproved {
		t.Fatalf("want approved, got %s %v", d.Outcome, d.Reasons)
	}
	if d.Offer == nil || d.Offer.Amount != 1_000_000 || d.Offer.TermMonths != 24 || d.Offer.Rate != 21.5 {
		t.Fatalf("unexpected offer %+v", d.Offer)
	}
	if d.PolicyVersion != Version || d.DecidedBy != "system" {
		t.Fatal("decision must record policy version and author")
	}
}

func TestDecide_RateDependsOnGrade(t *testing.T) {
	a := Decide(req(1_000_000, 24, 60_000_000, "A"), now)
	c := Decide(req(1_000_000, 24, 60_000_000, "C"), now)
	if !(c.Offer.Rate > a.Offer.Rate) {
		t.Fatalf("riskier grade must get higher rate: A=%v C=%v", a.Offer.Rate, c.Offer.Rate)
	}
}

func TestDecide_CounterOfferExtendsTerm(t *testing.T) {
	// Выручка 12 млн/год → 1 млн/мес, лимит 25% → 250k платёж.
	// 3 млн на 12 мес. ≈ 280k — не проходит, на большем сроке — проходит.
	d := Decide(req(3_000_000, 12, 12_000_000, "B"), now)
	if d.Outcome != domain.OutcomeCounterOffer || d.ReasonCodes[0] != ReasonTermExtended {
		t.Fatalf("want counter offer with extended term, got %s %v", d.Outcome, d.ReasonCodes)
	}
	if d.Offer.Amount != 3_000_000 || d.Offer.TermMonths <= 12 || d.Offer.MonthlyPayment > 250_000 {
		t.Fatalf("unexpected offer %+v", d.Offer)
	}
}

func TestDecide_CounterOfferReducesAmount(t *testing.T) {
	// 1 млн/мес выручки, лимит 250k; 5 млн не влезает даже на 36 мес.
	d := Decide(req(5_000_000, 36, 6_000_000, "B"), now)
	if d.Outcome != domain.OutcomeCounterOffer || d.ReasonCodes[0] != ReasonAmountReduced {
		t.Fatalf("want reduced amount, got %s %v", d.Outcome, d.ReasonCodes)
	}
	if d.Offer.Amount >= 5_000_000 || d.Offer.Amount%10_000 != 0 {
		t.Fatalf("unexpected offer %+v", d.Offer)
	}
	if d.Offer.MonthlyPayment > 125_000 {
		t.Fatalf("offer payment %d exceeds capacity", d.Offer.MonthlyPayment)
	}
}

func TestDecide_RejectNoCapacity(t *testing.T) {
	r := req(1_000_000, 12, 6_000_000, "B")
	r.ExistingPayments = 200_000 // вся допустимая нагрузка уже занята
	d := Decide(r, now)
	if d.Outcome != domain.OutcomeRejected || d.ReasonCodes[0] != ReasonNoCapacity {
		t.Fatalf("want reject no capacity, got %s %v", d.Outcome, d.ReasonCodes)
	}
	if len(d.Suggestions) == 0 {
		t.Fatal("rejection must contain suggestions for the client")
	}
}

func TestDecide_StopFactorAndGradeE(t *testing.T) {
	r := req(1_000_000, 12, 60_000_000, "A")
	r.Score.StopFactors = []string{"STOP_BANKRUPTCY"}
	if d := Decide(r, now); d.Outcome != domain.OutcomeRejected || d.Offer != nil {
		t.Fatalf("stop factor must reject without offer: %+v", d)
	}
	r = req(1_000_000, 12, 60_000_000, "E")
	r.Score.Factors = []domain.ScoreFactor{{Title: "Кредитная история", Value: "просрочка", Points: -90}}
	d := Decide(r, now)
	if d.Outcome != domain.OutcomeRejected || len(d.Reasons) < 2 {
		t.Fatalf("grade E must reject with explained drivers: %+v", d)
	}
}

func TestDecide_ManualReview(t *testing.T) {
	// Пограничный грейд
	d := Decide(req(1_000_000, 24, 60_000_000, "D"), now)
	if d.Outcome != domain.OutcomeManualReview || d.Offer == nil {
		t.Fatalf("grade D → manual review with preliminary offer, got %+v", d)
	}
	// Превышение лимита автоматического решения
	wc, _ := domain.ProductByCode("WORKING_CAPITAL")
	r := Request{Product: wc, Amount: 20_000_000, TermMonths: 36,
		Financials: domain.Financials{AnnualRevenue: 300_000_000}, Score: domain.ScoreResult{Grade: "A"}}
	d = Decide(r, now)
	if d.Outcome != domain.OutcomeManualReview || d.ReasonCodes[0] != ReasonOverAutoLimit {
		t.Fatalf("over auto limit → manual review, got %s %v", d.Outcome, d.ReasonCodes)
	}
	// Недостаточный залог по инвесткредиту
	inv, _ := domain.ProductByCode("INVESTMENT")
	r = Request{Product: inv, Amount: 5_000_000, TermMonths: 60,
		Financials: domain.Financials{AnnualRevenue: 100_000_000, CollateralValue: 1_000_000}, Score: domain.ScoreResult{Grade: "A"}}
	if d = Decide(r, now); d.Outcome != domain.OutcomeManualReview {
		t.Fatalf("low collateral → manual review, got %s", d.Outcome)
	}
}

func TestDecide_OutcomesMatchStateMachine(t *testing.T) {
	for _, g := range []string{"A", "B", "C", "D", "E"} {
		d := Decide(req(1_000_000, 24, 60_000_000, g), now)
		if !domain.CanTransition(domain.StatusScoring, d.Outcome.TargetStatus(), domain.ActorSystem) {
			t.Errorf("grade %s: outcome %s is not a valid transition from SCORING", g, d.Outcome)
		}
	}
}
