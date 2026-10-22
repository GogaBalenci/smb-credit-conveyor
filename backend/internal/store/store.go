// Package store — слой хранения в PostgreSQL.
//
// Все изменения статуса заявки проходят через Queries.Transition внутри транзакции
// с блокировкой строки (SELECT ... FOR UPDATE): проверка машины состояний, запись
// события в журнал, увеличение версии и постановка задачи конвейера выполняются
// атомарно. Это гарантирует последовательное изменение состояния заявки
// (кейс, критерий 04) даже при нескольких экземплярах API и воркеров.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	_ "github.com/lib/pq" // драйвер PostgreSQL
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// dbtx — общий интерфейс *sql.DB и *sql.Tx.
type dbtx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Queries — набор запросов, работающий как вне, так и внутри транзакции.
type Queries struct {
	db dbtx
}

// Store — точка входа в хранилище.
type Store struct {
	*Queries
	DB *sql.DB
}

// Open подключается к БД с ожиданием готовности (контейнер Postgres может стартовать дольше API).
func Open(ctx context.Context, dsn string) (*Store, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(30 * time.Minute)

	var lastErr error
	for i := 0; i < 30; i++ {
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		lastErr = db.PingContext(pingCtx)
		cancel()
		if lastErr == nil {
			return &Store{Queries: &Queries{db: db}, DB: db}, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	_ = db.Close()
	return nil, fmt.Errorf("database not reachable: %w", lastErr)
}

// Close закрывает пул соединений.
func (s *Store) Close() error { return s.DB.Close() }

// InTx выполняет fn в транзакции; при ошибке — откат.
func (s *Store) InTx(ctx context.Context, fn func(q *Queries) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(&Queries{db: tx}); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Ping — проверка готовности для /readyz.
func (s *Store) Ping(ctx context.Context) error { return s.DB.PingContext(ctx) }

// Migrate применяет встроенные миграции. Advisory lock защищает от гонки,
// когда одновременно стартуют несколько экземпляров.
func (s *Store) Migrate(ctx context.Context) error {
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock(727001)`); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(727001)`) //nolint:errcheck

	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		var exists bool
		if err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, name).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version) VALUES ($1)`, name); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func isUniqueViolation(err error) bool {
	type pqErr interface{ SQLState() string }
	var pe pqErr
	return errors.As(err, &pe) && pe.SQLState() == "23505"
}
