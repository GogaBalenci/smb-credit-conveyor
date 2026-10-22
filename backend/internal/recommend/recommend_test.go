package recommend

import (
	"testing"

	"alfahack/backend/internal/domain"
)

func TestRecommend_CashGapPrefersOverdraft(t *testing.T) {
	recs := Recommend(Need{Purpose: domain.PurposeCashGap, Amount: 1_000_000, TermMonths: 6, Urgent: true})
	if len(recs) == 0 || recs[0].Product.Code != "OVERDRAFT" || !recs[0].Best {
		t.Fatalf("cash gap should recommend OVERDRAFT first, got %+v", codes(recs))
	}
}

func TestRecommend_EquipmentWithCollateralPrefersInvestment(t *testing.T) {
	recs := Recommend(Need{Purpose: domain.PurposeEquipment, Amount: 8_000_000, TermMonths: 48, HasCollateral: true})
	if recs[0].Product.Code != "INVESTMENT" {
		t.Fatalf("got %v", codes(recs))
	}
}

func TestRecommend_EquipmentWithoutCollateralSmallAmount(t *testing.T) {
	recs := Recommend(Need{Purpose: domain.PurposeEquipment, Amount: 1_500_000, TermMonths: 24})
	if recs[0].Product.Code != "EXPRESS" {
		t.Fatalf("small equipment purchase without collateral → EXPRESS, got %v", codes(recs))
	}
}

func TestRecommend_TenderOnlyTender(t *testing.T) {
	recs := Recommend(Need{Purpose: domain.PurposeTender, Amount: 3_000_000, TermMonths: 12})
	if len(recs) != 1 || recs[0].Product.Code != "TENDER" {
		t.Fatalf("got %v", codes(recs))
	}
}

func TestRecommend_OutOfRangeIsWarnedAndClamped(t *testing.T) {
	recs := Recommend(Need{Purpose: domain.PurposeCashGap, Amount: 50_000_000, TermMonths: 6})
	for _, r := range recs {
		if r.SuggestedAmount > r.Product.MaxAmount {
			t.Errorf("%s: suggested amount above max", r.Product.Code)
		}
		if len(r.Warnings) == 0 {
			t.Errorf("%s: expected a warning about amount", r.Product.Code)
		}
		if r.Best {
			t.Errorf("%s: product with warnings cannot be marked best", r.Product.Code)
		}
	}
}

func TestRecommend_EveryResultHasPaymentEstimate(t *testing.T) {
	for _, p := range domain.Purposes {
		for _, r := range Recommend(Need{Purpose: p.Code}) {
			if r.EstimatedPayment <= 0 || r.SuggestedTerm == 0 {
				t.Errorf("%s/%s: missing estimate %+v", p.Code, r.Product.Code, r)
			}
		}
	}
}

func codes(recs []Recommendation) []string {
	var out []string
	for _, r := range recs {
		out = append(out, r.Product.Code)
	}
	return out
}
