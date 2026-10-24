// Package news — персональная лента клиента.
//
// Кейс (стр. 7) просит показать, как лента меняется под клиента. Лента строится
// из двух источников:
//  1. редакционные материалы банка с тегами (отрасль по ОКВЭД, регион, форма
//     бизнеса, возраст бизнеса, стадия кредитного пути, продукт);
//  2. персональные события — изменения статусов заявок клиента.
//
// Каждая карточка объясняет, почему она показана («Для вашей отрасли»,
// «По статусу вашей заявки») — так поведение ленты прозрачно и проверяемо.
// Ранжирование — чистая функция без доступа к БД, покрыта тестами.
package news

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"alfahack/backend/internal/domain"
)

// Stage — стадия кредитного пути клиента.
type Stage string

const (
	StageNone      Stage = "none"       // заявок ещё нет
	StageDraft     Stage = "draft"      // есть черновик
	StageInReview  Stage = "in_review"  // заявка на рассмотрении
	StageNeedsInfo Stage = "needs_info" // банк запросил документы
	StageOffer     Stage = "offer"      // есть одобрение / предложение
	StageRejected  Stage = "rejected"   // отказ или клиент отказался
	StageIssued    Stage = "issued"     // кредит выдан / подписан
)

// StageOf определяет стадию по статусу заявки.
func StageOf(s domain.Status) Stage {
	switch s {
	case domain.StatusDraft:
		return StageDraft
	case domain.StatusSubmitted, domain.StatusChecking, domain.StatusScoring, domain.StatusManualReview:
		return StageInReview
	case domain.StatusNeedsInfo:
		return StageNeedsInfo
	case domain.StatusApproved, domain.StatusCounterOffer:
		return StageOffer
	case domain.StatusRejected, domain.StatusDeclined:
		return StageRejected
	case domain.StatusSigned, domain.StatusIssued:
		return StageIssued
	default:
		return StageNone
	}
}

// Tags — таргетинг материала. Пустое измерение = «для всех».
type Tags struct {
	OKVED        []string `json:"okved,omitempty"`   // классы ОКВЭД (2 цифры)
	Regions      []string `json:"regions,omitempty"` // регионы
	Kinds        []string `json:"kinds,omitempty"`   // LEGAL / INDIVIDUAL
	Stages       []Stage  `json:"stages,omitempty"`
	Products     []string `json:"products,omitempty"`
	MaxAgeMonths int      `json:"max_age_months,omitempty"` // только для бизнеса младше N мес.
}

// Item — материал ленты.
type Item struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	Category    string    `json:"category"`
	Tags        Tags      `json:"tags"`
	Priority    int       `json:"priority"`
	Action      string    `json:"action,omitempty"`
	PublishedAt time.Time `json:"published_at"`
}

// Profile — то, что банк знает о клиенте.
type Profile struct {
	OKVEDSection string
	Region       string
	Kind         domain.CompanyKind
	AgeMonths    int
	Stage        Stage
	Products     []string // продукты из заявок клиента
}

// FeedItem — карточка ленты.
type FeedItem struct {
	ID          string    `json:"id"`
	Type        string    `json:"type"` // news / event
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	Category    string    `json:"category"`
	Reasons     []string  `json:"reasons"`
	Relevance   int       `json:"relevance"`
	Action      string    `json:"action,omitempty"`
	PublishedAt time.Time `json:"published_at"`
}

