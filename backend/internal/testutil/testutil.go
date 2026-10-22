// Package testutil — окружение для интеграционных тестов: чистая БД PostgreSQL
// и симулятор внешних источников.
//
// Тесты, которым нужна БД, пропускаются, если не задан TEST_DATABASE_URL
// (unit-тесты при этом работают без инфраструктуры). В CI и через
// `make test-integration` поднимается Postgres в Docker.
package testutil

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"alfahack/backend/internal/auth"
	"alfahack/backend/internal/integrations"
	"alfahack/backend/internal/mockgov"
	"alfahack/backend/internal/service"
	"alfahack/backend/internal/store"
)

// Env — поднятое тестовое окружение.
type Env struct {
	Store   *store.Store
	Service *service.Service
	Tokens  *auth.Issuer
	Mock    *httptest.Server
	Faults  *mockgov.Faults
	Log     *slog.Logger
}

// Setup пересоздаёт схему БД, применяет миграции, поднимает симулятор и сидирует демо-данные.
func Setup(t *testing.T) *Env {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — integration test skipped")
	}
	ctx := context.Background()
	st, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.DB.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if os.Getenv("TEST_VERBOSE") != "" {
		log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	data, err := mockgov.LoadData(time.Now)
	if err != nil {
		t.Fatal(err)
	}
	faults := &mockgov.Faults{}
	mock := httptest.NewServer(mockgov.NewServer(data, faults, log).Handler())
	t.Cleanup(mock.Close)

	cfg := integrations.DefaultConfig(mock.URL)
	cfg.BackoffBase = time.Millisecond
	cfg.BreakerThreshold = 1000
	src := integrations.NewClient(cfg, log)

	tokens, err := auth.NewIssuer("integration-test-secret-integration-test", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	svcCfg := service.DefaultConfig()
	svcCfg.StepDelay = 0
	svc := service.New(st, src, tokens, svcCfg, log)
	if err := svc.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	return &Env{Store: st, Service: svc, Tokens: tokens, Mock: mock, Faults: faults, Log: log}
}

// Drain прогоняет очередь конвейера до опустошения.
func (e *Env) Drain(t *testing.T) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if !e.Service.RunOnce(context.Background()) {
			return
		}
	}
	t.Fatal("pipeline did not settle in 100 steps")
}
