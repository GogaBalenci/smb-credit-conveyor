package news

import (
	"embed"
	"encoding/json"
	"time"
)

//go:embed seed/news.json
var seedFS embed.FS

type seedItem struct {
	Item
	DaysAgo int `json:"days_ago"`
}

// SeedItems возвращает демо-материалы ленты с датами относительно now.
func SeedItems(now time.Time) ([]Item, error) {
	raw, err := seedFS.ReadFile("seed/news.json")
	if err != nil {
		return nil, err
	}
	var list []seedItem
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(list))
	for _, s := range list {
		it := s.Item
		it.PublishedAt = now.AddDate(0, 0, -s.DaysAgo).Truncate(time.Hour)
		out = append(out, it)
	}
	return out, nil
}
