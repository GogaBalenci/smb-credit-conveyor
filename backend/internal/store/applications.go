package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"

	"alfahack/backend/internal/domain"
)

const appCols = `a.id, a.number, a.company_id, c.name, a.applicant_id, a.product_code, a.purpose, a.purpose_comment,
	a.amount, a.term_months, a.financials, a.status, a.required_docs, a.score, a.decision,
	COALESCE(a.assignee_id::text, ''), a.version, a.created_at, a.updated_at, a.submitted_at`

const appFrom = ` FROM applications a JOIN companies c ON c.id = a.company_id `

func scanApp(row interface{ Scan(...any) error }) (domain.Application, error) {
	var a domain.Application
	var fin, docs []byte
	var score, decision []byte
	var submitted sql.NullTime
	err := row.Scan(&a.ID, &a.Number, &a.CompanyID, &a.CompanyName, &a.ApplicantID, &a.ProductCode, &a.Purpose,
		&a.PurposeComment, &a.Amount, &a.TermMonths, &fin, &a.Status, &docs, &score, &decision,
		&a.AssigneeID, &a.Version, &a.CreatedAt, &a.UpdatedAt, &submitted)
	if errors.Is(err, sql.ErrNoRows) {
		return a, domain.ErrNotFound
	}
	if err != nil {
		return a, err
	}
	if submitted.Valid {
		a.SubmittedAt = &submitted.Time
	}
	if err := json.Unmarshal(fin, &a.Financials); err != nil {
		return a, fmt.Errorf("financials: %w", err)
	}
	if err := json.Unmarshal(docs, &a.RequiredDocs); err != nil {
		return a, fmt.Errorf("required_docs: %w", err)
	}
	if len(score) > 0 {
		a.Score = &domain.ScoreResult{}
		if err := json.Unmarshal(score, a.Score); err != nil {
			return a, fmt.Errorf("score: %w", err)
		}
	}
	if len(decision) > 0 {
		a.Decision = &domain.Decision{}
		if err := json.Unmarshal(decision, a.Decision); err != nil {
			return a, fmt.Errorf("decision: %w", err)
		}
	}
	if a.RequiredDocs == nil {
		a.RequiredDocs = []domain.DocRequest{}
	}
	return a, nil
}

func jsonOrNull(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if string(b) == "null" {
		return nil, nil
	}
	return b, nil
}

// CreateApplication создаёт черновик заявки с человекочитаемым номером.
func (q *Queries) CreateApplication(ctx context.Context, a domain.Application) (domain.Application, error) {
	if a.ID == "" {
		a.ID = domain.NewID()
	}
	fin, _ := json.Marshal(a.Financials)
	docs, _ := json.Marshal(nonNilDocs(a.RequiredDocs))
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO applications (id, number, company_id, applicant_id, product_code, purpose, purpose_comment,
			amount, term_months, financials, status, required_docs)
		VALUES ($1, 'КЗ-' || to_char(now(), 'YYYY') || '-' || nextval('application_number_seq'),
			$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		a.ID, a.CompanyID, a.ApplicantID, a.ProductCode, a.Purpose, a.PurposeComment,
		a.Amount, a.TermMonths, fin, a.Status, docs)
	if err != nil {
		return a, err
	}
	return q.GetApplication(ctx, a.ID)
}

// GetApplication возвращает заявку.
func (q *Queries) GetApplication(ctx context.Context, id string) (domain.Application, error) {
	return scanApp(q.db.QueryRowContext(ctx, `SELECT `+appCols+appFrom+`WHERE a.id=$1`, id))
}

// LockApplication возвращает заявку с блокировкой строки до конца транзакции.
func (q *Queries) LockApplication(ctx context.Context, id string) (domain.Application, error) {
	return scanApp(q.db.QueryRowContext(ctx, `SELECT `+appCols+appFrom+`WHERE a.id=$1 FOR UPDATE OF a`, id))
}

// SaveApplication сохраняет изменяемые поля с оптимистической блокировкой:
// запись проходит, только если версия не изменилась с момента чтения.
func (q *Queries) SaveApplication(ctx context.Context, a domain.Application) (domain.Application, error) {
	fin, _ := json.Marshal(a.Financials)
	docs, _ := json.Marshal(nonNilDocs(a.RequiredDocs))
	score, err := jsonOrNull(a.Score)
	if err != nil {
		return a, err
	}
	decision, err := jsonOrNull(a.Decision)
	if err != nil {
		return a, err
	}
	var assignee any
	if a.AssigneeID != "" {
		assignee = a.AssigneeID
	}
	res, err := q.db.ExecContext(ctx, `
		UPDATE applications SET product_code=$3, purpose=$4, purpose_comment=$5, amount=$6, term_months=$7,
			financials=$8, status=$9, required_docs=$10, score=$11, decision=$12, assignee_id=$13,
			submitted_at=$14, version=version+1, updated_at=now()
		WHERE id=$1 AND version=$2`,
		a.ID, a.Version, a.ProductCode, a.Purpose, a.PurposeComment, a.Amount, a.TermMonths,
		fin, a.Status, docs, score, decision, assignee, a.SubmittedAt)
	if err != nil {
		return a, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return a, domain.ErrConflict
	}
	return q.GetApplication(ctx, a.ID)
}

// ListFilter — фильтр списка заявок.
type ListFilter struct {
	CompanyID  string
	Statuses   []domain.Status
	AssigneeID string
	Search     string // номер заявки, название компании или ИНН
	Limit      int
	Offset     int
}

