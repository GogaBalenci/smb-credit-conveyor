package service

import (
	"context"
	"errors"
	"net/mail"
	"strings"
	"time"

	"alfahack/backend/internal/auth"
	"alfahack/backend/internal/domain"
	"alfahack/backend/internal/integrations"
	"alfahack/backend/internal/store"
)

// RegisterInput — регистрация представителя бизнеса.
type RegisterInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	FullName string `json:"full_name"`
	INN      string `json:"inn"`
}

// Session — результат входа.
type Session struct {
	Token     string          `json:"token"`
	ExpiresAt time.Time       `json:"expires_at"`
	User      domain.User     `json:"user"`
	Company   *domain.Company `json:"company,omitempty"`
}

// Register создаёт пользователя и профиль компании. Клиент вводит только ИНН —
// остальное подтягивается из ЕГРЮЛ/ЕГРИП.
func (s *Service) Register(ctx context.Context, in RegisterInput) (Session, error) {
	v := domain.NewValidation()
	if _, err := mail.ParseAddress(in.Email); err != nil {
		v.Add("email", "Некорректный email")
	}
	if len(in.Password) < 8 {
		v.Add("password", "Минимум 8 символов")
	}
	if strings.TrimSpace(in.FullName) == "" {
		v.Add("full_name", "Укажите ФИО")
	}
	if !domain.ValidINN(in.INN) {
		v.Add("inn", "Некорректный ИНН: 10 цифр для организации, 12 — для ИП")
	}
	if err := v.OrNil(); err != nil {
		return Session{}, err
	}

	company, err := s.companyFromRegistry(ctx, in.INN)
	if err != nil {
		return Session{}, err
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		return Session{}, err
	}
	var user domain.User
	err = s.store.InTx(ctx, func(q *store.Queries) error {
		c, err := q.UpsertCompany(ctx, company)
		if err != nil {
			return err
		}
		company = c
		user, err = q.CreateUser(ctx, domain.User{
			Email: in.Email, PasswordHash: hash, FullName: strings.TrimSpace(in.FullName),
			Role: domain.RoleClient, CompanyID: c.ID,
		})
		return err
	})
	if errors.Is(err, domain.ErrAlreadyExists) {
		ve := domain.NewValidation()
		ve.Add("email", "Пользователь с таким email уже зарегистрирован")
		return Session{}, ve
	}
	if err != nil {
		return Session{}, err
	}
	return s.session(user, &company)
}

// LookupCompany — предпросмотр компании по ИНН до регистрации (автозаполнение формы).
func (s *Service) LookupCompany(ctx context.Context, inn string) (domain.Company, error) {
	if !domain.ValidINN(inn) {
		v := domain.NewValidation()
		v.Add("inn", "Некорректный ИНН")
		return domain.Company{}, v
	}
	return s.companyFromRegistry(ctx, inn)
}

func (s *Service) companyFromRegistry(ctx context.Context, inn string) (domain.Company, error) {
	rec, err := s.src.Registry(ctx, inn)
	if errors.Is(err, integrations.ErrNotFound) {
		v := domain.NewValidation()
		v.Add("inn", "Компания с таким ИНН не найдена в ЕГРЮЛ/ЕГРИП")
		return domain.Company{}, v
	}
	if err != nil {
		return domain.Company{}, errors.Join(ErrSourceUnavailable, err)
	}
	return companyFromRecord(rec), nil
}

func companyFromRecord(rec integrations.RegistryRecord) domain.Company {
	c := domain.Company{
		INN: rec.INN, OGRN: rec.OGRN, Name: rec.ShortName, Kind: domain.CompanyKind(rec.Kind),
		OKVED: rec.OKVED, OKVEDName: rec.OKVEDName, Region: rec.Region, Address: rec.Address,
		Director: rec.Director, RegistryStatus: rec.Status,
	}
	if t, err := time.Parse("2006-01-02", rec.RegisteredAt); err == nil {
		c.RegisteredAt = t
	}
	if c.Kind == "" {
		c.Kind = domain.INNKind(rec.INN)
	}
	return c
}

// Login проверяет пароль и выдаёт токен. Ответ одинаков для неизвестного email
// и неверного пароля — не раскрываем, какие учётные записи существуют.
func (s *Service) Login(ctx context.Context, email, password string) (Session, error) {
	u, err := s.store.GetUserByEmail(ctx, email)
	if errors.Is(err, domain.ErrNotFound) {
		auth.CheckPassword(dummyHash, password) // выравниваем время ответа
		return Session{}, domain.ErrUnauthorized
	}
	if err != nil {
		return Session{}, err
	}
	if !auth.CheckPassword(u.PasswordHash, password) {
		return Session{}, domain.ErrUnauthorized
	}
	var company *domain.Company
	if u.CompanyID != "" {
		c, err := s.store.GetCompany(ctx, u.CompanyID)
		if err != nil {
			return Session{}, err
		}
		company = &c
	}
	return s.session(u, company)
}

// dummyHash — хэш для выравнивания времени ответа при неизвестном email.
var dummyHash, _ = auth.HashPassword("timing-equalizer")

func (s *Service) session(u domain.User, c *domain.Company) (Session, error) {
	tok, exp, err := s.tokens.Issue(u)
	if err != nil {
		return Session{}, err
	}
	return Session{Token: tok, ExpiresAt: exp, User: u, Company: c}, nil
}

// Me — профиль текущего пользователя.
func (s *Service) Me(ctx context.Context, p auth.Principal) (domain.User, *domain.Company, error) {
	u, err := s.store.GetUser(ctx, p.UserID)
	if err != nil {
		return u, nil, err
	}
	if u.CompanyID == "" {
		return u, nil, nil
	}
	c, err := s.store.GetCompany(ctx, u.CompanyID)
	return u, &c, err
}
