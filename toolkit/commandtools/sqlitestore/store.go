// Package sqlitestore provides a durable SQLite-backed command transcript
// store.
//
// The store preserves commandtools.CommandTranscriptStore semantics with SQLite
// BEGIN IMMEDIATE transactions, so concurrent snapshot saves and output
// appends serialize before sequence validation and persisted transcript updates
// are applied. Unlike session.Store, this package persists tool-owned command
// transcript state: command-session snapshots, ordered output chunks, and
// visibility metadata remain durable across manager restarts without moving
// background process state into the kernel conversation store.
package sqlitestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/identity"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/commandtools"
)

const (
	defaultReadChunks = 32
	defaultReadBytes  = 16 * 1024
)

// Store is a SQLite-backed commandtools.CommandTranscriptStore.
type Store struct {
	db *sql.DB
}

var _ commandtools.CommandTranscriptStore = (*Store)(nil)

// New initializes and returns a SQLite-backed command transcript store.
func New(ctx context.Context, db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("sqlite command transcript store db is required")
	}
	store := &Store{db: db}
	if err := store.init(ctx); err != nil {
		return nil, err
	}
	return store, nil
}

// SaveCommandSession implements commandtools.CommandTranscriptStore.
func (s *Store) SaveCommandSession(ctx context.Context, session commandtools.CommandSession) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if s == nil {
		return fmt.Errorf("sqlite command transcript store is nil")
	}
	if session.ID == "" {
		return fmt.Errorf("commandtools: command session id is required")
	}
	ctx = contextOrBackground(ctx)
	return s.withImmediateTx(ctx, func(conn *sql.Conn) error {
		lastSeq, err := s.lastChunkSeqConn(ctx, conn, session.ID)
		if err != nil {
			return err
		}
		if session.NextSeq <= lastSeq {
			session.NextSeq = lastSeq + 1
		}
		return s.upsertSessionConn(ctx, conn, session)
	})
}

// AppendCommandOutput implements commandtools.CommandTranscriptStore.
func (s *Store) AppendCommandOutput(ctx context.Context, id string, chunks []commandtools.OutputChunk) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if s == nil {
		return fmt.Errorf("sqlite command transcript store is nil")
	}
	if id == "" {
		return fmt.Errorf("commandtools: command session id is required")
	}
	if len(chunks) == 0 {
		return nil
	}
	ctx = contextOrBackground(ctx)
	return s.withImmediateTx(ctx, func(conn *sql.Conn) error {
		nextSeq, err := s.sessionNextSeqConn(ctx, conn, id)
		if err != nil {
			return err
		}
		lastSeq, err := s.lastChunkSeqConn(ctx, conn, id)
		if err != nil {
			return err
		}
		for _, chunk := range chunks {
			if chunk.Seq <= 0 {
				return fmt.Errorf("commandtools: output chunk seq must be positive")
			}
			if chunk.Seq <= lastSeq {
				return fmt.Errorf("commandtools: output chunk seq %d must be greater than previous seq %d", chunk.Seq, lastSeq)
			}
			lastSeq = chunk.Seq
		}
		for _, chunk := range chunks {
			if _, err := conn.ExecContext(ctx, `
				INSERT INTO memax_command_transcript_chunks (
					command_id, seq, stream, text, time_unix_ms
				) VALUES (?, ?, ?, ?, ?)
			`, id, chunk.Seq, chunk.Stream, chunk.Text, unixMillis(chunk.Time)); err != nil {
				return fmt.Errorf("append sqlite command transcript chunk %s/%d: %w", id, chunk.Seq, err)
			}
		}
		if nextSeq <= lastSeq {
			nextSeq = lastSeq + 1
		}
		if _, err := conn.ExecContext(ctx, `
			UPDATE memax_command_transcript_sessions
			SET next_seq = ?
			WHERE id = ?
		`, nextSeq, id); err != nil {
			return fmt.Errorf("update sqlite command transcript next seq %s: %w", id, err)
		}
		return nil
	})
}