// ListApplications возвращает заявки по фильтру, новые сверху.
func (q *Queries) ListApplications(ctx context.Context, f ListFilter) ([]domain.Application, int, error) {
	var where []string
	var args []any
	arg := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	if f.CompanyID != "" {
		where = append(where, "a.company_id="+arg(f.CompanyID))
	}
	if len(f.Statuses) > 0 {
		ss := make([]string, len(f.Statuses))
		for i, s := range f.Statuses {
			ss[i] = string(s)
		}
		where = append(where, "a.status = ANY("+arg(pq.Array(ss))+")")
	}
	if f.AssigneeID != "" {
		where = append(where, "a.assignee_id="+arg(f.AssigneeID))
	}
	if s := strings.TrimSpace(f.Search); s != "" {
		p := arg("%" + s + "%")
		where = append(where, "(a.number ILIKE "+p+" OR c.name ILIKE "+p+" OR c.inn ILIKE "+p+")")
	}
	cond := ""
	if len(where) > 0 {
		cond = "WHERE " + strings.Join(where, " AND ")
	}
	var total int
	if err := q.db.QueryRowContext(ctx, `SELECT count(*)`+appFrom+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	rows, err := q.db.QueryContext(ctx, `SELECT `+appCols+appFrom+cond+
		` ORDER BY a.updated_at DESC LIMIT `+arg(f.Limit)+` OFFSET `+arg(f.Offset), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []domain.Application{}
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, a)
	}
	return out, total, rows.Err()
}

// InsertEvent пишет событие в журнал.
func (q *Queries) InsertEvent(ctx context.Context, e domain.Event) error {
	var from any
	if e.From != "" {
		from = e.From
	}
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO application_events (application_id, from_status, to_status, actor_kind, actor_id, comment)
		VALUES ($1,$2,$3,$4,$5,$6)`, e.ApplicationID, from, e.To, e.ActorKind, e.ActorID, e.Comment)
	return err
}

// ListEvents возвращает журнал заявки по порядку.
func (q *Queries) ListEvents(ctx context.Context, appID string) ([]domain.Event, error) {
	return q.queryEvents(ctx, `WHERE application_id=$1 ORDER BY id`, appID)
}

// ListCompanyEvents — последние события по всем заявкам компании (для ленты).
func (q *Queries) ListCompanyEvents(ctx context.Context, companyID string, limit int) ([]domain.Event, error) {
	return q.queryEvents(ctx, `WHERE application_id IN (SELECT id FROM applications WHERE company_id=$1)
		ORDER BY id DESC LIMIT `+fmt.Sprint(limit), companyID)
}

func (q *Queries) queryEvents(ctx context.Context, cond string, args ...any) ([]domain.Event, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT id, application_id, COALESCE(from_status,''), to_status,
		actor_kind, actor_id, comment, created_at FROM application_events `+cond, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Event{}
	for rows.Next() {
		var e domain.Event
		if err := rows.Scan(&e.ID, &e.ApplicationID, &e.From, &e.To, &e.ActorKind, &e.ActorID, &e.Comment, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// SetSignCode сохраняет хэш кода подписания и срок его действия.
func (q *Queries) SetSignCode(ctx context.Context, appID, hash string, until time.Time) error {
	_, err := q.db.ExecContext(ctx, `UPDATE applications SET sign_code_hash=$2, sign_code_until=$3 WHERE id=$1`, appID, hash, until)
	return err
}

// GetSignCode возвращает хэш кода подписания.
func (q *Queries) GetSignCode(ctx context.Context, appID string) (hash string, until time.Time, err error) {
	var h sql.NullString
	var u sql.NullTime
	err = q.db.QueryRowContext(ctx, `SELECT sign_code_hash, sign_code_until FROM applications WHERE id=$1`, appID).Scan(&h, &u)
	return h.String, u.Time, err
}

// StatusCounts — количество заявок по статусам (дашборд банка).
func (q *Queries) StatusCounts(ctx context.Context) (map[domain.Status]int, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT status, count(*) FROM applications GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[domain.Status]int{}
	for rows.Next() {
		var s domain.Status
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			return nil, err
		}
		out[s] = n
	}
	return out, rows.Err()
}

// AvgDecisionSeconds — среднее время от отправки до решения (автоматического или ручного).
func (q *Queries) AvgDecisionSeconds(ctx context.Context) (auto, manual float64, err error) {
	err = q.db.QueryRowContext(ctx, `
		WITH decided AS (
			SELECT a.id, a.submitted_at, (a.decision->>'decided_by') AS by,
				(SELECT min(e.created_at) FROM application_events e
				 WHERE e.application_id=a.id AND e.to_status IN ('APPROVED','COUNTER_OFFER','REJECTED')) AS decided_at
			FROM applications a WHERE a.submitted_at IS NOT NULL AND a.decision IS NOT NULL)
		SELECT COALESCE(avg(EXTRACT(EPOCH FROM decided_at - submitted_at)) FILTER (WHERE by='system'), 0),
		       COALESCE(avg(EXTRACT(EPOCH FROM decided_at - submitted_at)) FILTER (WHERE by<>'system'), 0)
		FROM decided WHERE decided_at IS NOT NULL`).Scan(&auto, &manual)
	return
}

func nonNilDocs(d []domain.DocRequest) []domain.DocRequest {
	if d == nil {
		return []domain.DocRequest{}
	}
	return d
}
