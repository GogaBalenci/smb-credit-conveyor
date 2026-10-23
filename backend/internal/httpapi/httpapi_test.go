package httpapi_test

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"alfahack/backend/internal/httpapi"
	"alfahack/backend/internal/metrics"
	"alfahack/backend/internal/testutil"
)

type client struct {
	t     *testing.T
	base  string
	token string
}

func (c *client) do(method, path string, body any, headers ...string) (*http.Response, map[string]any) {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func setup(t *testing.T) (*testutil.Env, *httptest.Server) {
	env := testutil.Setup(t)
	srv := httptest.NewServer(httpapi.New(env.Service, env.Store, env.Tokens, httpapi.Config{
		CORSOrigins: []string{"http://localhost:5173"}, DemoMode: true, MockAdminURL: env.Mock.URL,
	}, env.Log).Handler())
	t.Cleanup(srv.Close)
	return env, srv
}

func loginAs(t *testing.T, base, email string) *client {
	c := &client{t: t, base: base}
	resp, body := c.do("POST", "/api/v1/auth/login", map[string]string{"email": email, "password": "demo12345"})
	if resp.StatusCode != 200 {
		t.Fatalf("login %s: %d %v", email, resp.StatusCode, body)
	}
	c.token = body["token"].(string)
	return c
}

func TestHTTP_FullClientFlow(t *testing.T) {
	env, srv := setup(t)
	c := loginAs(t, srv.URL, "approve@demo.ru")

	resp, rec := c.do("POST", "/api/v1/recommendations", map[string]any{"purpose": "working_capital", "amount": 2000000, "term_months": 24})
	if resp.StatusCode != 200 || len(rec["items"].([]any)) == 0 {
		t.Fatalf("recommendations: %d %v", resp.StatusCode, rec)
	}
	_, pre := c.do("GET", "/api/v1/prefill", nil)
	fin := pre["financials"]

	draft := map[string]any{"product_code": "EXPRESS", "purpose": "working_capital", "amount": 2000000, "term_months": 24, "financials": fin}
	resp, app := c.do("POST", "/api/v1/applications", draft, "Idempotency-Key", "create-1")
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d %v", resp.StatusCode, app)
	}
	id := app["id"].(string)

	// Повтор с тем же ключом не создаёт вторую заявку.
	resp, again := c.do("POST", "/api/v1/applications", draft, "Idempotency-Key", "create-1")
	if resp.Header.Get("Idempotent-Replayed") != "true" || again["id"] != id {
		t.Fatalf("idempotent replay failed: %v", again)
	}
	// Тот же ключ с другим телом — ошибка.
	if resp, _ := c.do("POST", "/api/v1/applications", map[string]any{"product_code": "OVERDRAFT"}, "Idempotency-Key", "create-1"); resp.StatusCode != 422 {
		t.Fatalf("key reuse with different body must fail, got %d", resp.StatusCode)
	}

	if resp, body := c.do("POST", "/api/v1/applications/"+id+"/submit", nil); resp.StatusCode != 200 {
		t.Fatalf("submit: %d %v", resp.StatusCode, body)
	}
	env.Drain(t)

	resp, det := c.do("GET", "/api/v1/applications/"+id, nil)
	if resp.StatusCode != 200 || resp.Header.Get("ETag") == "" {
		t.Fatalf("details: %d", resp.StatusCode)
	}
	a := det["application"].(map[string]any)
	if a["status"] != "APPROVED" {
		t.Fatalf("want APPROVED, got %v", a["status"])
	}

	_, ch := c.do("POST", "/api/v1/applications/"+id+"/sign-code", nil)
	if resp, body := c.do("POST", "/api/v1/applications/"+id+"/sign", map[string]string{"code": ch["demo_code"].(string)}); resp.StatusCode != 200 {
		t.Fatalf("sign: %d %v", resp.StatusCode, body)
	}
	env.Drain(t)
	_, det = c.do("GET", "/api/v1/applications/"+id, nil)
	if det["application"].(map[string]any)["status"] != "ISSUED" {
		t.Fatal("loan must be issued")
	}

	_, feed := c.do("GET", "/api/v1/feed", nil)
	if feed["profile"].(map[string]any)["stage"] != "issued" {
		t.Fatalf("feed profile: %v", feed["profile"])
	}
}