// CommandSession implements commandtools.CommandTranscriptStore.
func (s *Store) CommandSession(ctx context.Context, id string) (commandtools.CommandSession, error) {
	if err := contextError(ctx); err != nil {
		return commandtools.CommandSession{}, err
	}
	if s == nil {
		return commandtools.CommandSession{}, fmt.Errorf("sqlite command transcript store is nil")
	}
	if id == "" {
		return commandtools.CommandSession{}, fmt.Errorf("commandtools: command session id is required")
	}
	ctx = contextOrBackground(ctx)
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return commandtools.CommandSession{}, fmt.Errorf("acquire sqlite command transcript connection: %w", err)
	}
	defer conn.Close()
	return s.getSessionConn(ctx, conn, id)
}

// ReadCommandOutput implements commandtools.CommandTranscriptStore.
func (s *Store) ReadCommandOutput(ctx context.Context, req commandtools.ReadRequest) (commandtools.ReadResult, error) {
	if err := contextError(ctx); err != nil {
		return commandtools.ReadResult{}, err
	}
	if s == nil {
		return commandtools.ReadResult{}, fmt.Errorf("sqlite command transcript store is nil")
	}
	if req.ID == "" {
		return commandtools.ReadResult{}, fmt.Errorf("commandtools: command session id is required")
	}
	ctx = contextOrBackground(ctx)
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return commandtools.ReadResult{}, fmt.Errorf("acquire sqlite command transcript connection: %w", err)
	}
	defer conn.Close()

	session, err := s.getSessionConn(ctx, conn, req.ID)
	if err != nil {
		return commandtools.ReadResult{}, err
	}
	if req.SessionID != "" && session.SessionID != "" && session.SessionID != req.SessionID {
		return commandtools.ReadResult{}, commandSessionError(commandtools.ErrCommandSessionNotVisible, "commandtools: command session %s is not visible in this agent session", req.ID)
	}

	maxChunks := req.MaxChunks
	if maxChunks <= 0 {
		maxChunks = defaultReadChunks
	}
	maxBytes := req.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultReadBytes
	}
	rows, err := conn.QueryContext(ctx, `
		SELECT seq, stream, text, time_unix_ms
		FROM memax_command_transcript_chunks
		WHERE command_id = ? AND seq > ?
		ORDER BY seq ASC
	`, req.ID, req.AfterSeq)
	if err != nil {
		return commandtools.ReadResult{}, fmt.Errorf("query sqlite command transcript chunks %s: %w", req.ID, err)
	}
	defer rows.Close()

	chunks := make([]commandtools.OutputChunk, 0, maxChunks)
	bytes := 0
	for rows.Next() {
		var (
			chunk     commandtools.OutputChunk
			timeUnixM unixMillisTime
		)
		if err := rows.Scan(&chunk.Seq, &chunk.Stream, &chunk.Text, &timeUnixM); err != nil {
			return commandtools.ReadResult{}, fmt.Errorf("scan sqlite command transcript chunk %s: %w", req.ID, err)
		}
		chunk.Time = time.Time(timeUnixM)
		if len(chunks) >= maxChunks {
			break
		}
		if bytes > 0 && bytes+len(chunk.Text) > maxBytes {
			break
		}
		chunks = append(chunks, chunk)
		bytes += len(chunk.Text)
	}
	if err := rows.Err(); err != nil {
		return commandtools.ReadResult{}, fmt.Errorf("iterate sqlite command transcript chunks %s: %w", req.ID, err)
	}
	return commandtools.ReadResult{
		Session: session,
		Chunks:  chunks,
		NextSeq: session.NextSeq,
	}, nil
}

