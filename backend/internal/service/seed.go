package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"alfahack/backend/internal/auth"
	"alfahack/backend/internal/domain"
	"alfahack/backend/internal/news"
	"alfahack/backend/internal/store"
)

// DemoPassword — пароль всех демо-учёток.
const DemoPassword = "demo12345"

// DemoAccount — тестовая учётная запись под сценарий.
type DemoAccount struct {
	Email    string      `json:"email"`
	FullName string      `json:"full_name"`
	INN      string      `json:"inn,omitempty"`
	Role     domain.Role `json:"role"`
	Scenario string      `json:"scenario"`
}

// DemoAccounts — по одной учётке на каждый исход конвейера.
var DemoAccounts = []DemoAccount{
	{"approve@demo.ru", "Орлов Дмитрий Андреевич", "7701234560", domain.RoleClient, "Автоматическое одобрение (Экспресс-кредит 2 000 000 ₽ на 24 мес.)"},
	{"counter@demo.ru", "Кузнецова Анна Сергеевна", "165987654329", domain.RoleClient, "Контрпредложение с увеличенным сроком (Экспресс 3 000 000 ₽ на 12 мес.)"},
	{"reject@demo.ru", "Грачёв Сергей Викторович", "7802345676", domain.RoleClient, "Отказ по стоп-фактору: просрочка более 90 дней"},
	{"review@demo.ru", "Белова Мария Игоревна", "5403456780", domain.RoleClient, "Дозапрос выписки → ручное рассмотрение андеррайтером (Экспресс 1 000 000 ₽ на 24 мес.)"},
	{"large@demo.ru", "Ткаченко Игорь Петрович", "6164567897", domain.RoleClient, "Крупная сумма: дозапрос отчётности → ручное решение (Оборотный 15 000 000 ₽ на 24 мес.)"},
	{"liquidated@demo.ru", "Соколов Павел Олегович", "770123456703", domain.RoleClient, "Отказ: ИП прекратил деятельность по данным реестра"},
	{"underwriter@demo.ru", "Кредитный эксперт Смирнова Е. В.", "", domain.RoleUnderwriter, "Рабочее место андеррайтера: очередь, проверки, скоринг, решение"},
}

// Seed создаёт демо-данные идемпотентно: компании (из симулятора реестра),
// учётные записи и материалы ленты.
func (s *Service) Seed(ctx context.Context) error {
	hash, err := auth.HashPassword(DemoPassword)
	if err != nil {
		return err
	}
	for _, a := range DemoAccounts {
		u := domain.User{Email: a.Email, FullName: a.FullName, Role: a.Role, PasswordHash: hash}
		if a.INN != "" {
			// Компания уже в базе — реестр не запрашиваем: повторный старт API
			// не должен зависеть от доступности внешних источников.
			c, err := s.store.GetCompanyByINN(ctx, a.INN)
			if errors.Is(err, domain.ErrNotFound) {
				if c, err = s.companyFromRegistry(ctx, a.INN); err != nil {
					return fmt.Errorf("seed company %s: %w", a.INN, err)
				}
				c, err = s.store.UpsertCompany(ctx, c)
			}
			if err != nil {
				return err
			}
			u.CompanyID = c.ID
		}
		if err := s.store.UpsertDemoUser(ctx, u); err != nil {
			return fmt.Errorf("seed user %s: %w", a.Email, err)
		}
	}
	items, err := news.SeedItems(s.now())
	if err != nil {
		return err
	}
	return s.store.InTx(ctx, func(q *store.Queries) error {
		for _, it := range items {
			tags, _ := json.Marshal(it.Tags)
			if err := q.UpsertNews(ctx, store.NewsRow{ID: it.ID, Title: it.Title, Body: it.Body, Category: it.Category,
				Tags: tags, Priority: it.Priority, LinkAction: it.Action,
				PublishedAt: sql.NullTime{Time: it.PublishedAt, Valid: true}}); err != nil {
				return err
			}
		}
		return nil
	})
}
