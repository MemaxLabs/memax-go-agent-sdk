// Package sqlitestore provides durable SQLite-backed cloudmanaged stores.
//
// The store preserves the QuotaStore atomicity contract with SQLite
// BEGIN IMMEDIATE transactions, so concurrent reservations serialize before the
// limit check and update are applied. Unlike the Redis-backed store, SQLite
// does not apply TTL expiry automatically; normal session-end cleanup should
// call ResetSession, and hosts can use host-scheduled PruneBefore calls to
// clear stale rows from crashed or abandoned runs. The same Store also
// implements cloudmanaged.RunStore so quota state and durable managed-run
// lifecycle can live in one embedded database.
package sqlitestore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/stack/cloudmanaged"
	"github.com/MemaxLabs/memax-go-agent-sdk/stack/cloudmanaged/internal/scopekey"
	"github.com/MemaxLabs/memax-go-agent-sdk/tenant"
)

// Store is a SQLite-backed cloudmanaged.QuotaStore and cloudmanaged.RunStore.
type Store struct {
	db *sql.DB
}

// New initializes and returns a SQLite-backed managed store.
func New(ctx context.Context, db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("sqlite quota store db is required")
	}
	store := &Store{db: db}
	if err := store.init(ctx); err != nil {
		return nil, err
	}
	return store, nil
}

