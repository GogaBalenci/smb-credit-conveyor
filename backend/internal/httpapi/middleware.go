package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"alfahack/backend/internal/auth"
	"alfahack/backend/internal/domain"
	"alfahack/backend/internal/integrations"
	"alfahack/backend/internal/metrics"
	"alfahack/backend/internal/store"
)

type ctxKey int

const (
	keyRequestID ctxKey = iota
	keyPrincipal
)

func requestID(ctx context.Context) string {
	id, _ := ctx.Value(keyRequestID).(string)
	return id
}

func principal(ctx context.Context) (auth.Principal, bool) {
	p, ok := ctx.Value(keyPrincipal).(auth.Principal)
	return p, ok
}

// withRequestID присваивает каждому запросу идентификатор и прокидывает его
// в логи, ответ и вызовы внешних источников (сквозная трассировка).
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" || len(id) > 64 {
			b := make([]byte, 8)
			_, _ = rand.Read(b)
			id = hex.EncodeToString(b)
		}
		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), keyRequestID, id)
		ctx = integrations.WithRequestID(ctx, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) { s.status = code; s.ResponseWriter.WriteHeader(code) }
func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = 200
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// withLogging пишет структурный лог и метрики по каждому запросу.
func withLogging(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		metrics.Default.Inc("http_requests_total", "HTTP-запросы", "route", route, "code", strconv.Itoa(rec.status))
		metrics.Default.Add("http_request_duration_seconds_sum", "Суммарная длительность HTTP-запросов", time.Since(start).Seconds(), "route", route)
		if r.URL.Path == "/healthz" || r.URL.Path == "/metrics" {
			return
		}
		log.Info("http", "method", r.Method, "path", r.URL.Path, "status", rec.status,
			"ms", time.Since(start).Milliseconds(), "request_id", requestID(r.Context()))
	})
}

// withRecover превращает панику в 500, не роняя процесс.
func withRecover(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				log.Error("panic", "value", v, "stack", string(debug.Stack()), "request_id", requestID(r.Context()))
				writeJSON(w, 500, map[string]apiError{"error": {Code: "internal_error", Message: "Внутренняя ошибка", RequestID: requestID(r.Context())}})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// withSecurityHeaders — базовые заголовки безопасности.
func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// withCORS разрешает запросы только с перечисленных origin (фронт в dev-режиме).
func withCORS(origins []string, next http.Handler) http.Handler {
	allowed := map[string]bool{}
	for _, o := range origins {
		if o = strings.TrimSpace(o); o != "" {
			allowed[o] = true
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" && (allowed[o] || allowed["*"]) {
			w.Header().Set("Access-Control-Allow-Origin", o)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key, If-Match, X-Request-ID")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Expose-Headers", "X-Request-ID, ETag")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// authenticated проверяет Bearer-токен.
func (s *Server) authenticated(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		token, ok := strings.CutPrefix(h, "Bearer ")
		if !ok || token == "" {
			writeError(w, r, domain.ErrUnauthorized)
			return
		}
		p, err := s.tokens.Parse(token)
		if err != nil {
			writeError(w, r, domain.ErrUnauthorized)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), keyPrincipal, p)))
	}
}

// rateLimiter — простой ограничитель по IP (фиксированное окно) против перебора паролей.
type rateLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	hits   map[string]*window
}

type window struct {
	start time.Time
	count int
}

func newRateLimiter(limit int, per time.Duration) *rateLimiter {
	return &rateLimiter{limit: limit, window: per, hits: map[string]*window{}}
}

func (l *rateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if len(l.hits) > 10_000 { // защита от разрастания
		l.hits = map[string]*window{}
	}
	w := l.hits[key]
	if w == nil || now.Sub(w.start) > l.window {
		l.hits[key] = &window{start: now, count: 1}
		return true
	}
	w.count++
	return w.count <= l.limit
}

func (l *rateLimiter) wrap(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}
		if !l.allow(ip) {
			w.Header().Set("Retry-After", strconv.Itoa(int(l.window.Seconds())))
			writeJSON(w, http.StatusTooManyRequests, map[string]apiError{"error": {Code: "rate_limited", Message: "Слишком много попыток, попробуйте позже"}})
			return
		}
		next(w, r)
	}
}

// idempotent — повтор POST с тем же Idempotency-Key возвращает сохранённый ответ
// вместо повторного действия (защита от двойной отправки заявки при сбое сети).
func (s *Server) idempotent(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		p, ok := principal(r.Context())
		if key == "" || !ok {
			next(w, r)
			return
		}
		if len(key) > 128 {
			writeError(w, r, badRequest("Idempotency-Key слишком длинный"))
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			writeError(w, r, badRequest("тело запроса слишком большое"))
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		sum := sha256.Sum256(append([]byte(r.Method+" "+r.URL.Path+"\n"), body...))
		hash := hex.EncodeToString(sum[:])

		prev, err := s.store.GetIdempotent(r.Context(), p.UserID, key)
		if err != nil {
			writeError(w, r, err)
			return
		}
		if prev != nil {
			if prev.RequestHash != hash {
				writeJSON(w, http.StatusUnprocessableEntity, map[string]apiError{"error": {Code: "idempotency_mismatch",
					Message: "Ключ идемпотентности уже использован для другого запроса"}})
				return
			}
			w.Header().Set("Idempotent-Replayed", "true")
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(prev.StatusCode)
			_, _ = w.Write(prev.Body)
			return
		}
		rec := &captureWriter{ResponseWriter: w, status: 200}
		next(rec, r)
		if rec.status < 500 {
			if err := s.store.SaveIdempotent(context.WithoutCancel(r.Context()), p.UserID, key,
				store.IdempotentResponse{RequestHash: hash, StatusCode: rec.status, Body: rec.buf.Bytes()}); err != nil {
				s.log.Error("save idempotency key", "err", err)
			}
		}
	}
}

type captureWriter struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
}

func (c *captureWriter) WriteHeader(code int) { c.status = code; c.ResponseWriter.WriteHeader(code) }
func (c *captureWriter) Write(b []byte) (int, error) {
	c.buf.Write(b)
	return c.ResponseWriter.Write(b)
}
