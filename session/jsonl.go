package session

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

const transcriptExt = ".jsonl"

type JSONLStore struct {
	dir string
}

func NewJSONLStore(dir string) *JSONLStore {
	return &JSONLStore{dir: dir}
}

type transcriptEntry struct {
	Type      string         `json:"type"`
	Timestamp time.Time      `json:"timestamp"`
	Session   *Session       `json:"session,omitempty"`
	Message   *model.Message `json:"message,omitempty"`
}

func (s *JSONLStore) Create(ctx context.Context) (Session, error) {
	return s.CreateWithOptions(ctx, CreateOptions{})
}

func (s *JSONLStore) CreateWithOptions(_ context.Context, opts CreateOptions) (Session, error) {
	if s.dir == "" {
		return Session{}, fmt.Errorf("session jsonl store directory is required")
	}
	parentID, err := canonicalParentID(opts.ParentID)
	if err != nil {
		return Session{}, err
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return Session{}, fmt.Errorf("create session directory: %w", err)
	}

	id, err := newID()
	if err != nil {
		return Session{}, err
	}
	path, err := s.path(id)
	if err != nil {
		return Session{}, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return Session{}, fmt.Errorf("create transcript: %w", err)
	}
	session := Session{ID: id, ParentID: parentID, CreatedAt: time.Now().UTC()}
	entry := transcriptEntry{
		Type:      "session",
		Timestamp: session.CreatedAt,
		Session:   &session,
	}
	if err := json.NewEncoder(file).Encode(entry); err != nil {
		_ = file.Close()
		return Session{}, fmt.Errorf("write session transcript entry: %w", err)
	}
	if err := file.Close(); err != nil {
		return Session{}, fmt.Errorf("close transcript: %w", err)
	}
	return session, nil
}

func (s *JSONLStore) Append(_ context.Context, id string, msg model.Message) error {
	path, err := s.path(id)
	if err != nil {
		return err
	}
	if msg.ID == "" {
		msg.ID, err = newID()
		if err != nil {
			return err
		}
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return fmt.Errorf("open transcript for append: %w", err)
	}
	defer file.Close()

	entry := transcriptEntry{
		Type:      "message",
		Timestamp: time.Now().UTC(),
		Message:   &msg,
	}
	if err := json.NewEncoder(file).Encode(entry); err != nil {
		return fmt.Errorf("append transcript entry: %w", err)
	}
	return nil
}

func (s *JSONLStore) Messages(ctx context.Context, id string) ([]model.Message, error) {
	_, messages, err := s.readTranscript(ctx, id)
	return messages, err
}

func (s *JSONLStore) Get(ctx context.Context, id string) (Session, error) {
	session, _, err := s.readTranscript(ctx, id)
	return session, err
}

// Exists reports whether a transcript exists for id without reading the full
// transcript. Invalid IDs return an error so callers can distinguish malformed
// input from a missing session.
func (s *JSONLStore) Exists(_ context.Context, id string) (bool, error) {
	path, err := s.path(id)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat transcript: %w", err)
	}
	return true, nil
}

func (s *JSONLStore) List(ctx context.Context) ([]Session, error) {
	if s.dir == "" {
		return nil, fmt.Errorf("session jsonl store directory is required")
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list session directory: %w", err)
	}
	var sessions []Session
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != transcriptExt {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), transcriptExt)
		if !ValidID(id) {
			continue
		}
		session, err := s.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	sortSessions(sessions)
	return sessions, nil
}

func (s *JSONLStore) Fork(ctx context.Context, id string, opts ForkOptions) (Session, error) {
	_, messages, err := s.readTranscript(ctx, id)
	if err != nil {
		return Session{}, err
	}
	messages, err = forkMessages(messages, opts.ThroughMessageID)
	if err != nil {
		return Session{}, err
	}
	parentID := opts.ParentID
	if parentID == "" {
		parentID = id
	}
	session, err := s.CreateWithOptions(ctx, CreateOptions{ParentID: parentID})
	if err != nil {
		return Session{}, err
	}
	for _, msg := range messages {
		if err := s.Append(ctx, session.ID, msg); err != nil {
			return Session{}, err
		}
	}
	return session, nil
}

func (s *JSONLStore) readTranscript(_ context.Context, id string) (Session, []model.Message, error) {
	canonicalID, err := canonicalRequiredID(id)
	if err != nil {
		return Session{}, nil, err
	}
	path, err := s.path(id)
	if err != nil {
		return Session{}, nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return Session{}, nil, fmt.Errorf("open transcript: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 64*1024*1024)

	session := Session{ID: canonicalID}
	var messages []model.Message
	line := 0
	for scanner.Scan() {
		line++
		data := scanner.Bytes()
		if len(data) == 0 {
			continue
		}
		var entry transcriptEntry
		if err := json.Unmarshal(data, &entry); err != nil {
			return Session{}, nil, fmt.Errorf("decode transcript line %d: %w", line, err)
		}
		if entry.Type == "session" && entry.Session != nil {
			session = *entry.Session
			if session.ID == "" {
				session.ID = canonicalID
			}
			continue
		}
		if entry.Type != "message" || entry.Message == nil {
			continue
		}
		messages = append(messages, *entry.Message)
	}
	if err := scanner.Err(); err != nil {
		return Session{}, nil, fmt.Errorf("scan transcript: %w", err)
	}
	return session, messages, nil
}

func (s *JSONLStore) path(id string) (string, error) {
	canonicalID, ok := CanonicalID(id)
	if !ok {
		return "", fmt.Errorf("invalid session id: %q", id)
	}
	return filepath.Join(s.dir, canonicalID+transcriptExt), nil
}