// ListCommands implements commandtools.CommandTranscriptStore.
func (s *Store) ListCommands(ctx context.Context, req commandtools.ListRequest) ([]commandtools.CommandSession, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("sqlite command transcript store is nil")
	}
	ctx = contextOrBackground(ctx)

	var (
		query strings.Builder
		args  []any
	)
	query.WriteString(`
		SELECT id, session_id, parent_session_id, identity_json, argv_json,
			cwd, purpose, status, pid, tty, cols, rows, signals_process_tree,
			started_at_unix_ms, finished_at_unix_ms, exit_code, timed_out,
			next_seq, dropped_chunks, dropped_bytes
		FROM memax_command_transcript_sessions
		WHERE 1 = 1
	`)
	if req.SessionID != "" {
		query.WriteString(` AND session_id = ?`)
		args = append(args, req.SessionID)
	}
	if !req.IncludeCompleted {
		query.WriteString(` AND status = ?`)
		args = append(args, string(commandtools.SessionRunning))
	}
	query.WriteString(` ORDER BY started_at_unix_ms ASC, id ASC`)
	if req.Limit > 0 {
		query.WriteString(` LIMIT ?`)
		args = append(args, req.Limit)
	}

	rows, err := s.db.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("list sqlite command transcript sessions: %w", err)
	}
	defer rows.Close()

	out := make([]commandtools.CommandSession, 0)
	for rows.Next() {
		session, err := scanCommandSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sqlite command transcript sessions: %w", err)
	}
	return out, nil
}

