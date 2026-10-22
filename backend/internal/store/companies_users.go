package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"alfahack/backend/internal/domain"
)

const companyCols = `id, inn, ogrn, name, kind, okved, okved_name, region, address, director, registered_at, registry_status, created_at`

func scanCompany(row interface{ Scan(...any) error }) (domain.Company, error) {
	var c domain.Company
	var reg sql.NullTime
	err := row.Scan(&c.ID, &c.INN, &c.OGRN, &c.Name, &c.Kind, &c.OKVED, &c.OKVEDName, &c.Region,
		&c.Address, &c.Director, &reg, &c.RegistryStatus, &c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return c, domain.ErrNotFound
	}
	if reg.Valid {
		c.RegisteredAt = reg.Time
	}
	return c, err
}

// UpsertCompany создаёт или обновляет компанию по ИНН (данные из реестра).
func (q *Queries) UpsertCompany(ctx context.Context, c domain.Company) (domain.Company, error) {
	if c.ID == "" {
		c.ID = domain.NewID()
	}
	var reg any
	if !c.RegisteredAt.IsZero() {
		reg = c.RegisteredAt
	}
	row := q.db.QueryRowContext(ctx, `
		INSERT INTO companies (id, inn, ogrn, name, kind, okved, okved_name, region, address, director, registered_at, registry_status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (inn) DO UPDATE SET
			ogrn=EXCLUDED.ogrn, name=EXCLUDED.name, kind=EXCLUDED.kind, okved=EXCLUDED.okved,
			okved_name=EXCLUDED.okved_name, region=EXCLUDED.region, address=EXCLUDED.address,
			director=EXCLUDED.director, registered_at=EXCLUDED.registered_at,
			registry_status=EXCLUDED.registry_status, updated_at=now()
		RETURNING `+companyCols,
		c.ID, c.INN, c.OGRN, c.Name, c.Kind, c.OKVED, c.OKVEDName, c.Region, c.Address, c.Director, reg, c.RegistryStatus)
	return scanCompany(row)
}

// GetCompany возвращает компанию по id.
func (q *Queries) GetCompany(ctx context.Context, id string) (domain.Company, error) {
	return scanCompany(q.db.QueryRowContext(ctx, `SELECT `+companyCols+` FROM companies WHERE id=$1`, id))
}

// GetCompanyByINN возвращает компанию по ИНН.
func (q *Queries) GetCompanyByINN(ctx context.Context, inn string) (domain.Company, error) {
	return scanCompany(q.db.QueryRowContext(ctx, `SELECT `+companyCols+` FROM companies WHERE inn=$1`, inn))
}

const userCols = `id, email, password_hash, full_name, role, COALESCE(company_id::text, ''), created_at`

func scanUser(row interface{ Scan(...any) error }) (domain.User, error) {
	var u domain.User
	err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.FullName, &u.Role, &u.CompanyID, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return u, domain.ErrNotFound
	}
	return u, err
}

// CreateUser создаёт пользователя; email нормализуется к нижнему регистру.
func (q *Queries) CreateUser(ctx context.Context, u domain.User) (domain.User, error) {
	if u.ID == "" {
		u.ID = domain.NewID()
	}
	var company any
	if u.CompanyID != "" {
		company = u.CompanyID
	}
	row := q.db.QueryRowContext(ctx, `
		INSERT INTO users (id, email, password_hash, full_name, role, company_id)
		VALUES ($1, lower($2), $3, $4, $5, $6) RETURNING `+userCols,
		u.ID, strings.TrimSpace(u.Email), u.PasswordHash, u.FullName, u.Role, company)
	out, err := scanUser(row)
	if isUniqueViolation(err) {
		return out, domain.ErrAlreadyExists
	}
	return out, err
}

// GetUserByEmail ищет пользователя по email.
func (q *Queries) GetUserByEmail(ctx context.Context, email string) (domain.User, error) {
	return scanUser(q.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE email=lower($1)`, strings.TrimSpace(email)))
}

// GetUser возвращает пользователя по id.
func (q *Queries) GetUser(ctx context.Context, id string) (domain.User, error) {
	return scanUser(q.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id=$1`, id))
}

// UpsertDemoUser — для сидирования: обновляет пароль/компанию, если пользователь уже есть.
func (q *Queries) UpsertDemoUser(ctx context.Context, u domain.User) error {
	var company any
	if u.CompanyID != "" {
		company = u.CompanyID
	}
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO users (id, email, password_hash, full_name, role, company_id)
		VALUES ($1, lower($2), $3, $4, $5, $6)
		ON CONFLICT (email) DO UPDATE SET password_hash=EXCLUDED.password_hash,
			full_name=EXCLUDED.full_name, role=EXCLUDED.role, company_id=EXCLUDED.company_id`,
		domain.NewID(), u.Email, u.PasswordHash, u.FullName, u.Role, company)
	return err
}

// Now — время БД (единый источник времени для нескольких экземпляров).
func (q *Queries) Now(ctx context.Context) (time.Time, error) {
	var t time.Time
	err := q.db.QueryRowContext(ctx, `SELECT now()`).Scan(&t)
	return t, err
}
