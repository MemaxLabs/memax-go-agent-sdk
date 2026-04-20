// Package sqlitestore provides a durable SQLite-backed personal scheduled-run
// store and scheduled-run notification outbox.
//
// The store preserves the ScheduledRunStore idempotency contract with SQLite
// BEGIN IMMEDIATE transactions, so concurrent watchers attempting the same
// trigger occurrence serialize before the create-if-missing check runs. The
// notification outbox uses the same transaction discipline for idempotent
// run/status records, so duplicate observer deliveries do not create duplicate
// notifications. Its notification delivery extension also serializes claim
// and ack transitions so separate host workers can drain the outbox with
// at-least-once delivery and expired-lease reclaim. Unlike
// cloudmanaged.RunStore, this store is keyed by deterministic trigger intent
// rather than worker claim or tenant identity.
package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/stack/personal"
)

// Store is a SQLite-backed personal.ScheduledRunStore.
type Store struct {
	db *sql.DB
}

var (
	_ personal.ScheduledRunStore                        = (*Store)(nil)
	_ personal.ScheduledRunStoreWithStaleReconciliation = (*Store)(nil)
	_ personal.ScheduledRunNotificationStore            = (*Store)(nil)
	_ personal.ScheduledRunNotificationDeliveryStore    = (*Store)(nil)
)

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

