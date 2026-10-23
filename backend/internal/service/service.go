// Package service — прикладной слой: сценарии клиента и банка, конвейер.
//
// Слой связывает доменные правила (машина состояний, скоринг, политика) с
// хранилищем и интеграциями. HTTP-слой вызывает только этот пакет, поэтому
// одни и те же правила действуют для API, воркера и тестов.
package service

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"alfahack/backend/internal/assistant"
	"alfahack/backend/internal/auth"
	"alfahack/backend/internal/domain"
	"alfahack/backend/internal/integrations"
	"alfahack/backend/internal/store"
)

// Config — параметры прикладного слоя.
type Config struct {
	StepDelay   time.Duration // искусственная пауза между шагами конвейера (для наглядности демо)
	SignCodeTTL time.Duration
	TaskLease   time.Duration
	// DemoSignCode — показывать код подписания в ответе API (в проде он уходил бы
	// в push мобильного банка; email/SMS по условиям кейса в сценарий не входят).
	DemoSignCode bool
}

// DefaultConfig — значения по умолчанию.
func DefaultConfig() Config {
	return Config{StepDelay: 2 * time.Second, SignCodeTTL: 5 * time.Minute, TaskLease: time.Minute, DemoSignCode: true}
}

// Service — прикладной сервис.
type Service struct {
	store  *store.Store
	src    integrations.Source
	tokens *auth.Issuer
	cfg    Config
	log    *slog.Logger
	now    func() time.Time

	summarizer assistant.Summarizer
	summaries  summaryCache
}

// New создаёт сервис.
func New(st *store.Store, src integrations.Source, tokens *auth.Issuer, cfg Config, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{store: st, src: src, tokens: tokens, cfg: cfg, log: log, now: time.Now, summarizer: assistant.Template{}}
}

// Задачи конвейера.
const (
	TaskChecks   = "checks"
	TaskScore    = "score"
	TaskDisburse = "disburse"
)

// ErrSourceUnavailable — внешний источник недоступен (HTTP 503 на синхронных вызовах).
var ErrSourceUnavailable = errors.New("external source unavailable")

// transitionReq — запрос на изменение статуса.
type transitionReq struct {
	appID   string
	to      domain.Status
	actor   domain.ActorKind
	actorID string
	comment string
	mutate  func(a *domain.Application) error // изменения полей вместе с переходом
	task    string                            // задача конвейера, ставится атомарно с переходом
	expect  []domain.Status                   // если задано — переход только из этих статусов
	extra   func(q *store.Queries) error      // дополнительные записи в той же транзакции
}

// transition — единственная точка изменения статуса заявки. В одной транзакции:
// блокировка строки, проверка машины состояний, изменение полей, запись события
// в журнал, постановка следующей задачи конвейера.
func (s *Service) transition(ctx context.Context, r transitionReq) (domain.Application, error) {
	var out domain.Application
	err := s.store.InTx(ctx, func(q *store.Queries) error {
		app, err := q.LockApplication(ctx, r.appID)
		if err != nil {
			return err
		}
		if len(r.expect) > 0 && !statusIn(app.Status, r.expect) {
			return domain.ErrInvalidTransition
		}
		if err := domain.ValidateTransition(app.Status, r.to, r.actor); err != nil {
			return err
		}
		from := app.Status
		if r.mutate != nil {
			if err := r.mutate(&app); err != nil {
				return err
			}
		}
		app.Status = r.to
		saved, err := q.SaveApplication(ctx, app)
		if err != nil {
			return err
		}
		if err := q.InsertEvent(ctx, domain.Event{
			ApplicationID: app.ID, From: from, To: r.to, ActorKind: r.actor, ActorID: r.actorID, Comment: r.comment,
		}); err != nil {
			return err
		}
		if r.extra != nil {
			if err := r.extra(q); err != nil {
				return err
			}
		}
		if r.task != "" {
			if err := q.EnqueueTask(ctx, r.task, app.ID, s.cfg.StepDelay); err != nil {
				return err
			}
		}
		out = saved
		return nil
	})
	if err == nil {
		s.log.InfoContext(ctx, "application transition", "app", r.appID, "to", r.to, "actor", r.actor)
	}
	return out, err
}

// authorize загружает заявку с проверкой прав: клиент видит только заявки
// своей компании. Чужая заявка выглядит как несуществующая (не раскрываем факт её наличия).
func (s *Service) authorize(ctx context.Context, p auth.Principal, appID string) (domain.Application, error) {
	app, err := s.store.GetApplication(ctx, appID)
	if err != nil {
		return app, err
	}
	switch p.Role {
	case domain.RoleUnderwriter:
		return app, nil
	case domain.RoleClient:
		if app.CompanyID == p.CompanyID && p.CompanyID != "" {
			return app, nil
		}
	}
	return domain.Application{}, domain.ErrNotFound
}

func requireRole(p auth.Principal, role domain.Role) error {
	if p.Role != role {
		return domain.ErrForbidden
	}
	return nil
}

func statusIn(s domain.Status, list []domain.Status) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
