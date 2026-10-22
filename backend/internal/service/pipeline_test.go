package service

import (
	"strings"
	"testing"
	"time"

	"alfahack/backend/internal/domain"
	"alfahack/backend/internal/store"
)

func TestRequiredDocuments(t *testing.T) {
	now := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	mature := domain.Company{RegisteredAt: now.AddDate(-5, 0, 0)}
	young := domain.Company{RegisteredAt: now.AddDate(0, -8, 0)}

	kinds := func(d []domain.DocRequest) map[domain.DocKind]bool {
		m := map[domain.DocKind]bool{}
		for _, x := range d {
			m[x.Kind] = true
			if x.Reason == "" {
				t.Errorf("%s: every request must explain why", x.Kind)
			}
		}
		return m
	}

	if d := RequiredDocuments(domain.Application{ProductCode: "EXPRESS", Amount: 1_000_000}, mature, now); len(d) != 0 {
		t.Fatalf("mature company, small amount — no docs needed, got %+v", d)
	}
	if k := kinds(RequiredDocuments(domain.Application{ProductCode: "EXPRESS", Amount: 1_000_000}, young, now)); !k[domain.DocBankStatement] {
		t.Fatal("young business needs bank statement")
	}
	if k := kinds(RequiredDocuments(domain.Application{ProductCode: "WORKING_CAPITAL", Amount: 6_000_000}, mature, now)); !k[domain.DocFinancialStatements] {
		t.Fatal("large amount needs financial statements")
	}
	if k := kinds(RequiredDocuments(domain.Application{ProductCode: "TENDER", Amount: 1_000_000}, mature, now)); !k[domain.DocContract] {
		t.Fatal("tender loan needs contract")
	}
	if k := kinds(RequiredDocuments(domain.Application{ProductCode: "INVESTMENT", Amount: 2_000_000}, mature, now)); !k[domain.DocCollateral] {
		t.Fatal("investment loan needs collateral docs")
	}
}

func TestMergeDocs_KeepsUnderwriterRequestsAndMarksUploads(t *testing.T) {
	existing := []domain.DocRequest{{Kind: domain.DocOther, Reason: "от андеррайтера"}}
	required := []domain.DocRequest{{Kind: domain.DocBankStatement, Reason: "правило"}, {Kind: domain.DocOther, Reason: "дубль"}}
	got := mergeDocs(existing, required, map[domain.DocKind]bool{domain.DocBankStatement: true})
	if len(got) != 2 || got[0].Reason != "от андеррайтера" || !got[1].Provided || got[0].Provided {
		t.Fatalf("unexpected merge %+v", got)
	}
}

func TestAllowedActions(t *testing.T) {
	cases := map[domain.Status][]string{
		domain.StatusDraft:        {"edit", "upload_document", "submit", "cancel"},
		domain.StatusApproved:     {"sign", "decline"},
		domain.StatusChecking:     {},
		domain.StatusManualReview: {"upload_document"},
	}
	for st, want := range cases {
		got := AllowedActions(domain.Application{Status: st}, domain.RoleClient)
		if len(got) != len(want) {
			t.Errorf("%s: got %v want %v", st, got, want)
		}
	}
	if a := AllowedActions(domain.Application{Status: domain.StatusManualReview}, domain.RoleUnderwriter); len(a) != 4 {
		t.Errorf("underwriter actions %v", a)
	}
}

func TestSanitizeFilename(t *testing.T) {
	for in, want := range map[string]string{
		"../../etc/passwd":   "passwd",
		`C:\docs\otchet.pdf`: "otchet.pdf",
		"":                   "document",
		"a<b>.pdf":           "a_b_.pdf",
	} {
		if got := sanitizeFilename(in); got != want {
			t.Errorf("sanitizeFilename(%q)=%q want %q", in, got, want)
		}
	}
}

func TestPipelineNotice(t *testing.T) {
	if pipelineNotice(nil, domain.RoleClient) != nil {
		t.Fatal("no trouble — no notice")
	}
	tr := &store.TaskTrouble{Status: "pending", Attempts: 2, MaxAttempts: 8, NextRunAt: time.Now(),
		LastError: "checks: registry: source unavailable: status 503\nbki: source unavailable: status 503"}
	client := pipelineNotice(tr, domain.RoleClient)
	if client.State != "retrying" || client.NextAttemptAt == nil || client.TechnicalError != "" {
		t.Fatalf("client notice: %+v", client)
	}
	if !strings.Contains(client.Message, "ЕГРЮЛ/ЕГРИП") || !strings.Contains(client.Message, "бюро кредитных историй") ||
		!strings.Contains(client.Message, "ничего делать не нужно") {
		t.Fatalf("client message must name the sources and reassure: %q", client.Message)
	}
	if uw := pipelineNotice(tr, domain.RoleUnderwriter); uw.TechnicalError == "" {
		t.Fatal("underwriter must see the technical cause")
	}
	tr.Status = "failed"
	if n := pipelineNotice(tr, domain.RoleClient); n.State != "failed" || n.NextAttemptAt != nil {
		t.Fatalf("failed notice: %+v", n)
	}
	if !strings.Contains(client.Message, "временно не отвечают") {
		t.Fatalf("plural sources need plural verb: %q", client.Message)
	}
	if got, many := failedSourceTitle("checks: fns: source unavailable"); got != "Сервис ФНС" || many {
		t.Fatalf("got %q %v", got, many)
	}
	if got, _ := failedSourceTitle("context deadline exceeded"); got != "Внешний сервис" {
		t.Fatalf("got %q", got)
	}
	single := pipelineNotice(&store.TaskTrouble{Status: "pending", LastError: "checks: bki: source unavailable"}, domain.RoleClient)
	if !strings.Contains(single.Message, "Сервис бюро кредитных историй временно не отвечает.") {
		t.Fatalf("single source message: %q", single.Message)
	}
}
