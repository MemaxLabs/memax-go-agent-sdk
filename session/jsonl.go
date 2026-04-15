package session

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

const transcriptExt = ".jsonl"

var sessionIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

type JSONLStore struct {
	dir string
}

func NewJSONLStore(dir string) *JSONLStore {
	return &JSONLStore{dir: dir}
}

type transcriptEntry struct {
	Type      string        `json:"type"`
	Timestamp time.Time     `json:"timestamp"`
	Message   model.Message `json:"message"`
}

func (s *JSONLStore) Create(context.Context) (Session, error) {
	if s.dir == "" {
		return Session{}, fmt.Errorf("session jsonl store directory is required")
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
	if err := file.Close(); err != nil {
		return Session{}, fmt.Errorf("close transcript: %w", err)
	}
	return Session{ID: id, CreatedAt: time.Now().UTC()}, nil
}

func (s *JSONLStore) Append(_ context.Context, id string, msg model.Message) error {
	path, err := s.path(id)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return fmt.Errorf("open transcript for append: %w", err)
	}
	defer file.Close()

	entry := transcriptEntry{
		Type:      "message",
		Timestamp: time.Now().UTC(),
		Message:   msg,
	}
	if err := json.NewEncoder(file).Encode(entry); err != nil {
		return fmt.Errorf("append transcript entry: %w", err)
	}
	return nil
}

func (s *JSONLStore) Messages(_ context.Context, id string) ([]model.Message, error) {
	path, err := s.path(id)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open transcript: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 64*1024*1024)

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
			return nil, fmt.Errorf("decode transcript line %d: %w", line, err)
		}
		if entry.Type != "message" {
			continue
		}
		messages = append(messages, entry.Message)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan transcript: %w", err)
	}
	return messages, nil
}

func (s *JSONLStore) path(id string) (string, error) {
	if !sessionIDPattern.MatchString(id) {
		return "", fmt.Errorf("invalid session id: %q", id)
	}
	return filepath.Join(s.dir, id+transcriptExt), nil
}
