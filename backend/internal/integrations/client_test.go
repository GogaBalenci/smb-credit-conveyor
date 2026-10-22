package integrations

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func fastConfig(url string) Config {
	return Config{BaseURL: url, Timeout: time.Second, MaxAttempts: 3, BackoffBase: time.Millisecond, BreakerThreshold: 2, BreakerCooldown: time.Hour}
}

func TestClient_RetriesTransientErrors(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(503)
			return
		}
		_, _ = w.Write([]byte(`{"inn":"7701234560","status":"ACTIVE"}`))
	}))
	defer srv.Close()

	rec, err := NewClient(fastConfig(srv.URL), nil).Registry(t.Context(), "7701234560")
	if err != nil || rec.Status != "ACTIVE" {
		t.Fatalf("expected success after retries: %+v %v", rec, err)
	}
	if calls.Load() != 3 {
		t.Fatalf("expected 3 attempts, got %d", calls.Load())
	}
}

func TestClient_DoesNotRetryNotFound(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(404)
	}))
	defer srv.Close()

	_, err := NewClient(fastConfig(srv.URL), nil).FNS(t.Context(), "1")
	if !errors.Is(err, ErrNotFound) || calls.Load() != 1 {
		t.Fatalf("404 must not be retried: err=%v calls=%d", err, calls.Load())
	}
}

func TestClient_CircuitBreakerOpens(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(500)
	}))
	defer srv.Close()

	c := NewClient(fastConfig(srv.URL), nil)
	for i := 0; i < 2; i++ {
		if _, err := c.CreditReport(t.Context(), "1"); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("call %d: want ErrUnavailable, got %v", i, err)
		}
	}
	before := calls.Load()
	if _, err := c.CreditReport(t.Context(), "1"); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("want ErrCircuitOpen, got %v", err)
	}
	if calls.Load() != before {
		t.Fatal("open breaker must not hit the source")
	}
	// Предохранитель изолирован по источникам: ЕГРЮЛ продолжает вызываться.
	if _, err := c.Registry(t.Context(), "1"); errors.Is(err, ErrCircuitOpen) {
		t.Fatal("breakers must be per source")
	}
}

func TestBreaker_HalfOpenAfterCooldown(t *testing.T) {
	now := time.Unix(0, 0)
	b := newBreaker(1, time.Minute)
	b.now = func() time.Time { return now }
	b.failure()
	if b.allow() {
		t.Fatal("breaker should be open")
	}
	now = now.Add(2 * time.Minute)
	if !b.allow() {
		t.Fatal("breaker should allow a probe after cooldown")
	}
}

func TestCreditReportSummary(t *testing.T) {
	r := CreditReport{Loans: []Loan{
		{Status: "ACTIVE", MonthlyPayment: 100, MaxOverdueDays: 5},
		{Status: "CLOSED", MaxOverdueDays: 40},
		{Status: "ACTIVE", MonthlyPayment: 50},
	}}
	active, closed, monthly, maxOverdue := r.Summary()
	if active != 2 || closed != 1 || monthly != 150 || maxOverdue != 40 {
		t.Fatalf("got %d %d %d %d", active, closed, monthly, maxOverdue)
	}
}
