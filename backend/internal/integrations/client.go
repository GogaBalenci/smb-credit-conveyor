package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"
)

var (
	// ErrNotFound — источник не нашёл субъекта (не повторяем).
	ErrNotFound = errors.New("subject not found in source")
	// ErrUnavailable — источник недоступен после всех попыток.
	ErrUnavailable = errors.New("source unavailable")
	// ErrCircuitOpen — источник временно отключён предохранителем.
	ErrCircuitOpen = errors.New("circuit breaker open")
)

// Source — интерфейс внешних проверок. Конвейер зависит от интерфейса,
// а не от HTTP-клиента: в тестах подставляется фейк, в проде — реальные адаптеры.
type Source interface {
	Registry(ctx context.Context, inn string) (RegistryRecord, error)
	FNS(ctx context.Context, inn string) (FNSProfile, error)
	CreditReport(ctx context.Context, inn string) (CreditReport, error)
}

// Config — параметры устойчивости клиента.
type Config struct {
	BaseURL          string
	Timeout          time.Duration // таймаут одного запроса
	MaxAttempts      int           // попыток на запрос
	BackoffBase      time.Duration // база экспоненциальной задержки
	BreakerThreshold int           // подряд неудач до размыкания
	BreakerCooldown  time.Duration // сколько держать предохранитель открытым
}

// DefaultConfig — разумные значения по умолчанию.
func DefaultConfig(baseURL string) Config {
	return Config{
		BaseURL: baseURL, Timeout: 3 * time.Second, MaxAttempts: 3,
		BackoffBase: 200 * time.Millisecond, BreakerThreshold: 5, BreakerCooldown: 15 * time.Second,
	}
}

// Client — HTTP-клиент к источникам с таймаутами, ретраями и circuit breaker
// на каждый источник отдельно (падение БКИ не должно блокировать ЕГРЮЛ).
type Client struct {
	cfg      Config
	http     *http.Client
	log      *slog.Logger
	breakers map[string]*breaker
}

// NewClient создаёт клиент.
func NewClient(cfg Config, log *slog.Logger) *Client {
	if log == nil {
		log = slog.Default()
	}
	return &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: cfg.Timeout},
		log:  log,
		breakers: map[string]*breaker{
			"registry": newBreaker(cfg.BreakerThreshold, cfg.BreakerCooldown),
			"fns":      newBreaker(cfg.BreakerThreshold, cfg.BreakerCooldown),
			"bki":      newBreaker(cfg.BreakerThreshold, cfg.BreakerCooldown),
		},
	}
}

// Registry — выписка ЕГРЮЛ/ЕГРИП.
func (c *Client) Registry(ctx context.Context, inn string) (RegistryRecord, error) {
	var out RegistryRecord
	err := c.get(ctx, "registry", "/registry/"+url.PathEscape(inn), &out)
	return out, err
}

// FNS — профиль «Прозрачный бизнес».
func (c *Client) FNS(ctx context.Context, inn string) (FNSProfile, error) {
	var out FNSProfile
	err := c.get(ctx, "fns", "/fns/"+url.PathEscape(inn), &out)
	return out, err
}

// CreditReport — отчёт БКИ.
func (c *Client) CreditReport(ctx context.Context, inn string) (CreditReport, error) {
	var out CreditReport
	err := c.get(ctx, "bki", "/bki/"+url.PathEscape(inn), &out)
	return out, err
}

func (c *Client) get(ctx context.Context, source, path string, out any) error {
	br := c.breakers[source]
	if !br.allow() {
		return fmt.Errorf("%s: %w", source, ErrCircuitOpen)
	}
	var lastErr error
	for attempt := 1; attempt <= c.cfg.MaxAttempts; attempt++ {
		retry, err := c.do(ctx, path, out)
		if err == nil {
			br.success()
			return nil
		}
		if !retry {
			br.success() // «не найдено» — штатный ответ, источник жив
			return err
		}
		lastErr = err
		c.log.WarnContext(ctx, "integration call failed", "source", source, "attempt", attempt, "err", err)
		if attempt < c.cfg.MaxAttempts {
			delay := c.cfg.BackoffBase << (attempt - 1)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}
	}
	br.failure()
	return fmt.Errorf("%s: %w: %v", source, ErrUnavailable, lastErr)
}

// do выполняет один запрос; retry=true для временных ошибок (сеть, 5xx, 429).
func (c *Client) do(ctx context.Context, path string, out any) (retry bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.BaseURL+path, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "application/json")
	if rid, ok := ctx.Value(requestIDKey{}).(string); ok {
		req.Header.Set("X-Request-ID", rid)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return true, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return true, err
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return false, ErrNotFound
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return true, fmt.Errorf("status %d", resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return false, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return false, fmt.Errorf("decode: %w", err)
	}
	return false, nil
}

type requestIDKey struct{}

// WithRequestID прокидывает идентификатор запроса во внешние вызовы (сквозная трассировка).
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// breaker — минимальный circuit breaker: после N неудач подряд источник
// считается недоступным на cooldown, затем пропускается пробный запрос.
type breaker struct {
	mu        sync.Mutex
	threshold int
	cooldown  time.Duration
	failures  int
	openUntil time.Time
	now       func() time.Time
}

func newBreaker(threshold int, cooldown time.Duration) *breaker {
	return &breaker{threshold: threshold, cooldown: cooldown, now: time.Now}
}

func (b *breaker) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.now().After(b.openUntil)
}

func (b *breaker) success() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures = 0
}

func (b *breaker) failure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures++
	if b.threshold > 0 && b.failures >= b.threshold {
		b.openUntil = b.now().Add(b.cooldown)
		b.failures = 0
	}
}