// Фронтенд ожидает массивы, а не null: пустые списки должны сериализоваться как [].
func TestHTTP_NoNullArrays(t *testing.T) {
	env, srv := setup(t)
	c := loginAs(t, srv.URL, "reject@demo.ru")
	raw := func(method, path string, body any) string {
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, srv.URL+path, rd)
		req.Header.Set("Authorization", "Bearer "+c.token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	check := func(name, body string) {
		for _, k := range []string{"warnings", "reasons", "suggestions", "stop_factors", "reason_codes", "required_docs", "items", "events", "checks", "documents", "actions", "sources"} {
			if strings.Contains(body, `"`+k+`":null`) {
				t.Errorf("%s: %q is null", name, k)
			}
		}
	}
	check("recommendations", raw("POST", "/api/v1/recommendations", map[string]any{"purpose": "cash_gap", "amount": 1000000, "term_months": 6}))
	_, pre := c.do("GET", "/api/v1/prefill", nil)
	_, app := c.do("POST", "/api/v1/applications", map[string]any{"product_code": "EXPRESS", "purpose": "cash_gap", "amount": 1000000, "term_months": 12, "financials": pre["financials"]})
	id := app["id"].(string)
	check("draft", raw("GET", "/api/v1/applications/"+id, nil))
	c.do("POST", "/api/v1/applications/"+id+"/submit", nil)
	env.Drain(t)
	check("rejected", raw("GET", "/api/v1/applications/"+id, nil))
	check("list", raw("GET", "/api/v1/applications", nil))
	check("feed", raw("GET", "/api/v1/feed", nil))
}

func TestHTTP_DocumentUploadMultipart(t *testing.T) {
	env, srv := setup(t)
	c := loginAs(t, srv.URL, "review@demo.ru")
	_, pre := c.do("GET", "/api/v1/prefill", nil)
	_, app := c.do("POST", "/api/v1/applications", map[string]any{"product_code": "EXPRESS", "purpose": "cash_gap", "amount": 1000000, "term_months": 24, "financials": pre["financials"]})
	id := app["id"].(string)
	c.do("POST", "/api/v1/applications/"+id+"/submit", nil)
	env.Drain(t)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("kind", "bank_statement")
	fw, _ := mw.CreateFormFile("file", "vypiska.pdf")
	_, _ = fw.Write([]byte("%PDF-1.4\n%%EOF"))
	_ = mw.Close()
	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/applications/"+id+"/documents", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 201 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("upload: %v %d %s", err, resp.StatusCode, b)
	}
	if resp, body := c.do("POST", "/api/v1/applications/"+id+"/submit", nil); resp.StatusCode != 200 {
		t.Fatalf("resubmit after upload: %d %v", resp.StatusCode, body)
	}
}

func TestHTTP_AuthAndErrors(t *testing.T) {
	_, srv := setup(t)
	anon := &client{t: t, base: srv.URL}

	resp, body := anon.do("GET", "/api/v1/applications", nil)
	if resp.StatusCode != 401 || body["error"].(map[string]any)["code"] != "unauthorized" {
		t.Fatalf("anonymous access must be 401 with error body: %d %v", resp.StatusCode, body)
	}
	if resp.Header.Get("X-Request-ID") == "" {
		t.Fatal("every response must carry X-Request-ID")
	}
	if resp, _ := anon.do("GET", "/api/v1/applications", nil, "Authorization", "Bearer garbage"); resp.StatusCode != 401 {
		t.Fatal("invalid token must be 401")
	}
	if resp, _ := anon.do("POST", "/api/v1/auth/login", map[string]string{"email": "approve@demo.ru", "password": "nope"}); resp.StatusCode != 401 {
		t.Fatal("wrong password must be 401")
	}
	if resp, _ := anon.do("POST", "/api/v1/auth/login", map[string]any{"email": "x", "unknown": 1}); resp.StatusCode != 400 {
		t.Fatal("unknown JSON fields must be rejected")
	}

	client := loginAs(t, srv.URL, "approve@demo.ru")
	resp, body = client.do("POST", "/api/v1/applications", map[string]any{"product_code": "EXPRESS", "amount": 999})
	if resp.StatusCode != 422 || body["error"].(map[string]any)["fields"].(map[string]any)["amount"] == nil {
		t.Fatalf("validation error must list fields: %d %v", resp.StatusCode, body)
	}
	// Клиент не имеет доступа к рабочему месту банка.
	if resp, _ := client.do("GET", "/api/v1/stats", nil); resp.StatusCode != 403 {
		t.Fatalf("client must not see stats: %d", resp.StatusCode)
	}
	if resp, _ := client.do("POST", "/api/v1/admin/faults", map[string]any{"failure_rate": 1}); resp.StatusCode != 403 {
		t.Fatal("client must not control faults")
	}
	if resp, _ := client.do("GET", "/api/v1/applications/00000000-0000-4000-8000-000000000000", nil); resp.StatusCode != 404 {
		t.Fatal("unknown application must be 404")
	}

	uw := loginAs(t, srv.URL, "underwriter@demo.ru")
	if resp, body := uw.do("GET", "/api/v1/stats", nil); resp.StatusCode != 200 {
		t.Fatalf("stats: %d %v", resp.StatusCode, body)
	}
	if resp, body := uw.do("POST", "/api/v1/admin/faults", map[string]any{"failure_rate": 0.3}); resp.StatusCode != 200 || body["failure_rate"] != 0.3 {
		t.Fatalf("faults proxy: %d %v", resp.StatusCode, body)
	}
}

