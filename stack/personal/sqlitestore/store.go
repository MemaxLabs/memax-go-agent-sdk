// Package sqlitestore provides a durable SQLite-backed personal scheduled-run
// store.
//
// The store preserves the ScheduledRunStore idempotency contract with SQLite
// BEGIN IMMEDIATE transactions, so concurrent watchers attempting the same
// trigger occurrence serialize before the create-if-missing check runs. Unlike
// cloudmanaged.RunStore, this store is keyed by deterministic trigger intent
// rather than worker claim or tenant identity.
package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/stack/personal"
)

// Store is a SQLite-backed personal.ScheduledRunStore.
type Store struct {
	db *sql.DB
}

// New initializes and returns a SQLite-backed scheduled-run store.
func New(ctx context.Context, db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("sqlite scheduled run store db is required")
	}
	store := &Store{db: db}
	if err := store.init(ctx); err != nil {
		return nil, err
	}
	return store, nil
}

// CreateScheduledRun implements personal.ScheduledRunStore.
func (s *Store) CreateScheduledRun(ctx context.Context, req personal.CreateScheduledRunRequest) (personal.ScheduledRunRecord, bool, error) {
	if err := contextError(ctx); err != nil {
		return personal.ScheduledRunRecord{}, false, err
	}
	if s == nil {
		return personal.ScheduledRunRecord{}, false, fmt.Errorf("sqlite scheduled run store is nil")
	}
	if req.ID == "" {
		return personal.ScheduledRunRecord{}, false, fmt.Errorf("scheduled run id is required")
	}
	if req.TriggerName == "" {
		return personal.ScheduledRunRecord{}, false, fmt.Errorf("scheduled trigger name is required")
	}
	if req.Prompt == "" {
		return personal.ScheduledRunRecord{}, false, fmt.Errorf("scheduled prompt is required")
	}

	var (
		record  personal.ScheduledRunRecord
		created bool
	)
	err := s.withImmediateTx(ctx, func(conn *sql.Conn) error {
		existing, err := s.getScheduledRunConn(ctx, conn, req.ID)
		if err == nil {
			record = existing
			created = false
			return nil
		}
		if !errors.Is(err, personal.ErrScheduledRunNotFound) {
			return err
		}

		now := time.Now().UTC()
		record = personal.ScheduledRunRecord{
			ID:           req.ID,
			TriggerName:  req.TriggerName,
			OccurrenceAt: req.OccurrenceAt.UTC(),
			Prompt:       req.Prompt,
			Status:       personal.ScheduledRunQueued,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		if _, err := conn.ExecContext(ctx, `
			INSERT INTO memax_personal_scheduled_runs (
				id, trigger_name, occurrence_at_unix_ms, prompt, status,
				session_id, result, error, created_at_unix_ms,
				started_at_unix_ms, completed_at_unix_ms, updated_at_unix_ms
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, record.ID, record.TriggerName, unixMillis(record.OccurrenceAt), record.Prompt, string(record.Status), "", "", "", unixMillis(record.CreatedAt), nil, nil, unixMillis(record.UpdatedAt)); err != nil {
			return fmt.Errorf("create sqlite scheduled run: %w", err)
		}
		created = true
		return nil
	})
	if err != nil {
		return personal.ScheduledRunRecord{}, false, err
	}
	return record, created, nil
}

// UpdateScheduledRun implements personal.ScheduledRunStore.
func (s *Store) UpdateScheduledRun(ctx context.Context, update personal.ScheduledRunUpdate) (personal.ScheduledRunRecord, error) {
	if err := contextError(ctx); err != nil {
		return personal.ScheduledRunRecord{}, err
	}
	if s == nil {
		return personal.ScheduledRunRecord{}, fmt.Errorf("sqlite scheduled run store is nil")
	}
	if update.ID == "" {
		return personal.ScheduledRunRecord{}, fmt.Errorf("scheduled run id is required")
	}

	var record personal.ScheduledRunRecord
	err := s.withImmediateTx(ctx, func(conn *sql.Conn) error {
		current, err := s.getScheduledRunConn(ctx, conn, update.ID)
		if err != nil {
			return err
		}
		if current.Terminal() {
			record = current
			return nil
		}
		if update.Status != "" {
			current.Status = update.Status
			if update.Status == personal.ScheduledRunRunning && current.StartedAt.IsZero() {
				current.StartedAt = time.Now().UTC()
			}
		}
		if update.SessionID != "" {
			current.SessionID = update.SessionID
		}
		if update.Result != nil {
			current.Result = *update.Result
		}
		if update.Error != nil {
			current.Error = *update.Error
		}
		if update.CompletedAt != nil {
			current.CompletedAt = update.CompletedAt.UTC()
		}
		current.UpdatedAt = time.Now().UTC()
		if err := s.updateScheduledRunConn(ctx, conn, current); err != nil {
			return err
		}
		record = current
		return nil
	})
	if err != nil {
		return personal.ScheduledRunRecord{}, err
	}
	return record, nil
}

// GetScheduledRun implements personal.ScheduledRunStore.
func (s *Store) GetScheduledRun(ctx context.Context, id string) (personal.ScheduledRunRecord, error) {
	if err := contextError(ctx); err != nil {
		return personal.ScheduledRunRecord{}, err
	}
	if s == nil {
		return personal.ScheduledRunRecord{}, fmt.Errorf("sqlite scheduled run store is nil")
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return personal.ScheduledRunRecord{}, fmt.Errorf("acquire sqlite scheduled run connection: %w", err)
	}
	defer conn.Close()
	return s.getScheduledRunConn(ctx, conn, id)
}

// FailStaleScheduledRuns implements
// personal.ScheduledRunStoreWithStaleReconciliation. Hosts are expected to call
// it from their own periodic reconciliation loop.
func (s *Store) FailStaleScheduledRuns(ctx context.Context, staleBefore time.Time, reason string) ([]personal.ScheduledRunRecord, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("sqlite scheduled run store is nil")
	}

	var failed []personal.ScheduledRunRecord
	err := s.withImmediateTx(ctx, func(conn *sql.Conn) error {
		rows, err := conn.QueryContext(ctx, `
			SELECT id
			FROM memax_personal_scheduled_runs
			WHERE status IN (?, ?)
				AND updated_at_unix_ms < ?
			ORDER BY updated_at_unix_ms ASC, id ASC
		`, string(personal.ScheduledRunQueued), string(personal.ScheduledRunRunning), unixMillis(staleBefore.UTC()))
		if err != nil {
			return fmt.Errorf("list stale sqlite scheduled runs: %w", err)
		}
		defer rows.Close()

		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return fmt.Errorf("scan stale sqlite scheduled run id: %w", err)
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate stale sqlite scheduled runs: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close stale sqlite scheduled run rows: %w", err)
		}

		now := time.Now().UTC()
		for _, id := range ids {
			record, err := s.getScheduledRunConn(ctx, conn, id)
			if err != nil {
				return err
			}
			if record.Terminal() {
				continue
			}
			record.Status = personal.ScheduledRunFailed
			record.Error = reason
			record.CompletedAt = now
			record.UpdatedAt = now
			if err := s.updateScheduledRunConn(ctx, conn, record); err != nil {
				return err
			}
			failed = append(failed, record)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return failed, nil
}

func (s *Store) init(ctx context.Context) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS memax_personal_scheduled_runs (
			id TEXT PRIMARY KEY,
			trigger_name TEXT NOT NULL,
			occurrence_at_unix_ms INTEGER NOT NULL,
			prompt TEXT NOT NULL,
			status TEXT NOT NULL,
			session_id TEXT NOT NULL DEFAULT '',
			result TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			created_at_unix_ms INTEGER NOT NULL,
			started_at_unix_ms INTEGER,
			completed_at_unix_ms INTEGER,
			updated_at_unix_ms INTEGER NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("init sqlite scheduled run schema: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS memax_personal_scheduled_runs_updated_idx
		ON memax_personal_scheduled_runs(updated_at_unix_ms)
	`); err != nil {
		return fmt.Errorf("init sqlite scheduled run updated index: %w", err)
	}
	return nil
}

func (s *Store) withImmediateTx(ctx context.Context, fn func(*sql.Conn) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire sqlite scheduled run connection: %w", err)
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
		return fmt.Errorf("begin sqlite scheduled run transaction: %w", err)
	}
	started = true
	if err := fn(conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit sqlite scheduled run transaction: %w", err)
	}
	committed = true
	return nil
}

func (s *Store) getScheduledRunConn(ctx context.Context, conn *sql.Conn, id string) (personal.ScheduledRunRecord, error) {
	if id == "" {
		return personal.ScheduledRunRecord{}, fmt.Errorf("scheduled run id is required")
	}
	var (
		record            personal.ScheduledRunRecord
		status            string
		startedAtUnixMS   sql.NullInt64
		completedAtUnixMS sql.NullInt64
	)
	err := conn.QueryRowContext(ctx, `
		SELECT id, trigger_name, occurrence_at_unix_ms, prompt, status,
			session_id, result, error, created_at_unix_ms,
			started_at_unix_ms, completed_at_unix_ms, updated_at_unix_ms
		FROM memax_personal_scheduled_runs
		WHERE id = ?
		LIMIT 1
	`, id).Scan(
		&record.ID,
		&record.TriggerName,
		(*unixMillisTime)(&record.OccurrenceAt),
		&record.Prompt,
		&status,
		&record.SessionID,
		&record.Result,
		&record.Error,
		(*unixMillisTime)(&record.CreatedAt),
		&startedAtUnixMS,
		&completedAtUnixMS,
		(*unixMillisTime)(&record.UpdatedAt),
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return personal.ScheduledRunRecord{}, personal.ErrScheduledRunNotFound
		}
		return personal.ScheduledRunRecord{}, fmt.Errorf("get sqlite scheduled run %s: %w", id, err)
	}
	record.Status = personal.ScheduledRunStatus(status)
	if startedAtUnixMS.Valid {
		record.StartedAt = time.UnixMilli(startedAtUnixMS.Int64).UTC()
	}
	if completedAtUnixMS.Valid {
		record.CompletedAt = time.UnixMilli(completedAtUnixMS.Int64).UTC()
	}
	return record, nil
}

func (s *Store) updateScheduledRunConn(ctx context.Context, conn *sql.Conn, record personal.ScheduledRunRecord) error {
	var startedAt any
	if !record.StartedAt.IsZero() {
		startedAt = unixMillis(record.StartedAt)
	}
	var completedAt any
	if !record.CompletedAt.IsZero() {
		completedAt = unixMillis(record.CompletedAt)
	}
	if _, err := conn.ExecContext(ctx, `
		UPDATE memax_personal_scheduled_runs
		SET status = ?, session_id = ?, result = ?, error = ?,
			started_at_unix_ms = ?, completed_at_unix_ms = ?, updated_at_unix_ms = ?
		WHERE id = ?
	`, string(record.Status), record.SessionID, record.Result, record.Error, startedAt, completedAt, unixMillis(record.UpdatedAt), record.ID); err != nil {
		return fmt.Errorf("update sqlite scheduled run: %w", err)
	}
	return nil
}

func unixMillis(t time.Time) int64 {
	return t.UnixMilli()
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

type unixMillisTime time.Time

func (t *unixMillisTime) Scan(value any) error {
	switch v := value.(type) {
	case int64:
		*t = unixMillisTime(time.UnixMilli(v).UTC())
		return nil
	case nil:
		*t = unixMillisTime(time.Time{})
		return nil
	default:
		return fmt.Errorf("scan unix millis time: unsupported %T", value)
	}
}
