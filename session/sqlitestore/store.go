package sqlitestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/session"
	"github.com/google/uuid"
)

type Store struct {
	db *sql.DB
}

func New(ctx context.Context, db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("sqlite session store db is required")
	}
	store := &Store{db: db}
	if err := store.init(ctx); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) Create(ctx context.Context) (session.Session, error) {
	return s.CreateWithOptions(ctx, session.CreateOptions{})
}

func (s *Store) CreateWithOptions(ctx context.Context, opts session.CreateOptions) (session.Session, error) {
	parentID, err := canonicalParentID(opts.ParentID)
	if err != nil {
		return session.Session{}, err
	}
	id, err := newID()
	if err != nil {
		return session.Session{}, err
	}
	sess := session.Session{
		ID:        id,
		ParentID:  parentID,
		CreatedAt: time.Now().UTC(),
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO memax_sessions (id, parent_id, created_at)
		VALUES (?, ?, ?)
	`, sess.ID, sess.ParentID, sess.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return session.Session{}, fmt.Errorf("create sqlite session: %w", err)
	}
	return sess, nil
}

func (s *Store) Append(ctx context.Context, id string, msg model.Message) error {
	id, err := canonicalRequiredID(id)
	if err != nil {
		return err
	}
	if _, err := s.Get(ctx, id); err != nil {
		return err
	}
	if msg.ID == "" {
		msg.ID, err = newID()
		if err != nil {
			return err
		}
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("encode sqlite session message: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO memax_messages (session_id, message_id, message_json, created_at)
		VALUES (?, ?, ?, ?)
	`, id, msg.ID, string(data), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("append sqlite session message: %w", err)
	}
	return nil
}

func (s *Store) Messages(ctx context.Context, id string) ([]model.Message, error) {
	id, err := canonicalRequiredID(id)
	if err != nil {
		return nil, err
	}
	if _, err := s.Get(ctx, id); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT message_json
		FROM memax_messages
		WHERE session_id = ?
		ORDER BY seq ASC
	`, id)
	if err != nil {
		return nil, fmt.Errorf("query sqlite session messages: %w", err)
	}
	defer rows.Close()

	var messages []model.Message
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, fmt.Errorf("scan sqlite session message: %w", err)
		}
		var msg model.Message
		if err := json.Unmarshal([]byte(data), &msg); err != nil {
			return nil, fmt.Errorf("decode sqlite session message: %w", err)
		}
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sqlite session messages: %w", err)
	}
	return messages, nil
}

func (s *Store) MessageView(ctx context.Context, id string) (session.MessageView, error) {
	messages, err := s.Messages(ctx, id)
	if err != nil {
		return session.MessageView{}, err
	}
	checkpoint, err := s.latestCompaction(ctx, id)
	if err != nil {
		return session.MessageView{}, err
	}
	return sessionMessageView(messages, checkpoint)
}

func (s *Store) SaveCompaction(ctx context.Context, id string, checkpoint session.CompactionCheckpoint) error {
	id, err := canonicalRequiredID(id)
	if err != nil {
		return err
	}
	messageCount, err := s.messageCount(ctx, id)
	if err != nil {
		return err
	}
	if checkpoint.RawMessageCount > messageCount {
		return fmt.Errorf("compaction raw message count %d exceeds transcript length %d", checkpoint.RawMessageCount, messageCount)
	}
	checkpoint, err = normalizeSessionCompaction(checkpoint)
	if err != nil {
		return err
	}
	data, err := json.Marshal(checkpoint)
	if err != nil {
		return fmt.Errorf("encode sqlite session compaction: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO memax_compactions (session_id, checkpoint_id, checkpoint_json, created_at)
		VALUES (?, ?, ?, ?)
	`, id, checkpoint.ID, string(data), checkpoint.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("append sqlite session compaction: %w", err)
	}
	return nil
}

