package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"alfahack/backend/internal/domain"
)

// SaveCheck сохраняет (перезаписывает) результат проверки источника.
func (q *Queries) SaveCheck(ctx context.Context, appID string, c domain.CheckResult) error {
	payload := c.Payload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO check_results (application_id, source, status, summary, payload, checked_at)
		VALUES ($1,$2,$3,$4,$5, now())
		ON CONFLICT (application_id, source) DO UPDATE SET status=EXCLUDED.status, summary=EXCLUDED.summary,
			payload=EXCLUDED.payload, checked_at=now()`,
		appID, c.Source, c.Status, c.Summary, []byte(payload))
	return err
}

// ListChecks возвращает результаты проверок по заявке.
func (q *Queries) ListChecks(ctx context.Context, appID string) ([]domain.CheckResult, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT source, status, summary, payload, checked_at
		FROM check_results WHERE application_id=$1 ORDER BY source`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.CheckResult{}
	for rows.Next() {
		var c domain.CheckResult
		var payload []byte
		if err := rows.Scan(&c.Source, &c.Status, &c.Summary, &payload, &c.CheckedAt); err != nil {
			return nil, err
		}
		c.Payload = payload
		out = append(out, c)
	}
	return out, rows.Err()
}

// CreateDocument сохраняет документ.
func (q *Queries) CreateDocument(ctx context.Context, d domain.Document, content []byte) (domain.Document, error) {
	if d.ID == "" {
		d.ID = domain.NewID()
	}
	err := q.db.QueryRowContext(ctx, `
		INSERT INTO documents (id, application_id, kind, filename, content_type, size_bytes, sha256, content, uploaded_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING uploaded_at`,
		d.ID, d.ApplicationID, d.Kind, d.Filename, d.ContentType, d.SizeBytes, d.SHA256, content, d.UploadedBy).Scan(&d.UploadedAt)
	return d, err
}

// ListDocuments — метаданные документов заявки.
func (q *Queries) ListDocuments(ctx context.Context, appID string) ([]domain.Document, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT id, application_id, kind, filename, content_type, size_bytes, sha256,
		uploaded_by, uploaded_at FROM documents WHERE application_id=$1 ORDER BY uploaded_at`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Document{}
	for rows.Next() {
		var d domain.Document
		if err := rows.Scan(&d.ID, &d.ApplicationID, &d.Kind, &d.Filename, &d.ContentType, &d.SizeBytes, &d.SHA256,
			&d.UploadedBy, &d.UploadedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetDocumentContent возвращает документ с содержимым.
func (q *Queries) GetDocumentContent(ctx context.Context, appID, docID string) (domain.Document, []byte, error) {
	var d domain.Document
	var content []byte
	err := q.db.QueryRowContext(ctx, `SELECT id, application_id, kind, filename, content_type, size_bytes, sha256,
		uploaded_by, uploaded_at, content FROM documents WHERE application_id=$1 AND id=$2`, appID, docID).Scan(
		&d.ID, &d.ApplicationID, &d.Kind, &d.Filename, &d.ContentType, &d.SizeBytes, &d.SHA256, &d.UploadedBy, &d.UploadedAt, &content)
	if errors.Is(err, sql.ErrNoRows) {
		return d, nil, domain.ErrNotFound
	}
	return d, content, err
}

// IdempotentResponse — сохранённый ответ на запрос с ключом идемпотентности.
type IdempotentResponse struct {
	RequestHash string
	StatusCode  int
	Body        []byte
}

// GetIdempotent ищет сохранённый ответ.
func (q *Queries) GetIdempotent(ctx context.Context, userID, key string) (*IdempotentResponse, error) {
	var r IdempotentResponse
	err := q.db.QueryRowContext(ctx, `SELECT request_hash, status_code, response FROM idempotency_keys
		WHERE user_id=$1 AND key=$2 AND created_at > now() - interval '24 hours'`, userID, key).Scan(&r.RequestHash, &r.StatusCode, &r.Body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &r, err
}

// SaveIdempotent сохраняет ответ (первый записавший побеждает).
func (q *Queries) SaveIdempotent(ctx context.Context, userID, key string, r IdempotentResponse) error {
	_, err := q.db.ExecContext(ctx, `INSERT INTO idempotency_keys (user_id, key, request_hash, status_code, response)
		VALUES ($1,$2,$3,$4,$5) ON CONFLICT (user_id, key) DO NOTHING`, userID, key, r.RequestHash, r.StatusCode, r.Body)
	return err
}

// NewsRow — новость с тегами в сыром виде.
type NewsRow struct {
	ID          string
	Title       string
	Body        string
	Category    string
	Tags        []byte
	Priority    int
	LinkAction  string
	PublishedAt sql.NullTime
}

// UpsertNews сохраняет новость (сидирование).
func (q *Queries) UpsertNews(ctx context.Context, n NewsRow) error {
	_, err := q.db.ExecContext(ctx, `INSERT INTO news (id, title, body, category, tags, priority, link_action, published_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (id) DO UPDATE SET title=EXCLUDED.title, body=EXCLUDED.body, category=EXCLUDED.category,
			tags=EXCLUDED.tags, priority=EXCLUDED.priority, link_action=EXCLUDED.link_action, published_at=EXCLUDED.published_at`,
		n.ID, n.Title, n.Body, n.Category, n.Tags, n.Priority, n.LinkAction, n.PublishedAt.Time)
	return err
}

// ListNews возвращает все новости.
func (q *Queries) ListNews(ctx context.Context) ([]NewsRow, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT id, title, body, category, tags, priority, link_action, published_at
		FROM news ORDER BY published_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NewsRow
	for rows.Next() {
		var n NewsRow
		if err := rows.Scan(&n.ID, &n.Title, &n.Body, &n.Category, &n.Tags, &n.Priority, &n.LinkAction, &n.PublishedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
