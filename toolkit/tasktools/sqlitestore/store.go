// Package sqlitestore provides a durable SQLite-backed tasktools.Store.
//
// The store mirrors tasktools.MemoryStore semantics while persisting task
// state for multi-run workflows. Generated task IDs and upserts run inside
// SQLite BEGIN IMMEDIATE transactions, so concurrent agents cannot allocate
// duplicate task-N IDs or race partial updates.
package sqlitestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/tasktools"
)

const nextTaskNumberKey = "next_task_number"

// Store is a SQLite-backed tasktools.Store.
type Store struct {
	db *sql.DB
}

var _ tasktools.Store = (*Store)(nil)

// New initializes and returns a SQLite-backed task store.
func New(ctx context.Context, db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("sqlite task store db is required")
	}
	store := &Store{db: db}
	if err := store.init(ctx); err != nil {
		return nil, err
	}
	return store, nil
}

// List implements tasktools.Store.
func (s *Store) List(ctx context.Context) ([]tasktools.Task, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("sqlite task store is nil")
	}
	ctx = contextOrBackground(ctx)
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, title, status, notes, priority, evidence_json
		FROM memax_tasks
		ORDER BY created_seq ASC, id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list sqlite tasks: %w", err)
	}
	defer rows.Close()

	var out []tasktools.Task
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, cloneTask(task))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sqlite tasks: %w", err)
	}
	return out, nil
}

// Upsert implements tasktools.Store.
func (s *Store) Upsert(ctx context.Context, task tasktools.Task) (tasktools.Task, error) {
	if err := contextError(ctx); err != nil {
		return tasktools.Task{}, err
	}
	if s == nil {
		return tasktools.Task{}, fmt.Errorf("sqlite task store is nil")
	}
	task = normalizeTask(task)
	if !isValidStatus(task.Status) {
		return tasktools.Task{}, fmt.Errorf("invalid task status: %s", task.Status)
	}

	ctx = contextOrBackground(ctx)
	var out tasktools.Task
	err := s.withImmediateTx(ctx, func(conn *sql.Conn) error {
		upserted, err := s.upsertTaskConn(ctx, conn, task)
		if err != nil {
			return err
		}
		out = upserted
		return nil
	})
	if err != nil {
		return tasktools.Task{}, err
	}
	return cloneTask(out), nil
}

