package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"alfahack/backend/internal/domain"
	"alfahack/backend/internal/service"
)

// apiError — единый формат ошибки API.
type apiError struct {
	Code      string            `json:"code"`
	Message   string            `json:"message"`
	Fields    map[string]string `json:"fields,omitempty"`
	RequestID string            `json:"request_id,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError отображает доменные ошибки в HTTP-коды. Внутренние детали
// (SQL, стектрейсы) клиенту не отдаются — только в лог с request_id.
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	rid := requestID(r.Context())
	var ve *domain.ValidationError
	e := apiError{RequestID: rid}
	status := http.StatusInternalServerError
	switch {
	case errors.As(err, &ve):
		status, e.Code, e.Message, e.Fields = http.StatusUnprocessableEntity, "validation_error", "Проверьте введённые данные", ve.Fields
	case errors.Is(err, domain.ErrNotFound):
		status, e.Code, e.Message = http.StatusNotFound, "not_found", "Не найдено"
	case errors.Is(err, domain.ErrUnauthorized):
		status, e.Code, e.Message = http.StatusUnauthorized, "unauthorized", "Неверные учётные данные или сессия истекла"
	case errors.Is(err, domain.ErrForbidden):
		status, e.Code, e.Message = http.StatusForbidden, "forbidden", "Недостаточно прав"
	case errors.Is(err, domain.ErrInvalidTransition):
		status, e.Code, e.Message = http.StatusConflict, "invalid_transition", "Действие недоступно в текущем статусе заявки"
	case errors.Is(err, domain.ErrConflict):
		status, e.Code, e.Message = http.StatusConflict, "version_conflict", "Заявка изменилась — обновите страницу"
	case errors.Is(err, service.ErrSourceUnavailable):
		status, e.Code, e.Message = http.StatusServiceUnavailable, "source_unavailable", "Внешний источник временно недоступен, попробуйте позже"
	case errors.Is(err, errBadRequest):
		status, e.Code, e.Message = http.StatusBadRequest, "bad_request", err.Error()
	default:
		e.Code, e.Message = "internal_error", "Внутренняя ошибка"
	}
	if status >= 500 {
		slog.ErrorContext(r.Context(), "request failed", "err", err, "path", r.URL.Path, "request_id", rid)
	} else if errors.Is(err, domain.ErrInvalidTransition) {
		e.Message = err.Error() // текст перехода полезен клиенту и не раскрывает внутренностей
	}
	writeJSON(w, status, map[string]apiError{"error": e})
}

var errBadRequest = errors.New("bad request")

func badRequest(msg string) error { return errors.Join(errBadRequest, errors.New(msg)) }

// decodeJSON читает тело с ограничением размера и запретом неизвестных полей.
func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return badRequest("некорректный JSON: " + err.Error())
	}
	return nil
}