// CreateRun implements cloudmanaged.RunStore.
func (s *Store) CreateRun(ctx context.Context, req cloudmanaged.CreateRunRequest) (cloudmanaged.RunRecord, error) {
	if err := contextError(ctx); err != nil {
		return cloudmanaged.RunRecord{}, err
	}
	if s == nil {
		return cloudmanaged.RunRecord{}, fmt.Errorf("sqlite run store is nil")
	}
	id, err := newRunID()
	if err != nil {
		return cloudmanaged.RunRecord{}, err
	}
	attrs, err := marshalAttributes(req.Tenant.Attributes)
	if err != nil {
		return cloudmanaged.RunRecord{}, fmt.Errorf("marshal sqlite run tenant attributes: %w", err)
	}
	now := time.Now().UTC()
	record := cloudmanaged.RunRecord{
		ID:        id,
		Status:    cloudmanaged.RunStatusQueued,
		Prompt:    req.Prompt,
		Tenant:    req.Tenant.Clone(),
		CreatedAt: now,
		UpdatedAt: now,
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO memax_cloudmanaged_runs (
			id, status, prompt, tenant_id, subject_id, tenant_attributes_json,
			worker_id, created_at_unix_ms, updated_at_unix_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, record.ID, string(record.Status), record.Prompt, record.Tenant.ID, record.Tenant.SubjectID, attrs, "", unixMillis(now), unixMillis(now))
	if err != nil {
		return cloudmanaged.RunRecord{}, fmt.Errorf("create sqlite managed run: %w", err)
	}
	return record, nil
}

// UpdateRun implements cloudmanaged.RunStore.
func (s *Store) UpdateRun(ctx context.Context, update cloudmanaged.RunUpdate) (cloudmanaged.RunRecord, error) {
	if err := contextError(ctx); err != nil {
		return cloudmanaged.RunRecord{}, err
	}
	if s == nil {
		return cloudmanaged.RunRecord{}, fmt.Errorf("sqlite run store is nil")
	}
	if update.ID == "" {
		return cloudmanaged.RunRecord{}, fmt.Errorf("run id is required")
	}

	var record cloudmanaged.RunRecord
	err := s.withImmediateTx(ctx, func(conn *sql.Conn) error {
		current, err := s.getRunConn(ctx, conn, update.ID)
		if err != nil {
			return err
		}
		if update.Status != "" {
			current.Status = update.Status
			if update.Status == cloudmanaged.RunStatusRunning && current.StartedAt.IsZero() {
				current.StartedAt = time.Now().UTC()
			}
		}
		if update.SessionID != "" {
			current.SessionID = update.SessionID
		}
		if update.ParentSessionID != "" {
			current.ParentSessionID = update.ParentSessionID
		}
		if update.WorkerID != "" {
			current.WorkerID = update.WorkerID
		}
		if update.Result != nil {
			current.Result = *update.Result
		}
		if update.Error != nil {
			current.Error = *update.Error
		}
		if update.HeartbeatAt != nil {
			current.HeartbeatAt = update.HeartbeatAt.UTC()
		}
		if update.CompletedAt != nil {
			current.CompletedAt = update.CompletedAt.UTC()
		}
		current.UpdatedAt = time.Now().UTC()

		var startedAt any
		if !current.StartedAt.IsZero() {
			startedAt = unixMillis(current.StartedAt)
		}
		var completedAt any
		if !current.CompletedAt.IsZero() {
			completedAt = unixMillis(current.CompletedAt)
		}
		var heartbeatAt any
		if !current.HeartbeatAt.IsZero() {
			heartbeatAt = unixMillis(current.HeartbeatAt)
		}
		_, err = conn.ExecContext(ctx, `
			UPDATE memax_cloudmanaged_runs
			SET status = ?, session_id = ?, parent_session_id = ?, worker_id = ?, result = ?, error = ?,
				started_at_unix_ms = ?, heartbeat_at_unix_ms = ?, completed_at_unix_ms = ?, updated_at_unix_ms = ?
			WHERE id = ?
		`, string(current.Status), current.SessionID, current.ParentSessionID, current.WorkerID, current.Result, current.Error, startedAt, heartbeatAt, completedAt, unixMillis(current.UpdatedAt), current.ID)
		if err != nil {
			return fmt.Errorf("update sqlite managed run: %w", err)
		}
		record = current
		return nil
	})
	if err != nil {
		return cloudmanaged.RunRecord{}, err
	}
	return record, nil
}

// GetRun implements cloudmanaged.RunStore.
func (s *Store) GetRun(ctx context.Context, id string) (cloudmanaged.RunRecord, error) {
	if err := contextError(ctx); err != nil {
		return cloudmanaged.RunRecord{}, err
	}
	if s == nil {
		return cloudmanaged.RunRecord{}, fmt.Errorf("sqlite run store is nil")
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return cloudmanaged.RunRecord{}, fmt.Errorf("acquire sqlite run connection: %w", err)
	}
	defer conn.Close()
	return s.getRunConn(ctx, conn, id)
}

// ClaimRun implements cloudmanaged.RunStoreWithClaim.
func (s *Store) ClaimRun(ctx context.Context, id, workerID string) (cloudmanaged.RunRecord, error) {
	if err := contextError(ctx); err != nil {
		return cloudmanaged.RunRecord{}, err
	}
	if s == nil {
		return cloudmanaged.RunRecord{}, fmt.Errorf("sqlite run store is nil")
	}
	var record cloudmanaged.RunRecord
	err := s.withImmediateTx(ctx, func(conn *sql.Conn) error {
		current, err := s.getRunConn(ctx, conn, id)
		if err != nil {
			return err
		}
		if current.Status != cloudmanaged.RunStatusQueued {
			return cloudmanaged.ErrRunNotQueued
		}
		now := time.Now().UTC()
		current.Status = cloudmanaged.RunStatusRunning
		current.WorkerID = workerID
		current.StartedAt = now
		if workerID != "" {
			current.HeartbeatAt = now
		}
		current.UpdatedAt = now
		if err := s.updateRunConn(ctx, conn, current); err != nil {
			return err
		}
		record = current
		return nil
	})
	if err != nil {
		return cloudmanaged.RunRecord{}, err
	}
	return record, nil
}

// HeartbeatRun implements cloudmanaged.RunStoreWithHeartbeat.
func (s *Store) HeartbeatRun(ctx context.Context, id, workerID string) (cloudmanaged.RunRecord, error) {
	if err := contextError(ctx); err != nil {
		return cloudmanaged.RunRecord{}, err
	}
	if s == nil {
		return cloudmanaged.RunRecord{}, fmt.Errorf("sqlite run store is nil")
	}
	var record cloudmanaged.RunRecord
	err := s.withImmediateTx(ctx, func(conn *sql.Conn) error {
		current, err := s.getRunConn(ctx, conn, id)
		if err != nil {
			return err
		}
		if current.Status != cloudmanaged.RunStatusRunning {
			return cloudmanaged.ErrRunNotActive
		}
		if current.WorkerID != "" && workerID != "" && current.WorkerID != workerID {
			return cloudmanaged.ErrRunWorkerMismatch
		}
		now := time.Now().UTC()
		current.WorkerID = workerID
		current.HeartbeatAt = now
		current.UpdatedAt = now
		if err := s.updateRunConn(ctx, conn, current); err != nil {
			return err
		}
		record = current
		return nil
	})
	if err != nil {
		return cloudmanaged.RunRecord{}, err
	}
	return record, nil
}

// FailStaleRuns implements cloudmanaged.RunStoreWithHeartbeat.
func (s *Store) FailStaleRuns(ctx context.Context, staleBefore time.Time, reason string) (int64, error) {
	if err := contextError(ctx); err != nil {
		return 0, err
	}
	if s == nil {
		return 0, fmt.Errorf("sqlite run store is nil")
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE memax_cloudmanaged_runs
		SET status = ?, error = ?, completed_at_unix_ms = ?, updated_at_unix_ms = ?
		WHERE status = ? AND heartbeat_at_unix_ms IS NOT NULL AND heartbeat_at_unix_ms < ?
	`, string(cloudmanaged.RunStatusFailed), reason, unixMillis(time.Now().UTC()), unixMillis(time.Now().UTC()), string(cloudmanaged.RunStatusRunning), unixMillis(staleBefore.UTC()))
	if err != nil {
		return 0, fmt.Errorf("fail stale sqlite managed runs: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("fail stale sqlite managed runs rows affected: %w", err)
	}
	return rows, nil
}

// EnsureSession implements cloudmanaged.QuotaStore.
func (s *Store) EnsureSession(ctx context.Context, scope tenant.Scope, sessionID string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if s == nil {
		return fmt.Errorf("sqlite quota store is nil")
	}
	if sessionID == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO memax_cloudmanaged_quota_sessions (
			scope_key, session_id, updated_at_unix_ms
		) VALUES (?, ?, ?)
		ON CONFLICT(scope_key, session_id) DO UPDATE SET
			updated_at_unix_ms = excluded.updated_at_unix_ms
	`, scopekey.Digest(scope), sessionID, unixMillis(time.Now().UTC()))
	if err != nil {
		return fmt.Errorf("ensure sqlite quota session: %w", err)
	}
	return nil
}

// Reserve implements cloudmanaged.QuotaStore.
func (s *Store) Reserve(ctx context.Context, scope tenant.Scope, sessionID string, counter cloudmanaged.QuotaCounter, limit int) (int, bool, error) {
	if err := contextError(ctx); err != nil {
		return 0, false, err
	}
	if s == nil {
		return 0, false, fmt.Errorf("sqlite quota store is nil")
	}
	if sessionID == "" || limit <= 0 {
		return 0, true, nil
	}
	column, err := counterColumn(counter)
	if err != nil {
		return 0, false, err
	}

	var (
		used    int
		granted bool
	)
	err = s.withImmediateTx(ctx, func(conn *sql.Conn) error {
		if err := s.ensureSessionConn(ctx, conn, scope, sessionID); err != nil {
			return err
		}

		scopeKey := scopekey.Digest(scope)
		query := fmt.Sprintf(`
			SELECT %s
			FROM memax_cloudmanaged_quota_sessions
			WHERE scope_key = ? AND session_id = ?
			LIMIT 1
		`, column)
		if err := conn.QueryRowContext(ctx, query, scopeKey, sessionID).Scan(&used); err != nil {
			return fmt.Errorf("read sqlite quota usage: %w", err)
		}
		if used >= limit {
			if _, err := conn.ExecContext(ctx, `
				UPDATE memax_cloudmanaged_quota_sessions
				SET updated_at_unix_ms = ?
				WHERE scope_key = ? AND session_id = ?
			`, unixMillis(time.Now().UTC()), scopeKey, sessionID); err != nil {
				return fmt.Errorf("touch denied sqlite quota session: %w", err)
			}
			granted = false
			return nil
		}
		update := fmt.Sprintf(`
			UPDATE memax_cloudmanaged_quota_sessions
			SET %s = %s + 1, updated_at_unix_ms = ?
			WHERE scope_key = ? AND session_id = ?
		`, column, column)
		if _, err := conn.ExecContext(ctx, update, unixMillis(time.Now().UTC()), scopeKey, sessionID); err != nil {
			return fmt.Errorf("reserve sqlite quota: %w", err)
		}
		used++
		granted = true
		return nil
	})
	if err != nil {
		return 0, false, err
	}
	return used, granted, nil
}

// ResetSession implements cloudmanaged.QuotaStore.
func (s *Store) ResetSession(ctx context.Context, scope tenant.Scope, sessionID string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if s == nil {
		return fmt.Errorf("sqlite quota store is nil")
	}
	if sessionID == "" {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `
		DELETE FROM memax_cloudmanaged_quota_sessions
		WHERE scope_key = ? AND session_id = ?
	`, scopekey.Digest(scope), sessionID); err != nil {
		return fmt.Errorf("reset sqlite quota session: %w", err)
	}
	return nil
}

// PruneBefore deletes sessions whose last update happened before cutoff.
// Hosts are expected to call it from their own periodic cleanup loop.
func (s *Store) PruneBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	if err := contextError(ctx); err != nil {
		return 0, err
	}
	if s == nil {
		return 0, fmt.Errorf("sqlite quota store is nil")
	}
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM memax_cloudmanaged_quota_sessions
		WHERE updated_at_unix_ms < ?
	`, unixMillis(cutoff.UTC()))
	if err != nil {
		return 0, fmt.Errorf("prune sqlite quota sessions: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("prune sqlite quota sessions rows affected: %w", err)
	}
	return deleted, nil
}

func (s *Store) init(ctx context.Context) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS memax_cloudmanaged_quota_sessions (
			scope_key TEXT NOT NULL,
			session_id TEXT NOT NULL,
			model_requests INTEGER NOT NULL DEFAULT 0,
			tool_uses INTEGER NOT NULL DEFAULT 0,
			updated_at_unix_ms INTEGER NOT NULL,
			PRIMARY KEY(scope_key, session_id)
		)
	`)
	if err != nil {
		return fmt.Errorf("init sqlite quota schema: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS memax_cloudmanaged_quota_sessions_updated_idx
		ON memax_cloudmanaged_quota_sessions(updated_at_unix_ms)
	`)
	if err != nil {
		return fmt.Errorf("init sqlite quota updated index: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS memax_cloudmanaged_runs (
			id TEXT PRIMARY KEY,
			status TEXT NOT NULL,
			prompt TEXT NOT NULL,
			tenant_id TEXT NOT NULL,
			subject_id TEXT NOT NULL,
			tenant_attributes_json TEXT NOT NULL,
			session_id TEXT NOT NULL DEFAULT '',
			parent_session_id TEXT NOT NULL DEFAULT '',
			worker_id TEXT NOT NULL DEFAULT '',
			result TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			created_at_unix_ms INTEGER NOT NULL,
			started_at_unix_ms INTEGER,
			heartbeat_at_unix_ms INTEGER,
			completed_at_unix_ms INTEGER,
			updated_at_unix_ms INTEGER NOT NULL
		)
	`)
	if err != nil {
		return fmt.Errorf("init sqlite run schema: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS memax_cloudmanaged_runs_updated_idx
		ON memax_cloudmanaged_runs(updated_at_unix_ms)
	`)
	if err != nil {
		return fmt.Errorf("init sqlite run updated index: %w", err)
	}
	if err := s.ensureRunColumn(ctx, "worker_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureRunColumn(ctx, "heartbeat_at_unix_ms", "INTEGER"); err != nil {
		return err
	}
	return nil
}

func (s *Store) ensureSessionConn(ctx context.Context, conn *sql.Conn, scope tenant.Scope, sessionID string) error {
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO memax_cloudmanaged_quota_sessions (
			scope_key, session_id, updated_at_unix_ms
		) VALUES (?, ?, ?)
		ON CONFLICT(scope_key, session_id) DO UPDATE SET
			updated_at_unix_ms = excluded.updated_at_unix_ms
	`, scopekey.Digest(scope), sessionID, unixMillis(time.Now().UTC())); err != nil {
		return fmt.Errorf("ensure sqlite quota session in transaction: %w", err)
	}
	return nil
}

func (s *Store) withImmediateTx(ctx context.Context, fn func(*sql.Conn) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire sqlite quota connection: %w", err)
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
		return fmt.Errorf("begin sqlite quota transaction: %w", err)
	}
	started = true
	if err := fn(conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit sqlite quota transaction: %w", err)
	}
	committed = true
	return nil
}