// CreateScheduledRunNotification implements
// personal.ScheduledRunNotificationStore.
func (s *Store) CreateScheduledRunNotification(ctx context.Context, req personal.CreateScheduledRunNotificationRequest) (personal.ScheduledRunNotificationRecord, bool, error) {
	if err := contextError(ctx); err != nil {
		return personal.ScheduledRunNotificationRecord{}, false, err
	}
	if s == nil {
		return personal.ScheduledRunNotificationRecord{}, false, fmt.Errorf("sqlite scheduled run notification store is nil")
	}
	record, err := req.Normalize()
	if err != nil {
		return personal.ScheduledRunNotificationRecord{}, false, err
	}

	var created bool
	err = s.withImmediateTx(ctx, func(conn *sql.Conn) error {
		existing, err := s.getScheduledRunNotificationConn(ctx, conn, record.ID)
		if err == nil {
			record = existing
			created = false
			return nil
		}
		if !errors.Is(err, personal.ErrScheduledRunNotificationNotFound) {
			return err
		}
		if _, err := conn.ExecContext(ctx, `
			INSERT INTO memax_personal_scheduled_run_notifications (
				id, run_id, status, trigger_name, occurrence_at_unix_ms,
				prompt, result, error, created_at_unix_ms, delivery_status,
				delivery_worker_id, delivery_attempts, delivery_error,
				deliver_after_unix_ms, delivered_at_unix_ms,
				delivery_updated_at_unix_ms
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, record.ID, record.RunID, string(record.Status), record.TriggerName,
			unixMillis(record.OccurrenceAt), record.Prompt, record.Result, record.Error,
			unixMillis(record.CreatedAt), string(record.DeliveryStatus), record.DeliveryWorkerID,
			record.DeliveryAttempts, record.DeliveryError, unixMillis(record.DeliverAfter),
			nilTime(record.DeliveredAt), unixMillis(record.DeliveryUpdatedAt)); err != nil {
			return fmt.Errorf("create sqlite scheduled run notification: %w", err)
		}
		created = true
		return nil
	})
	if err != nil {
		return personal.ScheduledRunNotificationRecord{}, false, err
	}
	return record, created, nil
}

// ListScheduledRunNotifications implements personal.ScheduledRunNotificationStore.
func (s *Store) ListScheduledRunNotifications(ctx context.Context, filter personal.ScheduledRunNotificationFilter) ([]personal.ScheduledRunNotificationRecord, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("sqlite scheduled run notification store is nil")
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire sqlite scheduled run connection: %w", err)
	}
	defer conn.Close()

	var (
		query strings.Builder
		args  []any
	)
	query.WriteString(`
		SELECT id, run_id, status, trigger_name, occurrence_at_unix_ms,
			prompt, result, error, created_at_unix_ms, delivery_status,
			delivery_worker_id, delivery_attempts, delivery_error,
			deliver_after_unix_ms, delivered_at_unix_ms,
			delivery_updated_at_unix_ms
		FROM memax_personal_scheduled_run_notifications
		WHERE 1 = 1
	`)
	if strings.TrimSpace(filter.RunID) != "" {
		query.WriteString(" AND run_id = ?")
		args = append(args, strings.TrimSpace(filter.RunID))
	}
	if filter.Status != "" {
		query.WriteString(" AND status = ?")
		args = append(args, string(filter.Status))
	}
	if filter.DeliveryStatus != "" {
		query.WriteString(" AND delivery_status = ?")
		args = append(args, string(filter.DeliveryStatus))
	}
	query.WriteString(" ORDER BY created_at_unix_ms ASC, id ASC")
	if filter.Limit > 0 {
		query.WriteString(" LIMIT ?")
		args = append(args, filter.Limit)
	}

	rows, err := conn.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("list sqlite scheduled run notifications: %w", err)
	}
	defer rows.Close()

	notifications := make([]personal.ScheduledRunNotificationRecord, 0)
	for rows.Next() {
		record, err := scanScheduledRunNotification(rows)
		if err != nil {
			return nil, err
		}
		notifications = append(notifications, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sqlite scheduled run notifications: %w", err)
	}
	return notifications, nil
}

// ClaimScheduledRunNotifications implements
// personal.ScheduledRunNotificationDeliveryStore.
func (s *Store) ClaimScheduledRunNotifications(ctx context.Context, req personal.ClaimScheduledRunNotificationsRequest) ([]personal.ScheduledRunNotificationRecord, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("sqlite scheduled run notification store is nil")
	}
	claim, err := normalizeNotificationClaim(req)
	if err != nil {
		return nil, err
	}

	var claimed []personal.ScheduledRunNotificationRecord
	err = s.withImmediateTx(ctx, func(conn *sql.Conn) error {
		rows, err := conn.QueryContext(ctx, `
			SELECT id
			FROM memax_personal_scheduled_run_notifications
			WHERE delivery_status IN (?, ?, ?)
				AND deliver_after_unix_ms <= ?
			ORDER BY deliver_after_unix_ms ASC, created_at_unix_ms ASC, id ASC
			LIMIT ?
		`,
			string(personal.ScheduledRunNotificationDeliveryPending),
			string(personal.ScheduledRunNotificationDeliveryFailed),
			string(personal.ScheduledRunNotificationDeliveryDelivering),
			unixMillis(claim.now),
			claim.limit,
		)
		if err != nil {
			return fmt.Errorf("query sqlite scheduled run notifications to claim: %w", err)
		}
		defer rows.Close()
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return fmt.Errorf("scan sqlite scheduled run notification claim id: %w", err)
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate sqlite scheduled run notification claim ids: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close sqlite scheduled run notification claim ids: %w", err)
		}

		claimed = make([]personal.ScheduledRunNotificationRecord, 0, len(ids))
		leaseUntil := claim.now.Add(claim.leaseDuration).UTC()
		for _, id := range ids {
			result, err := conn.ExecContext(ctx, `
				UPDATE memax_personal_scheduled_run_notifications
				SET delivery_status = ?,
					delivery_worker_id = ?,
					delivery_attempts = delivery_attempts + 1,
					delivery_error = '',
					deliver_after_unix_ms = ?,
					delivery_updated_at_unix_ms = ?
				WHERE id = ?
					AND delivery_status IN (?, ?, ?)
					AND deliver_after_unix_ms <= ?
			`,
				string(personal.ScheduledRunNotificationDeliveryDelivering),
				claim.workerID,
				unixMillis(leaseUntil),
				unixMillis(claim.now),
				id,
				string(personal.ScheduledRunNotificationDeliveryPending),
				string(personal.ScheduledRunNotificationDeliveryFailed),
				string(personal.ScheduledRunNotificationDeliveryDelivering),
				unixMillis(claim.now),
			)
			if err != nil {
				return fmt.Errorf("claim sqlite scheduled run notification %s: %w", id, err)
			}
			affected, err := result.RowsAffected()
			if err != nil {
				return fmt.Errorf("inspect sqlite scheduled run notification claim %s: %w", id, err)
			}
			if affected == 0 {
				continue
			}
			record, err := s.getScheduledRunNotificationConn(ctx, conn, id)
			if err != nil {
				return err
			}
			claimed = append(claimed, record)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return claimed, nil
}

// MarkScheduledRunNotificationDelivered implements
// personal.ScheduledRunNotificationDeliveryStore.
func (s *Store) MarkScheduledRunNotificationDelivered(ctx context.Context, req personal.MarkScheduledRunNotificationDeliveredRequest) (personal.ScheduledRunNotificationRecord, error) {
	if err := contextError(ctx); err != nil {
		return personal.ScheduledRunNotificationRecord{}, err
	}
	if s == nil {
		return personal.ScheduledRunNotificationRecord{}, fmt.Errorf("sqlite scheduled run notification store is nil")
	}
	update, err := normalizeNotificationDelivered(req)
	if err != nil {
		return personal.ScheduledRunNotificationRecord{}, err
	}
	var record personal.ScheduledRunNotificationRecord
	err = s.withImmediateTx(ctx, func(conn *sql.Conn) error {
		current, err := s.getScheduledRunNotificationConn(ctx, conn, update.id)
		if err != nil {
			return err
		}
		if current.DeliveryStatus == personal.ScheduledRunNotificationDeliveryDelivered {
			if current.DeliveryWorkerID == update.workerID {
				record = current
				return nil
			}
			return personal.ErrScheduledRunNotificationWorkerMismatch
		}
		if err := ensureNotificationDeliveryWorker(current, update.workerID); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `
			UPDATE memax_personal_scheduled_run_notifications
			SET delivery_status = ?,
				delivery_worker_id = ?,
				delivery_error = '',
				deliver_after_unix_ms = ?,
				delivered_at_unix_ms = ?,
				delivery_updated_at_unix_ms = ?
			WHERE id = ?
		`,
			string(personal.ScheduledRunNotificationDeliveryDelivered),
			update.workerID,
			unixMillis(update.deliveredAt),
			unixMillis(update.deliveredAt),
			unixMillis(update.deliveredAt),
			update.id,
		); err != nil {
			return fmt.Errorf("mark sqlite scheduled run notification delivered %s: %w", update.id, err)
		}
		record, err = s.getScheduledRunNotificationConn(ctx, conn, update.id)
		return err
	})
	if err != nil {
		return personal.ScheduledRunNotificationRecord{}, err
	}
	return record, nil
}

// MarkScheduledRunNotificationFailed implements
// personal.ScheduledRunNotificationDeliveryStore.
func (s *Store) MarkScheduledRunNotificationFailed(ctx context.Context, req personal.MarkScheduledRunNotificationFailedRequest) (personal.ScheduledRunNotificationRecord, error) {
	if err := contextError(ctx); err != nil {
		return personal.ScheduledRunNotificationRecord{}, err
	}
	if s == nil {
		return personal.ScheduledRunNotificationRecord{}, fmt.Errorf("sqlite scheduled run notification store is nil")
	}
	update, err := normalizeNotificationFailed(req)
	if err != nil {
		return personal.ScheduledRunNotificationRecord{}, err
	}
	var record personal.ScheduledRunNotificationRecord
	err = s.withImmediateTx(ctx, func(conn *sql.Conn) error {
		current, err := s.getScheduledRunNotificationConn(ctx, conn, update.id)
		if err != nil {
			return err
		}
		if err := ensureNotificationDeliveryWorker(current, update.workerID); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `
			UPDATE memax_personal_scheduled_run_notifications
			SET delivery_status = ?,
				delivery_worker_id = '',
				delivery_error = ?,
				deliver_after_unix_ms = ?,
				delivery_updated_at_unix_ms = ?
			WHERE id = ?
			`,
			string(personal.ScheduledRunNotificationDeliveryFailed),
			update.errorText,
			unixMillis(update.retryAt),
			unixMillis(update.failedAt),
			update.id,
		); err != nil {
			return fmt.Errorf("mark sqlite scheduled run notification failed %s: %w", update.id, err)
		}
		record, err = s.getScheduledRunNotificationConn(ctx, conn, update.id)
		return err
	})
	if err != nil {
		return personal.ScheduledRunNotificationRecord{}, err
	}
	return record, nil
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
	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS memax_personal_scheduled_run_notifications (
			id TEXT PRIMARY KEY,
			run_id TEXT NOT NULL,
			status TEXT NOT NULL,
			trigger_name TEXT NOT NULL,
			occurrence_at_unix_ms INTEGER NOT NULL,
			prompt TEXT NOT NULL,
			result TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			created_at_unix_ms INTEGER NOT NULL,
			delivery_status TEXT NOT NULL DEFAULT 'pending',
			delivery_worker_id TEXT NOT NULL DEFAULT '',
			delivery_attempts INTEGER NOT NULL DEFAULT 0,
			delivery_error TEXT NOT NULL DEFAULT '',
			deliver_after_unix_ms INTEGER NOT NULL DEFAULT 0,
			delivered_at_unix_ms INTEGER,
			delivery_updated_at_unix_ms INTEGER NOT NULL DEFAULT 0
		)
	`); err != nil {
		return fmt.Errorf("init sqlite scheduled run notification schema: %w", err)
	}
	for _, migration := range []struct {
		column string
		ddl    string
	}{
		{"delivery_status", "ALTER TABLE memax_personal_scheduled_run_notifications ADD COLUMN delivery_status TEXT NOT NULL DEFAULT 'pending'"},
		{"delivery_worker_id", "ALTER TABLE memax_personal_scheduled_run_notifications ADD COLUMN delivery_worker_id TEXT NOT NULL DEFAULT ''"},
		{"delivery_attempts", "ALTER TABLE memax_personal_scheduled_run_notifications ADD COLUMN delivery_attempts INTEGER NOT NULL DEFAULT 0"},
		{"delivery_error", "ALTER TABLE memax_personal_scheduled_run_notifications ADD COLUMN delivery_error TEXT NOT NULL DEFAULT ''"},
		{"deliver_after_unix_ms", "ALTER TABLE memax_personal_scheduled_run_notifications ADD COLUMN deliver_after_unix_ms INTEGER NOT NULL DEFAULT 0"},
		{"delivered_at_unix_ms", "ALTER TABLE memax_personal_scheduled_run_notifications ADD COLUMN delivered_at_unix_ms INTEGER"},
		{"delivery_updated_at_unix_ms", "ALTER TABLE memax_personal_scheduled_run_notifications ADD COLUMN delivery_updated_at_unix_ms INTEGER NOT NULL DEFAULT 0"},
	} {
		if err := s.ensureScheduledRunNotificationColumn(ctx, migration.column, migration.ddl); err != nil {
			return err
		}
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE memax_personal_scheduled_run_notifications
		SET deliver_after_unix_ms = created_at_unix_ms
		WHERE deliver_after_unix_ms = 0
	`); err != nil {
		return fmt.Errorf("backfill sqlite scheduled run notification deliver_after: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE memax_personal_scheduled_run_notifications
		SET delivery_updated_at_unix_ms = created_at_unix_ms
		WHERE delivery_updated_at_unix_ms = 0
	`); err != nil {
		return fmt.Errorf("backfill sqlite scheduled run notification delivery updated at: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS memax_personal_scheduled_run_notifications_run_idx
		ON memax_personal_scheduled_run_notifications(run_id, created_at_unix_ms)
	`); err != nil {
		return fmt.Errorf("init sqlite scheduled run notification run index: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS memax_personal_scheduled_run_notifications_status_idx
		ON memax_personal_scheduled_run_notifications(status, created_at_unix_ms)
	`); err != nil {
		return fmt.Errorf("init sqlite scheduled run notification status index: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS memax_personal_scheduled_run_notifications_delivery_idx
		ON memax_personal_scheduled_run_notifications(delivery_status, deliver_after_unix_ms, created_at_unix_ms)
	`); err != nil {
		return fmt.Errorf("init sqlite scheduled run notification delivery index: %w", err)
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

func (s *Store) ensureScheduledRunNotificationColumn(ctx context.Context, column, ddl string) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(memax_personal_scheduled_run_notifications)`)
	if err != nil {
		return fmt.Errorf("inspect sqlite scheduled run notification schema: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid       int
			name      string
			typeName  string
			notNull   int
			defaultV  any
			primaryID int
		)
		if err := rows.Scan(&cid, &name, &typeName, &notNull, &defaultV, &primaryID); err != nil {
			return fmt.Errorf("scan sqlite scheduled run notification schema: %w", err)
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate sqlite scheduled run notification schema: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("migrate sqlite scheduled run notification schema %s: %w", column, err)
	}
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

func (s *Store) getScheduledRunNotificationConn(ctx context.Context, conn *sql.Conn, id string) (personal.ScheduledRunNotificationRecord, error) {
	if id == "" {
		return personal.ScheduledRunNotificationRecord{}, fmt.Errorf("scheduled run notification id is required")
	}
	row := conn.QueryRowContext(ctx, `
		SELECT id, run_id, status, trigger_name, occurrence_at_unix_ms,
			prompt, result, error, created_at_unix_ms, delivery_status,
			delivery_worker_id, delivery_attempts, delivery_error,
			deliver_after_unix_ms, delivered_at_unix_ms,
			delivery_updated_at_unix_ms
		FROM memax_personal_scheduled_run_notifications
		WHERE id = ?
		LIMIT 1
	`, id)
	record, err := scanScheduledRunNotification(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return personal.ScheduledRunNotificationRecord{}, personal.ErrScheduledRunNotificationNotFound
		}
		return personal.ScheduledRunNotificationRecord{}, fmt.Errorf("get sqlite scheduled run notification %s: %w", id, err)
	}
	return record, nil
}

type scheduledRunNotificationScanner interface {
	Scan(dest ...any) error
}

func scanScheduledRunNotification(scanner scheduledRunNotificationScanner) (personal.ScheduledRunNotificationRecord, error) {
	var (
		record            personal.ScheduledRunNotificationRecord
		status            string
		deliveryStatus    string
		deliveredAtUnixMS sql.NullInt64
	)
	if err := scanner.Scan(
		&record.ID,
		&record.RunID,
		&status,
		&record.TriggerName,
		(*unixMillisTime)(&record.OccurrenceAt),
		&record.Prompt,
		&record.Result,
		&record.Error,
		(*unixMillisTime)(&record.CreatedAt),
		&deliveryStatus,
		&record.DeliveryWorkerID,
		&record.DeliveryAttempts,
		&record.DeliveryError,
		(*unixMillisTime)(&record.DeliverAfter),
		&deliveredAtUnixMS,
		(*unixMillisTime)(&record.DeliveryUpdatedAt),
	); err != nil {
		return personal.ScheduledRunNotificationRecord{}, fmt.Errorf("scan sqlite scheduled run notification: %w", err)
	}
	record.Status = personal.ScheduledRunStatus(status)
	record.DeliveryStatus = personal.ScheduledRunNotificationDeliveryStatus(deliveryStatus)
	if deliveredAtUnixMS.Valid {
		record.DeliveredAt = time.UnixMilli(deliveredAtUnixMS.Int64).UTC()
	}
	return record, nil
}

type normalizedNotificationClaim struct {
	workerID      string
	limit         int
	now           time.Time
	leaseDuration time.Duration
}

func normalizeNotificationClaim(req personal.ClaimScheduledRunNotificationsRequest) (normalizedNotificationClaim, error) {
	claim := normalizedNotificationClaim{
		workerID:      strings.TrimSpace(req.WorkerID),
		limit:         req.Limit,
		now:           req.Now.UTC(),
		leaseDuration: req.LeaseDuration,
	}
	if claim.workerID == "" {
		return normalizedNotificationClaim{}, fmt.Errorf("scheduled run notification delivery worker id is required")
	}
	if claim.limit <= 0 {
		claim.limit = 1
	}
	if claim.now.IsZero() {
		claim.now = time.Now().UTC()
	}
	if claim.leaseDuration <= 0 {
		claim.leaseDuration = personal.DefaultScheduledRunNotificationLeaseDuration
	}
	return claim, nil
}

type normalizedNotificationDelivered struct {
	id          string
	workerID    string
	deliveredAt time.Time
}

func normalizeNotificationDelivered(req personal.MarkScheduledRunNotificationDeliveredRequest) (normalizedNotificationDelivered, error) {
	update := normalizedNotificationDelivered{
		id:          strings.TrimSpace(req.ID),
		workerID:    strings.TrimSpace(req.WorkerID),
		deliveredAt: req.DeliveredAt.UTC(),
	}
	if update.id == "" {
		return normalizedNotificationDelivered{}, fmt.Errorf("scheduled run notification id is required")
	}
	if update.workerID == "" {
		return normalizedNotificationDelivered{}, fmt.Errorf("scheduled run notification delivery worker id is required")
	}
	if update.deliveredAt.IsZero() {
		update.deliveredAt = time.Now().UTC()
	}
	return update, nil
}

type normalizedNotificationFailed struct {
	id        string
	workerID  string
	errorText string
	retryAt   time.Time
	failedAt  time.Time
}

func normalizeNotificationFailed(req personal.MarkScheduledRunNotificationFailedRequest) (normalizedNotificationFailed, error) {
	update := normalizedNotificationFailed{
		id:        strings.TrimSpace(req.ID),
		workerID:  strings.TrimSpace(req.WorkerID),
		errorText: strings.TrimSpace(req.Error),
		retryAt:   req.RetryAt.UTC(),
		failedAt:  req.FailedAt.UTC(),
	}
	if update.id == "" {
		return normalizedNotificationFailed{}, fmt.Errorf("scheduled run notification id is required")
	}
	if update.workerID == "" {
		return normalizedNotificationFailed{}, fmt.Errorf("scheduled run notification delivery worker id is required")
	}
	if update.errorText == "" {
		return normalizedNotificationFailed{}, fmt.Errorf("scheduled run notification delivery error is required")
	}
	if update.retryAt.IsZero() {
		update.retryAt = time.Now().UTC()
	}
	if update.failedAt.IsZero() {
		update.failedAt = time.Now().UTC()
	}
	return update, nil
}

func ensureNotificationDeliveryWorker(record personal.ScheduledRunNotificationRecord, workerID string) error {
	if record.DeliveryStatus != personal.ScheduledRunNotificationDeliveryDelivering {
		return personal.ErrScheduledRunNotificationNotDelivering
	}
	if record.DeliveryWorkerID != "" && record.DeliveryWorkerID != workerID {
		return personal.ErrScheduledRunNotificationWorkerMismatch
	}
	return nil
}

func unixMillis(t time.Time) int64 {
	return t.UnixMilli()
}

func nilTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return unixMillis(t)
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
