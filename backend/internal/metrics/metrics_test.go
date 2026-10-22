package metrics

import (
	"strings"
	"testing"
)

func TestRegistry(t *testing.T) {
	r := New()
	r.Inc("http_requests_total", "HTTP requests", "route", "/a", "code", "200")
	r.Inc("http_requests_total", "HTTP requests", "route", "/a", "code", "200")
	r.Inc("http_requests_total", "HTTP requests", "route", `/b"x`, "code", "500")
	r.Gauge("queue_depth", "Pending tasks", func() map[string]float64 { return map[string]float64{"": 3} })

	if v := r.Value("http_requests_total", "route", "/a", "code", "200"); v != 2 {
		t.Fatalf("value %v", v)
	}
	var b strings.Builder
	r.Write(&b)
	out := b.String()
	for _, want := range []string{
		"# TYPE http_requests_total counter",
		`http_requests_total{route="/a",code="200"} 2`,
		`http_requests_total{route="/b\"x",code="500"} 1`,
		"queue_depth 3",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