func counterColumn(counter cloudmanaged.QuotaCounter) (string, error) {
	switch counter {
	case cloudmanaged.QuotaCounterModelRequests:
		return "model_requests", nil
	case cloudmanaged.QuotaCounterToolUses:
		return "tool_uses", nil
	default:
		return "", fmt.Errorf("unknown quota counter %q", counter)
	}
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

func (s *Store) getRunConn(ctx context.Context, conn *sql.Conn, id string) (cloudmanaged.RunRecord, error) {
	if id == "" {
		return cloudmanaged.RunRecord{}, fmt.Errorf("run id is required")
	}
	var (
		record            cloudmanaged.RunRecord
		status            string
		tenantAttrsJSON   string
		startedAtUnixMS   sql.NullInt64
		heartbeatAtUnixMS sql.NullInt64
		completedAtUnixMS sql.NullInt64
	)
	err := conn.QueryRowContext(ctx, `
		SELECT id, status, prompt, tenant_id, subject_id, tenant_attributes_json,
			session_id, parent_session_id, worker_id, result, error, created_at_unix_ms,
			started_at_unix_ms, heartbeat_at_unix_ms, completed_at_unix_ms, updated_at_unix_ms
		FROM memax_cloudmanaged_runs
		WHERE id = ?
		LIMIT 1
	`, id).Scan(
		&record.ID,
		&status,
		&record.Prompt,
		&record.Tenant.ID,
		&record.Tenant.SubjectID,
		&tenantAttrsJSON,
		&record.SessionID,
		&record.ParentSessionID,
		&record.WorkerID,
		&record.Result,
		&record.Error,
		(*unixMillisTime)(&record.CreatedAt),
		&startedAtUnixMS,
		&heartbeatAtUnixMS,
		&completedAtUnixMS,
		(*unixMillisTime)(&record.UpdatedAt),
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return cloudmanaged.RunRecord{}, cloudmanaged.ErrRunNotFound
		}
		return cloudmanaged.RunRecord{}, fmt.Errorf("get sqlite managed run %s: %w", id, err)
	}
	record.Status = cloudmanaged.RunStatus(status)
	attrs, err := unmarshalAttributes(tenantAttrsJSON)
	if err != nil {
		return cloudmanaged.RunRecord{}, fmt.Errorf("decode sqlite run tenant attributes: %w", err)
	}
	record.Tenant.Attributes = attrs
	if startedAtUnixMS.Valid {
		record.StartedAt = time.UnixMilli(startedAtUnixMS.Int64).UTC()
	}
	if heartbeatAtUnixMS.Valid {
		record.HeartbeatAt = time.UnixMilli(heartbeatAtUnixMS.Int64).UTC()
	}
	if completedAtUnixMS.Valid {
		record.CompletedAt = time.UnixMilli(completedAtUnixMS.Int64).UTC()
	}
	return record, nil
}

