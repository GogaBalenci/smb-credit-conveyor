package domain

import "errors"

// Доменные ошибки. HTTP-слой отображает их в коды ответа (см. httpapi/errors.go).
var (
	ErrNotFound          = errors.New("not found")
	ErrForbidden         = errors.New("forbidden")
	ErrUnauthorized      = errors.New("unauthorized")
	ErrInvalidTransition = errors.New("invalid status transition")
	ErrConflict          = errors.New("conflict: resource was modified concurrently")
	ErrAlreadyExists     = errors.New("already exists")
)

// ValidationError — ошибка проверки входных данных с привязкой к полям.
type ValidationError struct {
	Fields map[string]string `json:"fields"`
}

func (e *ValidationError) Error() string { return "validation failed" }

// NewValidation создаёт пустую ошибку валидации; добавляйте поля через Add.
func NewValidation() *ValidationError { return &ValidationError{Fields: map[string]string{}} }

// Add добавляет ошибку для поля.
func (e *ValidationError) Add(field, msg string) { e.Fields[field] = msg }

// OrNil возвращает nil, если ошибок нет (удобно в конце валидации).
func (e *ValidationError) OrNil() error {
	if len(e.Fields) == 0 {
		return nil
	}
	return e
}
