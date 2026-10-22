package service

import (
	"context"
	"encoding/json"

	"alfahack/backend/internal/auth"
	"alfahack/backend/internal/domain"
	"alfahack/backend/internal/news"
	"alfahack/backend/internal/store"
)

// Feed — персональная лента и профиль, по которому она построена (для прозрачности на демо).
type Feed struct {
	Profile FeedProfile     `json:"profile"`
	Items   []news.FeedItem `json:"items"`
}

// FeedProfile — что учтено при подборе.
type FeedProfile struct {
	Industry  string     `json:"industry"`
	Region    string     `json:"region"`
	Kind      string     `json:"kind"`
	AgeMonths int        `json:"age_months"`
	Stage     news.Stage `json:"stage"`
	Products  []string   `json:"products"`
}

// GetFeed строит персональную ленту клиента.
func (s *Service) GetFeed(ctx context.Context, p auth.Principal) (Feed, error) {
	if err := requireRole(p, domain.RoleClient); err != nil {
		return Feed{}, err
	}
	c, err := s.store.GetCompany(ctx, p.CompanyID)
	if err != nil {
		return Feed{}, err
	}
	apps, _, err := s.store.ListApplications(ctx, store.ListFilter{CompanyID: p.CompanyID, Limit: 50})
	if err != nil {
		return Feed{}, err
	}
	now := s.now()
	prof := news.Profile{OKVEDSection: c.OKVEDSection(), Region: c.Region, Kind: c.Kind, AgeMonths: c.AgeMonths(now), Stage: news.StageNone}
	numbers := map[string]string{}
	for _, a := range apps { // список отсортирован: самая свежая заявка первая
		numbers[a.ID] = a.Number
		if a.Status == domain.StatusCancelled {
			continue
		}
		if prof.Stage == news.StageNone {
			prof.Stage = news.StageOf(a.Status)
		}
		if !containsString(prof.Products, a.ProductCode) && a.Status != domain.StatusDraft {
			prof.Products = append(prof.Products, a.ProductCode)
		}
	}

	rows, err := s.store.ListNews(ctx)
	if err != nil {
		return Feed{}, err
	}
	items := make([]news.Item, 0, len(rows))
	for _, r := range rows {
		it := news.Item{ID: r.ID, Title: r.Title, Body: r.Body, Category: r.Category, Priority: r.Priority,
			Action: r.LinkAction, PublishedAt: r.PublishedAt.Time}
		_ = json.Unmarshal(r.Tags, &it.Tags)
		items = append(items, it)
	}
	events, err := s.store.ListCompanyEvents(ctx, p.CompanyID, 20)
	if err != nil {
		return Feed{}, err
	}
	feed := news.Merge(news.EventItems(events, numbers), news.Rank(items, prof, now), 3, 12)

	products := prof.Products
	if products == nil {
		products = []string{}
	}
	return Feed{
		Profile: FeedProfile{Industry: c.OKVED + " " + c.OKVEDName, Region: c.Region, Kind: string(c.Kind),
			AgeMonths: prof.AgeMonths, Stage: prof.Stage, Products: products},
		Items: feed,
	}, nil
}

func containsString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