// Delete implements tasktools.Store.
func (s *Store) Delete(ctx context.Context, id string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if s == nil {
		return fmt.Errorf("sqlite task store is nil")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("task id is required")
	}

	ctx = contextOrBackground(ctx)
	return s.withImmediateTx(ctx, func(conn *sql.Conn) error {
		result, err := conn.ExecContext(ctx, `DELETE FROM memax_tasks WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("delete sqlite task %s: %w", id, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("delete sqlite task %s rows affected: %w", id, err)
		}
		if affected == 0 {
			return fmt.Errorf("task not found: %s", id)
		}
		return nil
	})
}

func (s *Store) init(ctx context.Context) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	ctx = contextOrBackground(ctx)
	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS memax_tasks (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			status TEXT NOT NULL,
			notes TEXT NOT NULL DEFAULT '',
			priority INTEGER NOT NULL DEFAULT 0,
			evidence_json TEXT NOT NULL DEFAULT '[]',
			created_seq INTEGER NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("init sqlite task schema: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS memax_tasks_created_seq_idx
		ON memax_tasks(created_seq)
	`); err != nil {
		return fmt.Errorf("init sqlite task created index: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS memax_task_meta (
			name TEXT PRIMARY KEY,
			value INTEGER NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("init sqlite task meta schema: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO memax_task_meta(name, value)
		VALUES (?, 1)
	`, nextTaskNumberKey); err != nil {
		return fmt.Errorf("init sqlite task meta: %w", err)
	}
	return nil
}

func (s *Store) upsertTaskConn(ctx context.Context, conn *sql.Conn, task tasktools.Task) (tasktools.Task, error) {
	var createdSeq int64
	if task.ID == "" {
		if task.Title == "" {
			return tasktools.Task{}, fmt.Errorf("task title is required")
		}
		id, err := s.nextIDConn(ctx, conn)
		if err != nil {
			return tasktools.Task{}, err
		}
		task.ID = id
		if task.Status == "" {
			task.Status = tasktools.StatusPending
		}
		seq, err := s.nextCreatedSeqConn(ctx, conn)
		if err != nil {
			return tasktools.Task{}, err
		}
		createdSeq = seq
		return s.insertTaskConn(ctx, conn, task, createdSeq)
	}

	existing, existingSeq, err := s.getTaskConn(ctx, conn, task.ID)
	if err == nil {
		merged := mergeTask(existing, task)
		if !isValidStatus(merged.Status) {
			return tasktools.Task{}, fmt.Errorf("invalid task status: %s", merged.Status)
		}
		return s.updateTaskConn(ctx, conn, merged, existingSeq)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return tasktools.Task{}, err
	}
	if task.Title == "" {
		return tasktools.Task{}, fmt.Errorf("task title is required")
	}
	if task.Status == "" {
		task.Status = tasktools.StatusPending
	}
	if !isValidStatus(task.Status) {
		return tasktools.Task{}, fmt.Errorf("invalid task status: %s", task.Status)
	}
	seq, err := s.nextCreatedSeqConn(ctx, conn)
	if err != nil {
		return tasktools.Task{}, err
	}
	createdSeq = seq
	inserted, err := s.insertTaskConn(ctx, conn, task, createdSeq)
	if err != nil {
		return tasktools.Task{}, err
	}
	if err := s.bumpNextConn(ctx, conn, task.ID); err != nil {
		return tasktools.Task{}, err
	}
	return inserted, nil
}

func (s *Store) getTaskConn(ctx context.Context, conn *sql.Conn, id string) (tasktools.Task, int64, error) {
	var (
		task         tasktools.Task
		status       string
		evidenceJSON string
		createdSeq   int64
	)
	err := conn.QueryRowContext(ctx, `
		SELECT id, title, status, notes, priority, evidence_json, created_seq
		FROM memax_tasks
		WHERE id = ?
		LIMIT 1
	`, id).Scan(&task.ID, &task.Title, &status, &task.Notes, &task.Priority, &evidenceJSON, &createdSeq)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return tasktools.Task{}, 0, sql.ErrNoRows
		}
		return tasktools.Task{}, 0, fmt.Errorf("get sqlite task %s: %w", id, err)
	}
	task.Status = tasktools.Status(status)
	if err := unmarshalEvidence(evidenceJSON, &task.Evidence); err != nil {
		return tasktools.Task{}, 0, fmt.Errorf("decode sqlite task %s evidence: %w", id, err)
	}
	return cloneTask(task), createdSeq, nil
}

func (s *Store) insertTaskConn(ctx context.Context, conn *sql.Conn, task tasktools.Task, createdSeq int64) (tasktools.Task, error) {
	evidenceJSON, err := marshalEvidence(task.Evidence)
	if err != nil {
		return tasktools.Task{}, err
	}
	_, err = conn.ExecContext(ctx, `
		INSERT INTO memax_tasks(id, title, status, notes, priority, evidence_json, created_seq)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, task.ID, task.Title, string(task.Status), task.Notes, task.Priority, evidenceJSON, createdSeq)
	if err != nil {
		return tasktools.Task{}, fmt.Errorf("insert sqlite task %s: %w", task.ID, err)
	}
	return cloneTask(task), nil
}

func (s *Store) updateTaskConn(ctx context.Context, conn *sql.Conn, task tasktools.Task, createdSeq int64) (tasktools.Task, error) {
	evidenceJSON, err := marshalEvidence(task.Evidence)
	if err != nil {
		return tasktools.Task{}, err
	}
	_, err = conn.ExecContext(ctx, `
		UPDATE memax_tasks
		SET title = ?, status = ?, notes = ?, priority = ?, evidence_json = ?, created_seq = ?
		WHERE id = ?
	`, task.Title, string(task.Status), task.Notes, task.Priority, evidenceJSON, createdSeq, task.ID)
	if err != nil {
		return tasktools.Task{}, fmt.Errorf("update sqlite task %s: %w", task.ID, err)
	}
	return cloneTask(task), nil
}

func (s *Store) nextIDConn(ctx context.Context, conn *sql.Conn) (string, error) {
	for {
		next, err := s.nextTaskNumberConn(ctx, conn)
		if err != nil {
			return "", err
		}
		id := fmt.Sprintf("task-%d", next)
		if err := s.setNextTaskNumberConn(ctx, conn, next+1); err != nil {
			return "", err
		}
		var exists int
		err = conn.QueryRowContext(ctx, `SELECT 1 FROM memax_tasks WHERE id = ? LIMIT 1`, id).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return id, nil
		}
		if err != nil {
			return "", fmt.Errorf("check sqlite generated task id %s: %w", id, err)
		}
	}
}

func (s *Store) nextTaskNumberConn(ctx context.Context, conn *sql.Conn) (int, error) {
	var next int
	err := conn.QueryRowContext(ctx, `
		SELECT value
		FROM memax_task_meta
		WHERE name = ?
		LIMIT 1
	`, nextTaskNumberKey).Scan(&next)
	if err != nil {
		return 0, fmt.Errorf("read sqlite task id counter: %w", err)
	}
	if next < 1 {
		next = 1
	}
	return next, nil
}

func (s *Store) setNextTaskNumberConn(ctx context.Context, conn *sql.Conn, next int) error {
	_, err := conn.ExecContext(ctx, `
		INSERT INTO memax_task_meta(name, value)
		VALUES (?, ?)
		ON CONFLICT(name) DO UPDATE SET value = excluded.value
	`, nextTaskNumberKey, next)
	if err != nil {
		return fmt.Errorf("update sqlite task id counter: %w", err)
	}
	return nil
}

func (s *Store) bumpNextConn(ctx context.Context, conn *sql.Conn, id string) error {
	var n int
	if _, err := fmt.Sscanf(id, "task-%d", &n); err != nil {
		return nil
	}
	next, err := s.nextTaskNumberConn(ctx, conn)
	if err != nil {
		return err
	}
	if n >= next {
		return s.setNextTaskNumberConn(ctx, conn, n+1)
	}
	return nil
}

func (s *Store) nextCreatedSeqConn(ctx context.Context, conn *sql.Conn) (int64, error) {
	var seq int64
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(created_seq), 0) + 1 FROM memax_tasks`).Scan(&seq); err != nil {
		return 0, fmt.Errorf("allocate sqlite task created sequence: %w", err)
	}
	return seq, nil
}

func (s *Store) withImmediateTx(ctx context.Context, fn func(*sql.Conn) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire sqlite task connection: %w", err)
	}
	defer conn.Close()

	started := false
	committed := false
	defer func() {
		if started && !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("begin sqlite task transaction: %w", err)
	}
	started = true
	if err := fn(conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit sqlite task transaction: %w", err)
	}
	committed = true
	return nil
}

type taskScanner interface {
	Scan(dest ...any) error
}

func scanTask(scanner taskScanner) (tasktools.Task, error) {
	var (
		task         tasktools.Task
		status       string
		evidenceJSON string
	)
	if err := scanner.Scan(&task.ID, &task.Title, &status, &task.Notes, &task.Priority, &evidenceJSON); err != nil {
		return tasktools.Task{}, fmt.Errorf("scan sqlite task: %w", err)
	}
	task.Status = tasktools.Status(status)
	if err := unmarshalEvidence(evidenceJSON, &task.Evidence); err != nil {
		return tasktools.Task{}, fmt.Errorf("decode sqlite task %s evidence: %w", task.ID, err)
	}
	return task, nil
}

func marshalEvidence(evidence []string) (string, error) {
	if len(evidence) == 0 {
		return "[]", nil
	}
	data, err := json.Marshal(evidence)
	if err != nil {
		return "", fmt.Errorf("marshal sqlite task evidence: %w", err)
	}
	return string(data), nil
}

func unmarshalEvidence(data string, out *[]string) error {
	if strings.TrimSpace(data) == "" {
		*out = nil
		return nil
	}
	if err := json.Unmarshal([]byte(data), out); err != nil {
		return err
	}
	*out = nonEmptyStrings(*out)
	return nil
}

func normalizeTask(task tasktools.Task) tasktools.Task {
	task.ID = strings.TrimSpace(task.ID)
	task.Title = strings.TrimSpace(task.Title)
	task.Notes = strings.TrimSpace(task.Notes)
	task.Evidence = nonEmptyStrings(task.Evidence)
	return task
}

func mergeTask(existing tasktools.Task, update tasktools.Task) tasktools.Task {
	if update.Title != "" {
		existing.Title = update.Title
	}
	if update.Status != "" {
		existing.Status = update.Status
	}
	if update.Notes != "" {
		existing.Notes = update.Notes
	}
	if update.Priority > 0 {
		existing.Priority = update.Priority
	}
	if len(update.Evidence) > 0 {
		existing.Evidence = append([]string(nil), update.Evidence...)
	}
	return existing
}

func isValidStatus(status tasktools.Status) bool {
	switch status {
	case "", tasktools.StatusPending, tasktools.StatusInProgress, tasktools.StatusCompleted, tasktools.StatusBlocked, tasktools.StatusCanceled:
		return true
	default:
		return false
	}
}

func cloneTask(task tasktools.Task) tasktools.Task {
	task.Evidence = append([]string(nil), task.Evidence...)
	return task
}

func nonEmptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
