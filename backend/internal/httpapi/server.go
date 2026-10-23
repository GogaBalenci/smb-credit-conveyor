// Package httpapi — REST API кредитного конвейера.
package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"alfahack/backend/api"
	"alfahack/backend/internal/auth"
	"alfahack/backend/internal/metrics"
	"alfahack/backend/internal/service"
	"alfahack/backend/internal/store"
)

// Config — параметры HTTP-слоя.
type Config struct {
	CORSOrigins  []string
	MockAdminURL string // адрес симулятора для управления сбоями из UI андеррайтера (демо)
	DemoMode     bool   // показывать демо-учётки на /meta
	StaticDir    string // собранный фронтенд (если отдаём им же)
}

// Server — HTTP-сервер API.
type Server struct {
	svc    *service.Service
	store  *store.Store
	tokens *auth.Issuer
	cfg    Config
	log    *slog.Logger
	http   *http.Client
}

// New создаёт сервер.
func New(svc *service.Service, st *store.Store, tokens *auth.Issuer, cfg Config, log *slog.Logger) *Server {
	s := &Server{svc: svc, store: st, tokens: tokens, cfg: cfg, log: log, http: &http.Client{Timeout: 3 * time.Second}}
	metrics.Default.Gauge("conveyor_queue_tasks", "Задачи в очереди конвейера", func() map[string]float64 {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		qs, err := st.TaskStats(ctx)
		if err != nil {
			return nil
		}
		return map[string]float64{
			metrics.Label("state", "pending"):  float64(qs.Pending),
			metrics.Label("state", "retrying"): float64(qs.Retries),
			metrics.Label("state", "failed"):   float64(qs.Failed),
		}
	})
	return s
}

// Handler собирает маршруты и middleware.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	authLimit := newRateLimiter(20, time.Minute)
	a := s.authenticated

	// Служебные
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /readyz", s.ready)
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		metrics.Default.Write(w)
	})
	mux.HandleFunc("GET /api/openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(api.Spec)
	})
	mux.HandleFunc("GET /api/docs", swaggerUI)

	// Справочники и аутентификация
	mux.HandleFunc("GET /api/v1/meta", s.meta)
	mux.HandleFunc("POST /api/v1/auth/register", authLimit.wrap(s.register))
	mux.HandleFunc("POST /api/v1/auth/login", authLimit.wrap(s.login))
	mux.HandleFunc("GET /api/v1/auth/me", a(s.me))
	mux.HandleFunc("GET /api/v1/companies/lookup", authLimit.wrap(s.lookupCompany))

	// Клиентский путь
	mux.HandleFunc("POST /api/v1/recommendations", s.recommend)
	mux.HandleFunc("GET /api/v1/prefill", a(s.prefill))
	mux.HandleFunc("GET /api/v1/feed", a(s.feed))
	mux.HandleFunc("GET /api/v1/applications", a(s.listApplications))
	mux.HandleFunc("POST /api/v1/applications", a(s.idempotent(s.createApplication)))
	mux.HandleFunc("GET /api/v1/applications/{id}", a(s.getApplication))
	mux.HandleFunc("PUT /api/v1/applications/{id}", a(s.updateApplication))
	mux.HandleFunc("POST /api/v1/applications/{id}/submit", a(s.idempotent(s.submit)))
	mux.HandleFunc("POST /api/v1/applications/{id}/cancel", a(s.cancel))
	mux.HandleFunc("POST /api/v1/applications/{id}/documents", a(s.uploadDocument))
	mux.HandleFunc("GET /api/v1/applications/{id}/documents/{docId}", a(s.downloadDocument))
	mux.HandleFunc("POST /api/v1/applications/{id}/sign-code", a(s.signCode))
	mux.HandleFunc("POST /api/v1/applications/{id}/sign", a(s.idempotent(s.sign)))
	mux.HandleFunc("POST /api/v1/applications/{id}/decline", a(s.decline))

	// Рабочее место банка
	mux.HandleFunc("POST /api/v1/applications/{id}/take", a(s.take))
	mux.HandleFunc("POST /api/v1/applications/{id}/decision", a(s.idempotent(s.decision)))
	mux.HandleFunc("GET /api/v1/applications/{id}/summary", a(s.summary))
	mux.HandleFunc("GET /api/v1/stats", a(s.stats))
	mux.HandleFunc("GET /api/v1/admin/faults", a(s.faults))
	mux.HandleFunc("POST /api/v1/admin/faults", a(s.faults))

	if s.cfg.StaticDir != "" {
		mux.Handle("/", spaHandler(s.cfg.StaticDir))
	}

	var h http.Handler = mux
	h = withSecurityHeaders(h)
	h = withCORS(s.cfg.CORSOrigins, h)
	h = withRecover(s.log, h)
	h = withLogging(s.log, h)
	h = withRequestID(h)
	return h
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "db unavailable"})
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ready"})
}

// spaHandler отдаёт собранный фронтенд; неизвестные пути — index.html (клиентский роутинг).
func spaHandler(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path != "/" && !strings.Contains(r.URL.Path[strings.LastIndex(r.URL.Path, "/"):], ".") {
			r.URL.Path = "/"
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		fs.ServeHTTP(w, r)
	})
}

func swaggerUI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, `<!doctype html><html><head><meta charset="utf-8"><title>Кредитный конвейер — API</title>
<link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5.17.14/swagger-ui.css"></head>
<body><div id="ui"></div><script src="https://unpkg.com/swagger-ui-dist@5.17.14/swagger-ui-bundle.js"></script>
<script>SwaggerUIBundle({url:'/api/openapi.yaml',dom_id:'#ui',persistAuthorization:true});</script></body></html>`)
}
