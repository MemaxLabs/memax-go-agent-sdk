package session

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

const (
	transcriptExt = ".jsonl"
	indexDir      = ".index"
	indexExt      = ".json"
)

type JSONLStore struct {
	dir string
}

func NewJSONLStore(dir string) *JSONLStore {
	return &JSONLStore{dir: dir}
}

type transcriptEntry struct {
	Type       string                `json:"type"`
	Timestamp  time.Time             `json:"timestamp"`
	Session    *Session              `json:"session,omitempty"`
	Message    *model.Message        `json:"message,omitempty"`
	Compaction *CompactionCheckpoint `json:"compaction,omitempty"`
}

type transcriptIndexEntry struct {
	ID        string    `json:"id"`
	ParentID  string    `json:"parent_id,omitempty"`
	Path      string    `json:"path"`
	CreatedAt time.Time `json:"created_at"`
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
	if err := os.MkdirAll(filepath.Join(s.dir, indexDir), 0o755); err != nil {
		return Session{}, fmt.Errorf("create session index directory: %w", err)
	}

	id, err := newID()
	if err != nil {
		return Session{}, err
	}
	relPath, err := s.transcriptRelPath(id, parentID)
	if err != nil {
		return Session{}, err
	}
	path := filepath.Join(s.dir, relPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Session{}, fmt.Errorf("create transcript directory: %w", err)
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
	if err := s.writeIndex(transcriptIndexEntry{
		ID:        session.ID,
		ParentID:  session.ParentID,
		Path:      relPath,
		CreatedAt: session.CreatedAt,
	}); err != nil {
		_ = os.Remove(path)
		return Session{}, err
	}
	return session, nil
}

func (s *JSONLStore) Append(_ context.Context, id string, msg model.Message) error {
	path, err := s.path(id)
	if err != nil {
		return err
	}
	msg = NormalizeTranscriptMessage(msg)
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

// NormalizeTranscriptMessage returns a copy of msg with provider-facing raw
// JSON fields normalized for durable transcript storage.
func NormalizeTranscriptMessage(msg model.Message) model.Message {
	msg = model.CloneMessage(msg)
	for i := range msg.Content {
		if msg.Content[i].ToolUse != nil {
			use := model.NormalizeToolUse(*msg.Content[i].ToolUse)
			msg.Content[i].ToolUse = &use
		}
	}
	return msg
}

func (s *JSONLStore) Messages(ctx context.Context, id string) ([]model.Message, error) {
	_, messages, _, err := s.readTranscript(ctx, id)
	return messages, err
}

func (s *JSONLStore) Get(ctx context.Context, id string) (Session, error) {
	session, _, _, err := s.readTranscript(ctx, id)
	return session, err
}

func (s *JSONLStore) MessageView(ctx context.Context, id string) (MessageView, error) {
	_, messages, compaction, err := s.readTranscript(ctx, id)
	if err != nil {
		return MessageView{}, err
	}
	return messageView(messages, compaction)
}

func (s *JSONLStore) SaveCompaction(ctx context.Context, id string, checkpoint CompactionCheckpoint) error {
	canonicalID, err := canonicalRequiredID(id)
	if err != nil {
		return err
	}
	path, err := s.path(canonicalID)
	if err != nil {
		return err
	}
	_, messages, _, err := s.readTranscript(ctx, canonicalID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("unknown session: %s", canonicalID)
		}
		return err
	}
	if checkpoint.RawMessageCount > len(messages) {
		return fmt.Errorf("compaction raw message count %d exceeds transcript length %d", checkpoint.RawMessageCount, len(messages))
	}
	checkpoint, err = normalizeCompactionCheckpoint(checkpoint)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return fmt.Errorf("open transcript for compaction append: %w", err)
	}
	defer file.Close()

	entry := transcriptEntry{
		Type:       "compaction",
		Timestamp:  checkpoint.CreatedAt,
		Compaction: &checkpoint,
	}
	if err := json.NewEncoder(file).Encode(entry); err != nil {
		return fmt.Errorf("append compaction transcript entry: %w", err)
	}
	return nil
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
	seen := make(map[string]struct{})
	var sessions []Session
	indexEntries, err := os.ReadDir(filepath.Join(s.dir, indexDir))
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("list session index directory: %w", err)
	}
	for _, entry := range indexEntries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != indexExt {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), indexExt)
		if !ValidID(id) {
			continue
		}
		indexEntry, err := s.readIndex(id)
		if err != nil {
			return nil, err
		}
		session, transcriptPath, err := s.sessionFromIndex(indexEntry, id)
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(transcriptPath); err != nil {
			return nil, fmt.Errorf("stat indexed transcript: %w", err)
		}
		sessions = append(sessions, session)
		seen[session.ID] = struct{}{}
	}

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			sortSessions(sessions)
			return sessions, nil
		}
		return nil, fmt.Errorf("list session directory: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != transcriptExt {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), transcriptExt)
		if !ValidID(id) {
			continue
		}
		canonicalID, _ := CanonicalID(id)
		if _, ok := seen[canonicalID]; ok {
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

// Children returns sessions whose ParentID matches parentID. An empty parentID
// returns root sessions.
func (s *JSONLStore) Children(ctx context.Context, parentID string) ([]Session, error) {
	parentID, err := canonicalParentID(parentID)
	if err != nil {
		return nil, err
	}
	sessions, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	children := make([]Session, 0, len(sessions))
	for _, session := range sessions {
		if session.ParentID == parentID {
			children = append(children, session)
		}
	}
	sortSessions(children)
	return children, nil
}

func (s *JSONLStore) Fork(ctx context.Context, id string, opts ForkOptions) (Session, error) {
	_, messages, _, err := s.readTranscript(ctx, id)
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

func (s *JSONLStore) readTranscript(_ context.Context, id string) (Session, []model.Message, *CompactionCheckpoint, error) {
	canonicalID, err := canonicalRequiredID(id)
	if err != nil {
		return Session{}, nil, nil, err
	}
	path, err := s.path(id)
	if err != nil {
		return Session{}, nil, nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return Session{}, nil, nil, fmt.Errorf("open transcript: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 64*1024*1024)

	session := Session{ID: canonicalID}
	var messages []model.Message
	var compaction *CompactionCheckpoint
	line := 0
	for scanner.Scan() {
		line++
		data := scanner.Bytes()
		if len(data) == 0 {
			continue
		}
		var entry transcriptEntry
		if err := json.Unmarshal(data, &entry); err != nil {
			return Session{}, nil, nil, fmt.Errorf("decode transcript line %d: %w", line, err)
		}
		if entry.Type == "session" && entry.Session != nil {
			session = *entry.Session
			if session.ID == "" {
				session.ID = canonicalID
			}
			continue
		}
		if entry.Type == "compaction" && entry.Compaction != nil {
			copied := *entry.Compaction
			copied.Messages = model.CloneMessages(entry.Compaction.Messages)
			compaction = &copied
			continue
		}
		if entry.Type != "message" || entry.Message == nil {
			continue
		}
		messages = append(messages, *entry.Message)
	}
	if err := scanner.Err(); err != nil {
		return Session{}, nil, nil, fmt.Errorf("scan transcript: %w", err)
	}
	return session, messages, compaction, nil
}

func (s *JSONLStore) path(id string) (string, error) {
	canonicalID, ok := CanonicalID(id)
	if !ok {
		return "", fmt.Errorf("invalid session id: %q", id)
	}
	entry, err := s.readIndex(canonicalID)
	if err == nil {
		relPath, err := safeIndexPath(entry, canonicalID)
		if err != nil {
			return "", err
		}
		return filepath.Join(s.dir, relPath), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return filepath.Join(s.dir, canonicalID+transcriptExt), nil
}

func (s *JSONLStore) sessionFromIndex(entry transcriptIndexEntry, id string) (Session, string, error) {
	canonicalID, ok := CanonicalID(id)
	if !ok {
		return Session{}, "", fmt.Errorf("invalid session id: %q", id)
	}
	relPath, err := safeIndexPath(entry, canonicalID)
	if err != nil {
		return Session{}, "", err
	}
	parentID, err := canonicalParentID(entry.ParentID)
	if err != nil {
		return Session{}, "", err
	}
	return Session{
		ID:        canonicalID,
		ParentID:  parentID,
		CreatedAt: entry.CreatedAt,
	}, filepath.Join(s.dir, relPath), nil
}

func (s *JSONLStore) transcriptRelPath(id, parentID string) (string, error) {
	if parentID == "" {
		return id + transcriptExt, nil
	}
	entry, err := s.readIndex(parentID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return filepath.Join(parentID, id+transcriptExt), nil
		}
		return "", err
	}
	parentPath, err := safeIndexPath(entry, parentID)
	if err != nil {
		return "", err
	}
	return filepath.Join(strings.TrimSuffix(parentPath, transcriptExt), id+transcriptExt), nil
}

func (s *JSONLStore) indexPath(id string) (string, error) {
	canonicalID, ok := CanonicalID(id)
	if !ok {
		return "", fmt.Errorf("invalid session id: %q", id)
	}
	return filepath.Join(s.dir, indexDir, canonicalID+indexExt), nil
}

func (s *JSONLStore) readIndex(id string) (transcriptIndexEntry, error) {
	path, err := s.indexPath(id)
	if err != nil {
		return transcriptIndexEntry{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return transcriptIndexEntry{}, fmt.Errorf("read session index: %w", err)
	}
	var entry transcriptIndexEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return transcriptIndexEntry{}, fmt.Errorf("decode session index %s: %w", id, err)
	}
	return entry, nil
}

func (s *JSONLStore) writeIndex(entry transcriptIndexEntry) error {
	path, err := s.indexPath(entry.ID)
	if err != nil {
		return err
	}
	relPath, err := safeIndexPath(entry, entry.ID)
	if err != nil {
		return err
	}
	entry.Path = relPath
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode session index: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create session index temp file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write session index temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close session index temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("publish session index: %w", err)
	}
	return nil
}

func safeIndexPath(entry transcriptIndexEntry, id string) (string, error) {
	canonicalID, ok := CanonicalID(entry.ID)
	if !ok || canonicalID != id {
		return "", fmt.Errorf("session index id = %q, want %q", entry.ID, id)
	}
	if entry.Path == "" {
		return "", fmt.Errorf("session index path is empty for %s", id)
	}
	clean := filepath.Clean(entry.Path)
	if filepath.IsAbs(clean) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("session index path is unsafe for %s: %q", id, entry.Path)
	}
	if filepath.Ext(clean) != transcriptExt || filepath.Base(clean) != id+transcriptExt {
		return "", fmt.Errorf("session index path does not match session %s: %q", id, entry.Path)
	}
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("session index path is unsafe for %s: %q", id, entry.Path)
		}
	}
	return clean, nil
}