func TestHTTP_SummaryForUnderwriterOnly(t *testing.T) {
	env, srv := setup(t)
	c := loginAs(t, srv.URL, "review@demo.ru")
	_, pre := c.do("GET", "/api/v1/prefill", nil)
	_, app := c.do("POST", "/api/v1/applications", map[string]any{"product_code": "EXPRESS", "purpose": "cash_gap", "amount": 1000000, "term_months": 24, "financials": pre["financials"]})
	id := app["id"].(string)
	c.do("POST", "/api/v1/applications/"+id+"/submit", nil)
	env.Drain(t)

	if resp, _ := c.do("GET", "/api/v1/applications/"+id+"/summary", nil); resp.StatusCode != 403 {
		t.Fatalf("client must not get underwriter summary, got %d", resp.StatusCode)
	}
	uw := loginAs(t, srv.URL, "underwriter@demo.ru")
	resp, sum := uw.do("GET", "/api/v1/applications/"+id+"/summary", nil)
	if resp.StatusCode != 200 || sum["source"] != "template" {
		t.Fatalf("summary: %d %v", resp.StatusCode, sum)
	}
	text, _ := sum["text"].(string)
	for _, want := range []string{"Профиль: ООО «Хлебный дом»", "Что проверить:"} {
		if !strings.Contains(text, want) {
			t.Errorf("summary misses %q:\n%s", want, text)
		}
	}
}

func TestHTTP_CORSAndOps(t *testing.T) {
	_, srv := setup(t)
	req, _ := http.NewRequest("OPTIONS", srv.URL+"/api/v1/applications", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 204 || resp.Header.Get("Access-Control-Allow-Origin") != "http://localhost:5173" {
		t.Fatalf("preflight: %d %v", resp.StatusCode, resp.Header)
	}
	req.Header.Set("Origin", "https://evil.example")
	resp, _ = http.DefaultClient.Do(req)
	if resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("unknown origin must not be allowed")
	}
	for _, p := range []string{"/healthz", "/readyz", "/api/openapi.yaml", "/api/docs", "/api/v1/meta"} {
		r, err := http.Get(srv.URL + p)
		if err != nil || r.StatusCode != 200 {
			t.Fatalf("%s: %v %d", p, err, r.StatusCode)
		}
	}
	r, _ := http.Get(srv.URL + "/metrics")
	b, _ := io.ReadAll(r.Body)
	if !strings.Contains(string(b), `http_requests_total{route="GET /healthz",code="200"}`) {
		t.Fatalf("metrics must be labeled by route pattern:\n%s", b)
	}
	_ = metrics.Default
}

func TestHTTP_LoginRateLimit(t *testing.T) {
	_, srv := setup(t)
	c := &client{t: t, base: srv.URL}
	var last int
	for i := 0; i < 25; i++ {
		resp, _ := c.do("POST", "/api/v1/auth/login", map[string]string{"email": "x@y.ru", "password": "bad"})
		last = resp.StatusCode
	}
	if last != 429 {
		t.Fatalf("brute force must be rate limited, last status %d", last)
	}
}