// Rank отбирает и упорядочивает материалы для профиля.
func Rank(items []Item, p Profile, now time.Time) []FeedItem {
	out := make([]FeedItem, 0, len(items))
	for _, it := range items {
		reasons, ok := match(it.Tags, p)
		if !ok {
			continue
		}
		rel := it.Priority + 15*len(reasons)
		days := now.Sub(it.PublishedAt).Hours() / 24
		rel += int(math.Max(0, 10-days/3)) // свежие материалы выше
		if len(reasons) == 0 {
			reasons = []string{"Для всех клиентов"}
		}
		out = append(out, FeedItem{
			ID: it.ID, Type: "news", Title: it.Title, Body: it.Body, Category: it.Category,
			Reasons: reasons, Relevance: rel, Action: it.Action, PublishedAt: it.PublishedAt,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Relevance != out[j].Relevance {
			return out[i].Relevance > out[j].Relevance
		}
		return out[i].PublishedAt.After(out[j].PublishedAt)
	})
	return out
}

// match проверяет все непустые измерения тегов; возвращает причины показа.
func match(t Tags, p Profile) ([]string, bool) {
	var reasons []string
	if len(t.OKVED) > 0 {
		if !containsStr(t.OKVED, p.OKVEDSection) {
			return nil, false
		}
		reasons = append(reasons, "Для вашей отрасли (ОКВЭД "+p.OKVEDSection+")")
	}
	if len(t.Regions) > 0 {
		if !containsStr(t.Regions, p.Region) {
			return nil, false
		}
		reasons = append(reasons, "Для вашего региона: "+p.Region)
	}
	if len(t.Kinds) > 0 {
		if !containsStr(t.Kinds, string(p.Kind)) {
			return nil, false
		}
		if p.Kind == domain.CompanyIndividual {
			reasons = append(reasons, "Для индивидуальных предпринимателей")
		} else {
			reasons = append(reasons, "Для юридических лиц")
		}
	}
	if t.MaxAgeMonths > 0 {
		if p.AgeMonths > t.MaxAgeMonths {
			return nil, false
		}
		reasons = append(reasons, "Для молодого бизнеса")
	}
	if len(t.Stages) > 0 {
		found := false
		for _, s := range t.Stages {
			found = found || s == p.Stage
		}
		if !found {
			return nil, false
		}
		reasons = append(reasons, "По статусу вашей заявки")
	}
	if len(t.Products) > 0 {
		found := ""
		for _, pr := range t.Products {
			if containsStr(p.Products, pr) {
				found = pr
			}
		}
		if found == "" {
			return nil, false
		}
		name := found
		if prod, ok := domain.ProductByCode(found); ok {
			name = prod.Name
		}
		reasons = append(reasons, "По вашему продукту: "+name)
	}
	return reasons, true
}

// В ленту попадают только события, важные клиенту, и по каждой заявке — одно,
// самое свежее: промежуточные шаги конвейера видны на странице заявки.
var feedStatuses = map[domain.Status]bool{
	domain.StatusNeedsInfo: true, domain.StatusManualReview: true, domain.StatusApproved: true,
	domain.StatusCounterOffer: true, domain.StatusRejected: true, domain.StatusSigned: true, domain.StatusIssued: true,
}

// EventItems превращает события заявок (от новых к старым) в персональные карточки.
func EventItems(events []domain.Event, numbers map[string]string) []FeedItem {
	var out []FeedItem
	seen := map[string]bool{}
	for _, e := range events {
		if !feedStatuses[e.To] || e.From == e.To || seen[e.ApplicationID] {
			continue
		}
		seen[e.ApplicationID] = true
		info := e.To.Info()
		body := info.Description
		if e.Comment != "" {
			body += " " + strings.TrimSpace(e.Comment)
		}
		out = append(out, FeedItem{
			ID:   "event-" + strconv.FormatInt(e.ID, 10),
			Type: "event", Title: "Заявка " + numbers[e.ApplicationID] + ": " + strings.ToLower(info.Title),
			Body: body, Category: "Ваши заявки", Reasons: []string{"Событие по вашей заявке"},
			Relevance: 1000, Action: "open_application:" + e.ApplicationID, PublishedAt: e.CreatedAt,
		})
	}
	return out
}

// Merge объединяет персональные события (сверху, не больше maxEvents) и материалы.
func Merge(events, newsItems []FeedItem, maxEvents, limit int) []FeedItem {
	if len(events) > maxEvents {
		events = events[:maxEvents]
	}
	out := append(append([]FeedItem{}, events...), newsItems...)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}