func (s *Store) messageCount(ctx context.Context, id string) (int, error) {
	id, err := canonicalRequiredID(id)
	if err != nil {
		return 0, err
	}
	if _, err := s.Get(ctx, id); err != nil {
		return 0, err
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memax_messages WHERE session_id = ?`, id).Scan(&count); err != nil {
		return 0, fmt.Errorf("count sqlite session messages: %w", err)
	}
	return count, nil
}

func (s *Store) Get(ctx context.Context, id string) (session.Session, error) {
	id, err := canonicalRequiredID(id)
	if err != nil {
		return session.Session{}, err
	}
	var sess session.Session
	var createdAt string
	err = s.db.QueryRowContext(ctx, `
		SELECT id, parent_id, created_at
		FROM memax_sessions
		WHERE id = ?
	`, id).Scan(&sess.ID, &sess.ParentID, &createdAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return session.Session{}, fmt.Errorf("unknown session: %s", id)
		}
		return session.Session{}, fmt.Errorf("get sqlite session: %w", err)
	}
	if createdAt != "" {
		parsed, err := time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return session.Session{}, fmt.Errorf("parse sqlite session created_at: %w", err)
		}
		sess.CreatedAt = parsed
	}
	return sess, nil
}

func (s *Store) List(ctx context.Context) ([]session.Session, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, parent_id, created_at
		FROM memax_sessions
		ORDER BY created_at ASC, id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list sqlite sessions: %w", err)
	}
	defer rows.Close()

	var sessions []session.Session
	for rows.Next() {
		var sess session.Session
		var createdAt string
		if err := rows.Scan(&sess.ID, &sess.ParentID, &createdAt); err != nil {
			return nil, fmt.Errorf("scan sqlite session: %w", err)
		}
		if createdAt != "" {
			parsed, err := time.Parse(time.RFC3339Nano, createdAt)
			if err != nil {
				return nil, fmt.Errorf("parse sqlite session created_at: %w", err)
			}
			sess.CreatedAt = parsed
		}
		sessions = append(sessions, sess)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sqlite sessions: %w", err)
	}
	return sessions, nil
}

// Children returns sessions whose ParentID matches parentID. An empty parentID
// returns root sessions.
func (s *Store) Children(ctx context.Context, parentID string) ([]session.Session, error) {
	parentID, err := canonicalParentID(parentID)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, parent_id, created_at
		FROM memax_sessions
		WHERE parent_id = ?
		ORDER BY created_at ASC, id ASC
	`, parentID)
	if err != nil {
		return nil, fmt.Errorf("list sqlite child sessions: %w", err)
	}
	defer rows.Close()

	var sessions []session.Session
	for rows.Next() {
		var sess session.Session
		var createdAt string
		if err := rows.Scan(&sess.ID, &sess.ParentID, &createdAt); err != nil {
			return nil, fmt.Errorf("scan sqlite child session: %w", err)
		}
		if createdAt != "" {
			parsed, err := time.Parse(time.RFC3339Nano, createdAt)
			if err != nil {
				return nil, fmt.Errorf("parse sqlite child session created_at: %w", err)
			}
			sess.CreatedAt = parsed
		}
		sessions = append(sessions, sess)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sqlite child sessions: %w", err)
	}
	return sessions, nil
}

func (s *Store) Fork(ctx context.Context, id string, opts session.ForkOptions) (session.Session, error) {
	id, err := canonicalRequiredID(id)
	if err != nil {
		return session.Session{}, err
	}
	messages, err := s.Messages(ctx, id)
	if err != nil {
		return session.Session{}, err
	}
	messages, err = forkMessages(messages, opts.ThroughMessageID)
	if err != nil {
		return session.Session{}, err
	}
	parentID := opts.ParentID
	if parentID == "" {
		parentID = id
	}
	forked, err := s.CreateWithOptions(ctx, session.CreateOptions{ParentID: parentID})
	if err != nil {
		return session.Session{}, err
	}
	for _, msg := range messages {
		if err := s.Append(ctx, forked.ID, msg); err != nil {
			return session.Session{}, err
		}
	}
	return forked, nil
}