func (s *Store) updateRunConn(ctx context.Context, conn *sql.Conn, current cloudmanaged.RunRecord) error {
	var startedAt any
	if !current.StartedAt.IsZero() {
		startedAt = unixMillis(current.StartedAt)
	}
	var heartbeatAt any
	if !current.HeartbeatAt.IsZero() {
		heartbeatAt = unixMillis(current.HeartbeatAt)
	}
	var completedAt any
	if !current.CompletedAt.IsZero() {
		completedAt = unixMillis(current.CompletedAt)
	}
	if _, err := conn.ExecContext(ctx, `
		UPDATE memax_cloudmanaged_runs
		SET status = ?, session_id = ?, parent_session_id = ?, worker_id = ?, result = ?, error = ?,
			started_at_unix_ms = ?, heartbeat_at_unix_ms = ?, completed_at_unix_ms = ?, updated_at_unix_ms = ?
		WHERE id = ?
	`, string(current.Status), current.SessionID, current.ParentSessionID, current.WorkerID, current.Result, current.Error, startedAt, heartbeatAt, completedAt, unixMillis(current.UpdatedAt), current.ID); err != nil {
		return fmt.Errorf("update sqlite managed run: %w", err)
	}
	return nil
}

func (s *Store) ensureRunColumn(ctx context.Context, name, definition string) error {
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE memax_cloudmanaged_runs ADD COLUMN %s %s`, name, definition))
	if err == nil {
		return nil
	}
	if sqliteDuplicateColumn(err) {
		return nil
	}
	return fmt.Errorf("ensure sqlite run column %s: %w", name, err)
}

func marshalAttributes(attributes map[string]string) (string, error) {
	if len(attributes) == 0 {
		return "{}", nil
	}
	data, err := json.Marshal(attributes)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func unmarshalAttributes(raw string) (map[string]string, error) {
	if raw == "" {
		return nil, nil
	}
	attributes := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &attributes); err != nil {
		return nil, err
	}
	if len(attributes) == 0 {
		return nil, nil
	}
	return attributes, nil
}

func newRunID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate sqlite run id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
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

func sqliteDuplicateColumn(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "duplicate column name") || strings.Contains(err.Error(), "already exists"))
}
