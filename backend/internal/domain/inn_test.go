package domain

import "testing"

func TestValidINN(t *testing.T) {
	valid := []string{"7728168971", "7707083893", "500100732259"} // Альфа-Банк, Сбербанк, известный тестовый ИНН ИП
	for _, inn := range valid {
		if !ValidINN(inn) {
			t.Errorf("%s should be valid", inn)
		}
	}
	invalid := []string{"", "123", "7728168972", "50010073225a", "500100732258", "77281689711"}
	for _, inn := range invalid {
		if ValidINN(inn) {
			t.Errorf("%s should be invalid", inn)
		}
	}
}

func TestCompleteINN(t *testing.T) {
	if got := CompleteINN("772816897"); got != "7728168971" {
		t.Fatalf("got %s", got)
	}
	if got := CompleteINN("5001007322"); got != "500100732259" {
		t.Fatalf("got %s", got)
	}
	if INNKind("500100732259") != CompanyIndividual || INNKind("7728168971") != CompanyLegal {
		t.Fatal("kind by length")
	}
}
