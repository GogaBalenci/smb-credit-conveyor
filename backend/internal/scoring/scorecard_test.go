package scoring

import (
	"math"
	"testing"

	"alfahack/backend/internal/domain"
)

func goodInput() Input {
	return Input{
		RegistryActive:   true,
		CompanyAgeMonths: 72,
		OKVEDSection:     "62",
		Financials: domain.Financials{
			AnnualRevenue: 60_000_000, PrevAnnualRevenue: 50_000_000, AnnualNetProfit: 7_200_000,
		},
		Amount:           2_000_000,
		RequestedPayment: 100_000,
		Credit:           CreditHistory{Found: true, ClosedLoans: 3, ActiveLoans: 1, MonthlyPayments: 150_000},
	}
}

func factor(r domain.ScoreResult, code string) (domain.ScoreFactor, bool) {
	for _, f := range r.Factors {
		if f.Code == code {
			return f, true
		}
	}
	return domain.ScoreFactor{}, false
}

func TestScore_GoodBorrowerIsGradeA(t *testing.T) {
	r := Score(goodInput())
	if r.Grade != "A" {
		t.Fatalf("want grade A, got %s (score %d, factors %+v)", r.Grade, r.Score, r.Factors)
	}
	if len(r.StopFactors) != 0 {
		t.Fatalf("unexpected stop factors %v", r.StopFactors)
	}
	if r.ModelVersion != ModelVersion {
		t.Fatal("model version must be recorded")
	}
}

func TestScore_IsSumOfFactors(t *testing.T) {
	r := Score(goodInput())
	sum := baseScore
	for _, f := range r.Factors {
		sum += f.Points
	}
	if sum != r.Score {
		t.Fatalf("score %d != base + factors %d — модель должна быть объяснимой", r.Score, sum)
	}
}

func TestScore_StopFactors(t *testing.T) {
	cases := map[string]func(*Input){
		StopRegistryInactive:     func(in *Input) { in.RegistryActive = false },
		StopBankruptcy:           func(in *Input) { in.Tax.Bankruptcy = true },
		StopDisqualifiedDirector: func(in *Input) { in.Tax.DisqualifiedDirector = true },
		StopSevereOverdue:        func(in *Input) { in.Credit.MaxOverdueDays = 120 },
		StopTooYoung:             func(in *Input) { in.CompanyAgeMonths = 1 },
	}
	for code, mutate := range cases {
		in := goodInput()
		mutate(&in)
		r := Score(in)
		found := false
		for _, s := range r.StopFactors {
			found = found || s == code
		}
		if !found {
			t.Errorf("%s: stop factor not raised, got %v", code, r.StopFactors)
		}
		if StopFactorText(code) == code {
			t.Errorf("%s: no client-facing explanation", code)
		}
	}
}

func TestScore_MonotonicRisk(t *testing.T) {
	base := Score(goodInput()).Score

	// Ухудшение любого признака не должно повышать балл.
	worse := map[string]func(*Input){
		"overdue":   func(in *Input) { in.Credit.MaxOverdueDays = 45 },
		"debt":      func(in *Input) { in.Credit.MonthlyPayments = 1_500_000 },
		"loss":      func(in *Input) { in.Financials.AnnualNetProfit = -1_000_000 },
		"decline":   func(in *Input) { in.Financials.PrevAnnualRevenue = 100_000_000 },
		"tax":       func(in *Input) { in.Tax.TaxArrears = 300_000 },
		"mass":      func(in *Input) { in.Tax.MassAddress = true },
		"young":     func(in *Input) { in.CompanyAgeMonths = 8 },
		"inquiries": func(in *Input) { in.Credit.Inquiries30d = 6 },
	}
	for name, mutate := range worse {
		in := goodInput()
		mutate(&in)
		if s := Score(in).Score; s >= base {
			t.Errorf("%s: score %d should be lower than %d", name, s, base)
		}
	}
}

func TestDebtLoad(t *testing.T) {
	in := goodInput() // выручка 5 млн/мес, платежи 150k + 100k
	if got := in.DebtLoad(); math.Abs(got-0.05) > 1e-9 {
		t.Fatalf("debt load %v, want 0.05", got)
	}
	in.Financials.MonthlyObligations = 400_000 // заявлено больше, чем в БКИ — берём больше
	if in.ExistingPayments() != 400_000 {
		t.Fatal("existing payments must be max of declared and BKI")
	}
	in.Financials.AnnualRevenue = 0
	if !math.IsInf(in.DebtLoad(), 1) {
		t.Fatal("zero revenue must produce infinite debt load")
	}
	if f, _ := factor(Score(in), "DEBT_LOAD"); f.Points != -90 {
		t.Fatalf("zero revenue debt load points %d", f.Points)
	}
}

func TestGradeAndPD(t *testing.T) {
	for score, grade := range map[int]string{720: "A", 700: "A", 660: "B", 610: "C", 560: "D", 540: "E"} {
		if g := Grade(score); g != grade {
			t.Errorf("Grade(%d)=%s want %s", score, g, grade)
		}
	}
	if !(PD(500) > PD(600) && PD(600) > PD(700)) {
		t.Fatal("PD must decrease with score")
	}
}