// DeleteCommandSession implements commandtools.CommandTranscriptStore.
func (s *Store) DeleteCommandSession(ctx context.Context, id string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if s == nil {
		return fmt.Errorf("sqlite command transcript store is nil")
	}
	if id == "" {
		return fmt.Errorf("commandtools: command session id is required")
	}
	ctx = contextOrBackground(ctx)
	return s.withImmediateTx(ctx, func(conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, `DELETE FROM memax_command_transcript_chunks WHERE command_id = ?`, id); err != nil {
			return fmt.Errorf("delete sqlite command transcript chunks %s: %w", id, err)
		}
		if _, err := conn.ExecContext(ctx, `DELETE FROM memax_command_transcript_sessions WHERE id = ?`, id); err != nil {
			return fmt.Errorf("delete sqlite command transcript session %s: %w", id, err)
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
		CREATE TABLE IF NOT EXISTS memax_command_transcript_sessions (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL DEFAULT '',
			parent_session_id TEXT NOT NULL DEFAULT '',
			identity_json TEXT NOT NULL DEFAULT '{}',
			argv_json TEXT NOT NULL DEFAULT '[]',
			cwd TEXT NOT NULL DEFAULT '',
			purpose TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL,
			pid INTEGER NOT NULL DEFAULT 0,
			tty INTEGER NOT NULL DEFAULT 0,
			cols INTEGER NOT NULL DEFAULT 0,
			rows INTEGER NOT NULL DEFAULT 0,
			signals_process_tree INTEGER NOT NULL DEFAULT 0,
			started_at_unix_ms INTEGER NOT NULL,
			finished_at_unix_ms INTEGER,
			exit_code INTEGER,
			timed_out INTEGER NOT NULL DEFAULT 0,
			next_seq INTEGER NOT NULL DEFAULT 1,
			dropped_chunks INTEGER NOT NULL DEFAULT 0,
			dropped_bytes INTEGER NOT NULL DEFAULT 0
		)
	`); err != nil {
		return fmt.Errorf("init sqlite command transcript session schema: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS memax_command_transcript_sessions_list_idx
		ON memax_command_transcript_sessions(session_id, status, started_at_unix_ms, id)
	`); err != nil {
		return fmt.Errorf("init sqlite command transcript session index: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS memax_command_transcript_chunks (
			command_id TEXT NOT NULL,
			seq INTEGER NOT NULL,
			stream TEXT NOT NULL,
			text TEXT NOT NULL,
			time_unix_ms INTEGER NOT NULL,
			PRIMARY KEY(command_id, seq)
		)
	`); err != nil {
		return fmt.Errorf("init sqlite command transcript chunk schema: %w", err)
	}
	return nil
}

func (s *Store) withImmediateTx(ctx context.Context, fn func(*sql.Conn) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire sqlite command transcript connection: %w", err)
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
		return fmt.Errorf("begin sqlite command transcript transaction: %w", err)
	}
	started = true
	if err := fn(conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit sqlite command transcript transaction: %w", err)
	}
	committed = true
	return nil
}

func (s *Store) getSessionConn(ctx context.Context, conn *sql.Conn, id string) (commandtools.CommandSession, error) {
	if id == "" {
		return commandtools.CommandSession{}, fmt.Errorf("commandtools: command session id is required")
	}
	row := conn.QueryRowContext(ctx, `
		SELECT id, session_id, parent_session_id, identity_json, argv_json,
			cwd, purpose, status, pid, tty, cols, rows, signals_process_tree,
			started_at_unix_ms, finished_at_unix_ms, exit_code, timed_out,
			next_seq, dropped_chunks, dropped_bytes
		FROM memax_command_transcript_sessions
		WHERE id = ?
		LIMIT 1
	`, id)
	session, err := scanCommandSession(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return commandtools.CommandSession{}, commandSessionError(commandtools.ErrCommandSessionUnknown, "commandtools: unknown command session %s", id)
		}
		return commandtools.CommandSession{}, err
	}
	return session, nil
}

func (s *Store) upsertSessionConn(ctx context.Context, conn *sql.Conn, session commandtools.CommandSession) error {
	identityJSON, err := marshalIdentity(session.Identity)
	if err != nil {
		return err
	}
	argvJSON, err := marshalArgv(session.Argv)
	if err != nil {
		return err
	}
	var finishedAt any
	if session.FinishedAt != nil {
		finishedAt = unixMillis(session.FinishedAt.UTC())
	}
	var exitCode any
	if session.ExitCode != nil {
		exitCode = *session.ExitCode
	}
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO memax_command_transcript_sessions (
			id, session_id, parent_session_id, identity_json, argv_json,
			cwd, purpose, status, pid, tty, cols, rows, signals_process_tree,
			started_at_unix_ms, finished_at_unix_ms, exit_code, timed_out,
			next_seq, dropped_chunks, dropped_bytes
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			session_id = excluded.session_id,
			parent_session_id = excluded.parent_session_id,
			identity_json = excluded.identity_json,
			argv_json = excluded.argv_json,
			cwd = excluded.cwd,
			purpose = excluded.purpose,
			status = excluded.status,
			pid = excluded.pid,
			tty = excluded.tty,
			cols = excluded.cols,
			rows = excluded.rows,
			signals_process_tree = excluded.signals_process_tree,
			started_at_unix_ms = excluded.started_at_unix_ms,
			finished_at_unix_ms = excluded.finished_at_unix_ms,
			exit_code = excluded.exit_code,
			timed_out = excluded.timed_out,
			next_seq = excluded.next_seq,
			dropped_chunks = excluded.dropped_chunks,
			dropped_bytes = excluded.dropped_bytes
	`, session.ID, session.SessionID, session.ParentSessionID, identityJSON, argvJSON,
		session.CWD, session.Purpose, string(session.Status), session.PID, boolInt(session.TTY), session.Cols, session.Rows, boolInt(session.SignalsProcessTree),
		unixMillis(session.StartedAt), finishedAt, exitCode, boolInt(session.TimedOut), session.NextSeq, session.DroppedChunks, session.DroppedBytes); err != nil {
		return fmt.Errorf("upsert sqlite command transcript session %s: %w", session.ID, err)
	}
	return nil
}

func (s *Store) sessionNextSeqConn(ctx context.Context, conn *sql.Conn, id string) (int, error) {
	var nextSeq int
	err := conn.QueryRowContext(ctx, `
		SELECT next_seq
		FROM memax_command_transcript_sessions
		WHERE id = ?
		LIMIT 1
	`, id).Scan(&nextSeq)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, commandSessionError(commandtools.ErrCommandSessionUnknown, "commandtools: unknown command session %s", id)
		}
		return 0, fmt.Errorf("read sqlite command transcript next seq %s: %w", id, err)
	}
	return nextSeq, nil
}

