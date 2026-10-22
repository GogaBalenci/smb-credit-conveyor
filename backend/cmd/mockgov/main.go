// Command mockgov — симулятор внешних источников (ЕГРЮЛ/ЕГРИП, ФНС, БКИ).
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"alfahack/backend/internal/mockgov"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		resp, err := (&http.Client{Timeout: 3 * time.Second}).Get("http://127.0.0.1:" + envStr("PORT", "8090") + "/healthz")
		if err != nil || resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		os.Exit(0)
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "mockgov")

	data, err := mockgov.LoadData(time.Now)
	if err != nil {
		log.Error("load fixtures", "err", err)
		os.Exit(1)
	}
	faults := &mockgov.Faults{
		FailureRate: envFloat("MOCK_FAILURE_RATE", 0),
		LatencyMS:   envInt("MOCK_LATENCY_MS", 300),
	}
	addr := ":" + envStr("PORT", "8090")
	srv := &http.Server{
		Addr:              addr,
		Handler:           mockgov.NewServer(data, faults, log).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		log.Info("mockgov listening", "addr", addr, "failure_rate", faults.FailureRate, "latency_ms", faults.LatencyMS)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("listen", "err", err)
			os.Exit(1)
		}
	}()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

func envStr(k, def string) string {
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

func envFloat(k string, def float64) float64 {
	if v, err := strconv.ParseFloat(os.Getenv(k), 64); err == nil {
		return v
	}
	return def
}
