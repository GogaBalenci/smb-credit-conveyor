package news

import (
	"testing"
	"time"

	"alfahack/backend/internal/domain"
)

var now = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

func seed(t *testing.T) []Item {
	t.Helper()
	items, err := SeedItems(now)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func ids(feed []FeedItem) map[string]bool {
	m := map[string]bool{}
	for _, f := range feed {
		m[f.ID] = true
	}
	return m
}

func TestFeed_DiffersByIndustryAndRegion(t *testing.T) {
	items := seed(t)
	retailKazan := Rank(items, Profile{OKVEDSection: "47", Region: "Республика Татарстан", Kind: domain.CompanyIndividual, AgeMonths: 40, Stage: StageNone}, now)
	itMoscow := Rank(items, Profile{OKVEDSection: "62", Region: "Москва", Kind: domain.CompanyLegal, AgeMonths: 90, Stage: StageNone}, now)

	a, b := ids(retailKazan), ids(itMoscow)
	if !a["n-okved-retail"] || a["n-okved-it"] || !a["n-region-tatarstan"] || !a["n-kind-ip"] {
		t.Errorf("retail IE in Tatarstan got wrong feed: %v", a)
	}
	if !b["n-okved-it"] || b["n-okved-retail"] || !b["n-region-moscow"] || b["n-kind-ip"] {
		t.Errorf("IT company in Moscow got wrong feed: %v", b)
	}
}

func TestFeed_ChangesWithApplicationStage(t *testing.T) {
	items := seed(t)
	base := Profile{OKVEDSection: "62", Region: "Москва", Kind: domain.CompanyLegal, AgeMonths: 90}

	before := base
	before.Stage = StageNone
	rejected := base
	rejected.Stage = StageRejected
	issued := base
	issued.Stage = StageIssued
	issued.Products = []string{"OVERDRAFT"}

	if f := ids(Rank(items, before, now)); !f["n-stage-none-prepare"] || f["n-stage-issued-repay"] {
		t.Errorf("no-application stage: %v", f)
	}
	if f := ids(Rank(items, rejected, now)); !f["n-stage-rejected-history"] || f["n-stage-none-prepare"] {
		t.Errorf("rejected stage: %v", f)
	}
	f := Rank(items, issued, now)
	got := ids(f)
	if !got["n-stage-issued-repay"] || !got["n-product-overdraft"] || got["n-product-investment"] {
		t.Errorf("issued overdraft: %v", got)
	}
}

func TestFeed_TargetedItemsRankAboveGeneric(t *testing.T) {
	feed := Rank(seed(t), Profile{OKVEDSection: "56", Region: "Новосибирская область", AgeMonths: 10, Stage: StageNeedsInfo}, now)
	if len(feed) == 0 || len(feed[0].Reasons) == 0 || feed[0].Reasons[0] == "Для всех клиентов" {
		t.Fatalf("top item must be targeted, got %+v", feed[0])
	}
	for _, f := range feed {
		if len(f.Reasons) == 0 {
			t.Errorf("%s: every card must explain why it is shown", f.ID)
		}
	}
	if !ids(feed)["n-young-business"] {
		t.Error("young business must see the first-loan article")
	}
}

func TestStageOf_CoversAllStatuses(t *testing.T) {
	for _, s := range domain.AllStatuses {
		if s != domain.StatusCancelled && StageOf(s) == StageNone {
			t.Errorf("status %s has no stage", s)
		}
	}
}

func TestEventItemsAndMerge(t *testing.T) {
	events := []domain.Event{ // от новых к старым, как отдаёт хранилище
		{ID: 5, ApplicationID: "a1", From: domain.StatusApproved, To: domain.StatusSigned, ActorKind: domain.ActorClient, CreatedAt: now},
		{ID: 4, ApplicationID: "a1", From: domain.StatusScoring, To: domain.StatusApproved, ActorKind: domain.ActorSystem, CreatedAt: now},
		{ID: 3, ApplicationID: "a1", From: domain.StatusChecking, To: domain.StatusScoring, ActorKind: domain.ActorSystem, CreatedAt: now},
		{ID: 2, ApplicationID: "a2", From: domain.StatusChecking, To: domain.StatusNeedsInfo, ActorKind: domain.ActorSystem, CreatedAt: now},
		{ID: 1, ApplicationID: "a2", From: domain.StatusDraft, To: domain.StatusSubmitted, ActorKind: domain.ActorClient, CreatedAt: now},
	}
	ev := EventItems(events, map[string]string{"a1": "КЗ-2026-1001", "a2": "КЗ-2026-1002"})
	// По одной карточке на заявку, самое свежее важное событие; промежуточные шаги не попадают.
	if len(ev) != 2 || ev[0].Title != "Заявка КЗ-2026-1001: договор подписан" || ev[1].Title != "Заявка КЗ-2026-1002: нужны дополнительные данные" {
		t.Fatalf("unexpected event items %+v", ev)
	}
	merged := Merge(ev, Rank(seed(t), Profile{Stage: StageOffer}, now), 3, 5)
	if len(merged) != 5 || merged[0].Type != "event" {
		t.Fatalf("events must go first and limit applied: %d", len(merged))
	}
}
