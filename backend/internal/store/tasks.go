package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Task — задача конвейера.
type Task struct {
	ID            int64
	Kind          string
	ApplicationID string
	Attempts      int
	MaxAttempts   int
}

// EnqueueTask ставит задачу; если такая же задача уже ждёт — ничего не делает
// (уникальный частичный индекс), поэтому постановка идемпотентна.
// delay откладывает выполнение (время считается по часам БД — единым для всех экземпляров).
func (q *Queries) EnqueueTask(ctx context.Context, kind, appID string, delay time.Duration) error {
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO tasks (kind, application_id, run_at) VALUES ($1, $2, now() + make_interval(secs => $3))
		ON CONFLICT (application_id, kind) WHERE status = 'pending' DO NOTHING`, kind, appID, delay.Seconds())
	return err
}

// ClaimTask берёт ближайшую задачу и «арендует» её на lease: run_at сдвигается
// вперёд, поэтому при падении воркера задача снова станет доступна (at-least-once).
// SKIP LOCKED позволяет нескольким воркерам работать параллельно без конфликтов.
func (q *Queries) ClaimTask(ctx context.Context, lease time.Duration) (*Task, error) {
	var t Task
	err := q.db.QueryRowContext(ctx, `
		UPDATE tasks SET attempts = attempts + 1, run_at = now() + make_interval(secs => $1), updated_at = now()
		WHERE id = (
			SELECT id FROM tasks WHERE status = 'pending' AND run_at <= now()
			ORDER BY run_at, id LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING id, kind, application_id, attempts, max_attempts`,
		lease.Seconds()).Scan(&t.ID, &t.Kind, &t.ApplicationID, &t.Attempts, &t.MaxAttempts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// CompleteTask помечает задачу выполненной.
func (q *Queries) CompleteTask(ctx context.Context, id int64) error {
	_, err := q.db.ExecContext(ctx, `UPDATE tasks SET status='done', last_error='', updated_at=now() WHERE id=$1`, id)
	return err
}

// RetryTask откладывает задачу с экспоненциальной задержкой либо помечает
// её проваленной после исчерпания попыток.
func (q *Queries) RetryTask(ctx context.Context, t Task, cause error, delay time.Duration) error {
	if t.Attempts >= t.MaxAttempts {
		_, err := q.db.ExecContext(ctx, `UPDATE tasks SET status='failed', last_error=$2, updated_at=now() WHERE id=$1`, t.ID, cause.Error())
		return err
	}
	_, err := q.db.ExecContext(ctx, `UPDATE tasks SET run_at=now()+make_interval(secs => $2), last_error=$3, updated_at=now() WHERE id=$1`,
		t.ID, delay.Seconds(), cause.Error())
	return err
}

// QueueStats — состояние очереди для метрик.
type QueueStats struct {
	Pending int `json:"pending"`
	Failed  int `json:"failed"`
	Retries int `json:"retrying"` // pending с attempts > 0 и ошибкой
}

// TaskStats возвращает состояние очереди.
func (q *Queries) TaskStats(ctx context.Context) (QueueStats, error) {
	var s QueueStats
	err := q.db.QueryRowContext(ctx, `SELECT
		count(*) FILTER (WHERE status='pending'),
		count(*) FILTER (WHERE status='failed'),
		count(*) FILTER (WHERE status='pending' AND last_error <> '')
		FROM tasks`).Scan(&s.Pending, &s.Failed, &s.Retries)
	return s, err
}

// TaskTrouble — незавершённая задача заявки, которая уже падала (повторяется или исчерпала попытки).
type TaskTrouble struct {
	Kind        string
	Status      string // pending / failed
	Attempts    int
	MaxAttempts int
	LastError   string
	NextRunAt   time.Time
}

// LatestTrouble возвращает последнюю проблемную задачу заявки или nil, если всё идёт штатно.
func (q *Queries) LatestTrouble(ctx context.Context, appID string) (*TaskTrouble, error) {
	var t TaskTrouble
	err := q.db.QueryRowContext(ctx, `
		SELECT kind, status, attempts, max_attempts, last_error, run_at FROM tasks
		WHERE application_id = $1 AND last_error <> '' AND status IN ('pending', 'failed')
		ORDER BY id DESC LIMIT 1`, appID).Scan(&t.Kind, &t.Status, &t.Attempts, &t.MaxAttempts, &t.LastError, &t.NextRunAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}
