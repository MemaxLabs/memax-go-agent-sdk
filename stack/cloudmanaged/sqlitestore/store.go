// Package sqlitestore provides a durable SQLite-backed cloudmanaged.QuotaStore.
//
// The store preserves the QuotaStore atomicity contract with SQLite
// BEGIN IMMEDIATE transactions, so concurrent reservations serialize before the
// limit check and update are applied. Unlike the Redis-backed store, SQLite
// does not apply TTL expiry automatically; normal session-end cleanup should
// call ResetSession, and hosts can use host-scheduled PruneBefore calls to
// clear stale rows from crashed or abandoned runs.
package sqlitestore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/stack/cloudmanaged"
	"github.com/MemaxLabs/memax-go-agent-sdk/stack/cloudmanaged/internal/scopekey"
	"github.com/MemaxLabs/memax-go-agent-sdk/tenant"
)

// Store is a SQLite-backed cloudmanaged.QuotaStore.
type Store struct {
	db *sql.DB
}

// New initializes and returns a SQLite-backed quota store.
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
