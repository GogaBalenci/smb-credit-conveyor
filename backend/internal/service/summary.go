package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"alfahack/backend/internal/assistant"
	"alfahack/backend/internal/auth"
	"alfahack/backend/internal/domain"
	"alfahack/backend/internal/metrics"
)

// summaryCache хранит сводки по версии заявки: пока заявка не изменилась,
// повторный запрос не нагружает модель.
type summaryCache struct {
	mu    sync.Mutex
	items map[string]assistant.Summary
}

func (c *summaryCache) get(key string) (assistant.Summary, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.items[key]
	return s, ok
}

func (c *summaryCache) put(key string, s assistant.Summary) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.items == nil || len(c.items) > 1000 {
		c.items = map[string]assistant.Summary{}
	}
	c.items[key] = s
}

// SetSummarizer подключает генератор сводок (по умолчанию — шаблонный).
func (s *Service) SetSummarizer(sm assistant.Summarizer) { s.summarizer = sm }

// Summary — сводка досье для кредитного эксперта.
func (s *Service) Summary(ctx context.Context, p auth.Principal, id string, refresh bool) (assistant.Summary, error) {
	if err := requireRole(p, domain.RoleUnderwriter); err != nil {
		return assistant.Summary{}, err
	}
	d, err := s.GetDetails(ctx, p, id)
	if err != nil {
		return assistant.Summary{}, err
	}
	key := fmt.Sprintf("%s:%d", id, d.Application.Version)
	if cached, ok := s.summaries.get(key); ok && !refresh && cached.Warning == "" {
		return cached, nil
	}
	sum, err := s.summarizer.Summarize(ctx, factsFrom(d))
	if err != nil {
		return sum, err
	}
	result := sum.Source
	if sum.Warning != "" {
		result = "fallback"
	}
	metrics.Default.Inc("assistant_summaries_total", "Сводки досье для эксперта", "result", result)
	s.summaries.put(key, sum)
	return sum, nil
}

func factsFrom(d ApplicationDetails) assistant.Facts {
	a, c := d.Application, d.Company
	f := assistant.Facts{
		Number: a.Number, Company: c.Name, CompanyKind: c.Kind, INN: c.INN, OKVED: c.OKVED, OKVEDName: c.OKVEDName,
		Region: c.Region, RegisteredAt: c.RegisteredAt, AgeMonths: c.AgeMonths(time.Now()),
		Product: d.Product.Name, Purpose: string(a.Purpose), PurposeComment: a.PurposeComment,
		Amount: a.Amount, TermMonths: a.TermMonths, Financials: a.Financials, Status: d.StatusInfo.Title,
		Checks: d.Checks, Score: a.Score, Decision: a.Decision,
	}
	for _, p := range domain.Purposes {
		if p.Code == a.Purpose {
			f.Purpose = p.Title
		}
	}
	for _, doc := range d.Documents {
		f.Documents = append(f.Documents, domain.DocKindTitles[doc.Kind])
	}
	for _, r := range a.MissingDocs() {
		f.MissingDocs = append(f.MissingDocs, domain.DocKindTitles[r.Kind])
	}
	return f
}