func (s *Store) init(ctx context.Context) error {
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS memax_sessions (
			id TEXT PRIMARY KEY,
			parent_id TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS memax_messages (
			seq INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			message_id TEXT NOT NULL,
			message_json TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS memax_messages_session_seq_idx
			ON memax_messages (session_id, seq)`,
		`CREATE TABLE IF NOT EXISTS memax_compactions (
			seq INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			checkpoint_id TEXT NOT NULL,
			checkpoint_json TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS memax_compactions_session_seq_idx
			ON memax_compactions (session_id, seq)`,
	} {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize sqlite session store: %w", err)
		}
	}
	return nil
}

func forkMessages(messages []model.Message, throughMessageID string) ([]model.Message, error) {
	limit := len(messages)
	if throughMessageID != "" {
		limit = -1
		for i, msg := range messages {
			if msg.ID == throughMessageID {
				limit = i + 1
				break
			}
		}
		if limit < 0 {
			return nil, fmt.Errorf("message not found: %s", throughMessageID)
		}
	}
	return model.CloneMessages(messages[:limit]), nil
}

func newID() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}
	return id.String(), nil
}

func canonicalRequiredID(id string) (string, error) {
	canonical, ok := session.CanonicalID(id)
	if !ok {
		return "", fmt.Errorf("invalid session id: %q", id)
	}
	return canonical, nil
}

func canonicalParentID(id string) (string, error) {
	if id == "" {
		return "", nil
	}
	canonical, ok := session.CanonicalID(id)
	if !ok {
		return "", fmt.Errorf("invalid parent session id: %q", id)
	}
	return canonical, nil
}

func (s *Store) latestCompaction(ctx context.Context, id string) (*session.CompactionCheckpoint, error) {
	id, err := canonicalRequiredID(id)
	if err != nil {
		return nil, err
	}
	var data string
	err = s.db.QueryRowContext(ctx, `
		SELECT checkpoint_json
		FROM memax_compactions
		WHERE session_id = ?
		ORDER BY seq DESC
		LIMIT 1
	`, id).Scan(&data)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get sqlite session compaction: %w", err)
	}
	var checkpoint session.CompactionCheckpoint
	if err := json.Unmarshal([]byte(data), &checkpoint); err != nil {
		return nil, fmt.Errorf("decode sqlite session compaction: %w", err)
	}
	return &checkpoint, nil
}

func sessionMessageView(raw []model.Message, checkpoint *session.CompactionCheckpoint) (session.MessageView, error) {
	if checkpoint == nil {
		return session.MessageView{
			Messages:        model.CloneMessages(raw),
			RawMessageCount: len(raw),
		}, nil
	}
	if checkpoint.RawMessageCount > len(raw) {
		return session.MessageView{}, fmt.Errorf("compaction raw message count %d exceeds transcript length %d", checkpoint.RawMessageCount, len(raw))
	}
	active := model.CloneMessages(checkpoint.Messages)
	active = append(active, model.CloneMessages(raw[checkpoint.RawMessageCount:])...)
	copied := *checkpoint
	copied.Messages = model.CloneMessages(checkpoint.Messages)
	return session.MessageView{
		Messages:        active,
		RawMessageCount: len(raw),
		Compaction:      &copied,
	}, nil
}

func normalizeSessionCompaction(checkpoint session.CompactionCheckpoint) (session.CompactionCheckpoint, error) {
	if checkpoint.RawMessageCount < 0 {
		return session.CompactionCheckpoint{}, fmt.Errorf("compaction raw message count must be non-negative")
	}
	if checkpoint.ID == "" {
		id, err := newID()
		if err != nil {
			return session.CompactionCheckpoint{}, err
		}
		checkpoint.ID = id
	} else {
		canonical, ok := session.CanonicalID(checkpoint.ID)
		if !ok {
			return session.CompactionCheckpoint{}, fmt.Errorf("invalid compaction id: %q", checkpoint.ID)
		}
		checkpoint.ID = canonical
	}
	if checkpoint.CreatedAt.IsZero() {
		checkpoint.CreatedAt = time.Now().UTC()
	} else {
		checkpoint.CreatedAt = checkpoint.CreatedAt.UTC()
	}
	checkpoint.Messages = model.CloneMessages(checkpoint.Messages)
	return checkpoint, nil
}
