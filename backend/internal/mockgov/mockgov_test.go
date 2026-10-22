package mockgov

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"alfahack/backend/internal/integrations"
)

var fixedNow = func() time.Time { return time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC) }

func newTestServer(t *testing.T, failureRate float64) *httptest.Server {
	t.Helper()
	data, err := LoadData(fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewServer(data, &Faults{FailureRate: failureRate}, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler())
	t.Cleanup(srv.Close)
	return srv
}

func TestFixturesLoadAndAreValid(t *testing.T) {
	data, err := LoadData(fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Personas()) < 5 {
		t.Fatalf("expected demo personas, got %d", len(data.Personas()))
	}
	reg := data.Registry("7701234560")
	if reg.ShortName != "ООО «Северный ветер»" || reg.Kind != "LEGAL" {
		t.Fatalf("unexpected registry record %+v", reg)
	}
	if fns := data.FNS("7701234560"); fns.Reports[0].Year != 2025 {
		t.Fatalf("last report year must be relative to now: %+v", fns.Reports)
	}
}

func TestGeneratedProfileIsDeterministic(t *testing.T) {
	data, _ := LoadData(fixedNow)
	inn := "7728168971"
	if !reflect.DeepEqual(data.Registry(inn), data.Registry(inn)) || !reflect.DeepEqual(data.CreditReport(inn), data.CreditReport(inn)) {
		t.Fatal("generated profile must be deterministic for the same INN")
	}
	ip := data.Registry("500100732259")
	if ip.Kind != "INDIVIDUAL" || !strings.HasPrefix(ip.ShortName, "ИП ") {
		t.Fatalf("12-digit INN must be an individual entrepreneur: %+v", ip)
	}
}

func TestHTTP_InvalidINNIs404(t *testing.T) {
	srv := newTestServer(t, 0)
	resp, err := http.Get(srv.URL + "/registry/1234567890")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 404 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestHTTP_AdminFaults(t *testing.T) {
	srv := newTestServer(t, 0)
	resp, err := http.Post(srv.URL+"/admin/faults", "application/json", strings.NewReader(`{"failure_rate":1}`))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out["failure_rate"].(float64) != 1 {
		t.Fatalf("faults not applied: %v", out)
	}
	r2, _ := http.Get(srv.URL + "/fns/7701234560")
	if r2.StatusCode != 503 {
		t.Fatalf("expected injected 503, got %d", r2.StatusCode)
	}
}

// Контрактный тест: клиент интеграций корректно разбирает ответы симулятора.
func TestContract_ClientAgainstMock(t *testing.T) {
	srv := newTestServer(t, 0)
	c := integrations.NewClient(integrations.DefaultConfig(srv.URL), nil)
	ctx := t.Context()

	reg, err := c.Registry(ctx, "165987654329")
	if err != nil || reg.Kind != "INDIVIDUAL" || reg.OKVED != "47.71" {
		t.Fatalf("registry: %+v %v", reg, err)
	}
	fns, err := c.FNS(ctx, "165987654329")
	if err != nil || len(fns.Reports) != 2 || fns.Reports[0].Revenue != 14_400_000 {
		t.Fatalf("fns: %+v %v", fns, err)
	}
	bki, err := c.CreditReport(ctx, "7802345676")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, maxOverdue := bki.Summary(); maxOverdue < 90 {
		t.Fatalf("expected severe overdue persona, got %d", maxOverdue)
	}
	if _, err := c.Registry(ctx, "0000000001"); err == nil {
		t.Fatal("invalid INN must return error")
	}
}
