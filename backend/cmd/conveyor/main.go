// Command conveyor — кредитный конвейер.
//
// Подкоманды:
//
//	api     — HTTP API (+ миграции и демо-данные при старте, если включены)
//	worker  — обработчик очереди конвейера (можно запускать несколько экземпляров)
//	all     — API и воркер в одном процессе (удобно для бесплатного хостинга)
//	migrate — только применить миграции
//	seed    — только загрузить демо-данные
//	healthcheck — проверка готовности (для Docker)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"alfahack/backend/internal/assistant"
	"alfahack/backend/internal/auth"
	"alfahack/backend/internal/httpapi"
	"alfahack/backend/internal/integrations"
	"alfahack/backend/internal/service"
	"alfahack/backend/internal/store"
)

type config struct {
	Port         string
	DatabaseURL  string
	JWTSecret    string
	JWTTTL       time.Duration
	SourcesURL   string
	MockAdminURL string
	CORSOrigins  []string
	StaticDir    string
	DemoMode     bool
	AutoMigrate  bool
	AutoSeed     bool
	StepDelay    time.Duration
	Workers      int
	LogLevel     string
	LLMURL       string
	LLMModel     string
}

func loadConfig() config {
	return config{
		Port:         env("PORT", "8080"),
		DatabaseURL:  env("DATABASE_URL", "postgres://conveyor:conveyor@localhost:5432/conveyor?sslmode=disable"),
		JWTSecret:    env("JWT_SECRET", ""),
		JWTTTL:       envDuration("JWT_TTL", 12*time.Hour),
		SourcesURL:   env("SOURCES_URL", "http://localhost:8090"),
		MockAdminURL: env("MOCK_ADMIN_URL", ""),
		CORSOrigins:  strings.Split(env("CORS_ORIGINS", "http://localhost:5173"), ","),
		StaticDir:    env("STATIC_DIR", ""),
		DemoMode:     envBool("DEMO_MODE", true),
		AutoMigrate:  envBool("AUTO_MIGRATE", true),
		AutoSeed:     envBool("AUTO_SEED", true),
		StepDelay:    envDuration("PIPELINE_STEP_DELAY", 2*time.Second),
		Workers:      envInt("WORKER_CONCURRENCY", 4),
		LogLevel:     env("LOG_LEVEL", "info"),
		LLMURL:       env("LLM_URL", ""),
		LLMModel:     env("LLM_MODEL", "qwen2.5:1.5b-instruct"),
	}
}

func main() {
	cmd := "all"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	cfg := loadConfig()
	if cmd == "healthcheck" { // для Docker HEALTHCHECK в distroless-образе без curl
		os.Exit(healthcheck("http://127.0.0.1:" + cfg.Port + "/readyz"))
	}
	var level slog.Level
	_ = level.UnmarshalText([]byte(cfg.LogLevel))
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})).With("service", "conveyor", "cmd", cmd)
	slog.SetDefault(log)

	if err := run(cmd, cfg, log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(cmd string, cfg config, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cfg.JWTSecret == "" {
		if !cfg.DemoMode {
			return errors.New("JWT_SECRET is required when DEMO_MODE=false")
		}
		cfg.JWTSecret = "demo-only-insecure-secret-change-me-in-production"
		log.Warn("JWT_SECRET not set — using demo secret (do not use in production)")
	}
	tokens, err := auth.NewIssuer(cfg.JWTSecret, cfg.JWTTTL)
	if err != nil {
		return err
	}

	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()

	src := integrations.NewClient(integrations.DefaultConfig(cfg.SourcesURL), log)
	svcCfg := service.DefaultConfig()
	svcCfg.StepDelay = cfg.StepDelay
	svcCfg.DemoSignCode = cfg.DemoMode
	svc := service.New(st, src, tokens, svcCfg, log)
	if cfg.LLMURL != "" {
		svc.SetSummarizer(assistant.NewOllama(cfg.LLMURL, cfg.LLMModel, log))
		log.Info("assistant: local LLM enabled", "url", cfg.LLMURL, "model", cfg.LLMModel)
	}

	if cfg.AutoMigrate || cmd == "migrate" {
		if err := st.Migrate(ctx); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
		log.Info("migrations applied")
	}
	if cmd == "migrate" {
		return nil
	}
	if (cfg.AutoSeed && cmd != "worker") || cmd == "seed" {
		if err := seedWithRetry(ctx, svc, log); err != nil {
			return fmt.Errorf("seed: %w", err)
		}
	}
	if cmd == "seed" {
		return nil
	}

	var wg sync.WaitGroup
	errc := make(chan error, 2)
	if cmd == "api" || cmd == "all" {
		srv := &http.Server{
			Addr: ":" + cfg.Port,
			Handler: httpapi.New(svc, st, tokens, httpapi.Config{
				CORSOrigins: cfg.CORSOrigins, MockAdminURL: cfg.MockAdminURL, DemoMode: cfg.DemoMode, StaticDir: cfg.StaticDir,
			}, log).Handler(),
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      60 * time.Second, // сводка локальной моделью на CPU может занять до 45 с
			IdleTimeout:       120 * time.Second,
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			log.Info("api listening", "port", cfg.Port)
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errc <- err
			}
		}()
		go func() {
			<-ctx.Done()
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdown) // дожидаемся завершения активных запросов
		}()
	}
	if cmd == "worker" || cmd == "all" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			log.Info("worker started", "concurrency", cfg.Workers)
			svc.RunWorker(ctx, cfg.Workers, 500*time.Millisecond)
		}()
	}
	if cmd != "api" && cmd != "worker" && cmd != "all" {
		return fmt.Errorf("unknown command %q (api|worker|all|migrate|seed)", cmd)
	}

	select {
	case err := <-errc:
		stop()
		wg.Wait()
		return err
	case <-ctx.Done():
	}
	wg.Wait()
	log.Info("stopped gracefully")
	return nil
}

// seedWithRetry ждёт готовности симулятора реестра (он может стартовать позже API).
func seedWithRetry(ctx context.Context, svc *service.Service, log *slog.Logger) error {
	var err error
	for i := 0; i < 20; i++ {
		if err = svc.Seed(ctx); err == nil {
			log.Info("demo data seeded")
			return nil
		}
		log.Warn("seed failed, retrying", "err", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return err
}

func healthcheck(url string) int {
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get(url)
	if err != nil || resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return v
	}
	return def
}

func envBool(k string, def bool) bool {
	if v, err := strconv.ParseBool(os.Getenv(k)); err == nil {
		return v
	}
	return def
}

func envDuration(k string, def time.Duration) time.Duration {
	if v, err := time.ParseDuration(os.Getenv(k)); err == nil {
		return v
	}
	return def
}