func (s *Store) lastChunkSeqConn(ctx context.Context, conn *sql.Conn, id string) (int, error) {
	var lastSeq int
	if err := conn.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(seq), 0)
		FROM memax_command_transcript_chunks
		WHERE command_id = ?
	`, id).Scan(&lastSeq); err != nil {
		return 0, fmt.Errorf("read sqlite command transcript last seq %s: %w", id, err)
	}
	return lastSeq, nil
}

type commandSessionScanner interface {
	Scan(dest ...any) error
}

func scanCommandSession(scanner commandSessionScanner) (commandtools.CommandSession, error) {
	var (
		session            commandtools.CommandSession
		status             string
		identityJSON       string
		argvJSON           string
		tty                int64
		signalsProcessTree int64
		timedOut           int64
		startedAtUnixMS    unixMillisTime
		finishedAtUnixMS   sql.NullInt64
		exitCode           sql.NullInt64
	)
	if err := scanner.Scan(
		&session.ID,
		&session.SessionID,
		&session.ParentSessionID,
		&identityJSON,
		&argvJSON,
		&session.CWD,
		&session.Purpose,
		&status,
		&session.PID,
		&tty,
		&session.Cols,
		&session.Rows,
		&signalsProcessTree,
		&startedAtUnixMS,
		&finishedAtUnixMS,
		&exitCode,
		&timedOut,
		&session.NextSeq,
		&session.DroppedChunks,
		&session.DroppedBytes,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return commandtools.CommandSession{}, sql.ErrNoRows
		}
		return commandtools.CommandSession{}, fmt.Errorf("scan sqlite command transcript session: %w", err)
	}
	session.Status = commandtools.SessionStatus(status)
	session.TTY = tty != 0
	session.SignalsProcessTree = signalsProcessTree != 0
	session.TimedOut = timedOut != 0
	session.StartedAt = time.Time(startedAtUnixMS)
	if finishedAtUnixMS.Valid {
		finished := time.UnixMilli(finishedAtUnixMS.Int64).UTC()
		session.FinishedAt = &finished
	}
	if exitCode.Valid {
		value := int(exitCode.Int64)
		session.ExitCode = &value
	}
	ident, err := unmarshalIdentity(identityJSON)
	if err != nil {
		return commandtools.CommandSession{}, fmt.Errorf("decode sqlite command transcript identity: %w", err)
	}
	session.Identity = ident
	argv, err := unmarshalArgv(argvJSON)
	if err != nil {
		return commandtools.CommandSession{}, fmt.Errorf("decode sqlite command transcript argv: %w", err)
	}
	session.Argv = argv
	return session, nil
}

func marshalIdentity(ident identity.Identity) (string, error) {
	if ident.IsZero() {
		return "{}", nil
	}
	data, err := json.Marshal(ident)
	if err != nil {
		return "", fmt.Errorf("marshal sqlite command transcript identity: %w", err)
	}
	return string(data), nil
}

func unmarshalIdentity(raw string) (identity.Identity, error) {
	if strings.TrimSpace(raw) == "" || raw == "{}" {
		return identity.Identity{}, nil
	}
	var ident identity.Identity
	if err := json.Unmarshal([]byte(raw), &ident); err != nil {
		return identity.Identity{}, err
	}
	if len(ident.Constraints) > 0 {
		ident.Constraints = append([]string(nil), ident.Constraints...)
	}
	return ident, nil
}

func marshalArgv(argv []string) (string, error) {
	if len(argv) == 0 {
		return "[]", nil
	}
	data, err := json.Marshal(argv)
	if err != nil {
		return "", fmt.Errorf("marshal sqlite command transcript argv: %w", err)
	}
	return string(data), nil
}

func unmarshalArgv(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" || raw == "[]" {
		return nil, nil
	}
	var argv []string
	if err := json.Unmarshal([]byte(raw), &argv); err != nil {
		return nil, err
	}
	if len(argv) == 0 {
		return nil, nil
	}
	return append([]string(nil), argv...), nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func unixMillis(t time.Time) int64 {
	return t.UnixMilli()
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

type classifiedCommandSessionError struct {
	kind error
	msg  string
}

func (e classifiedCommandSessionError) Error() string {
	return e.msg
}

func (e classifiedCommandSessionError) Unwrap() error {
	return e.kind
}

func commandSessionError(kind error, format string, args ...any) error {
	return classifiedCommandSessionError{
		kind: kind,
		msg:  fmt.Sprintf(format, args...),
	}
}
