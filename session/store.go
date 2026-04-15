package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

type Session struct {
	ID        string
	ParentID  string
	CreatedAt time.Time
}

type Store interface {
	Create(context.Context) (Session, error)
	Append(context.Context, string, model.Message) error
	Messages(context.Context, string) ([]model.Message, error)
}

type CreateOptions struct {
	ParentID string
}

type StoreWithCreateOptions interface {
	CreateWithOptions(context.Context, CreateOptions) (Session, error)
}

type MemoryStore struct {
	mu       sync.RWMutex
	sessions map[string]memorySession
}

type memorySession struct {
	session  Session
	messages []model.Message
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{sessions: make(map[string]memorySession)}
}

func (s *MemoryStore) Create(ctx context.Context) (Session, error) {
	return s.CreateWithOptions(ctx, CreateOptions{})
}

func (s *MemoryStore) CreateWithOptions(_ context.Context, opts CreateOptions) (Session, error) {
	id, err := newID()
	if err != nil {
		return Session{}, err
	}
	session := Session{ID: id, ParentID: opts.ParentID, CreatedAt: time.Now().UTC()}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[id] = memorySession{session: session}
	return session, nil
}

func (s *MemoryStore) Append(_ context.Context, id string, msg model.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.sessions[id]
	if !ok {
		return fmt.Errorf("unknown session: %s", id)
	}
	record.messages = append(record.messages, msg)
	s.sessions[id] = record
	return nil
}

func (s *MemoryStore) Messages(_ context.Context, id string) ([]model.Message, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.sessions[id]
	if !ok {
		return nil, fmt.Errorf("unknown session: %s", id)
	}
	out := make([]model.Message, len(record.messages))
	copy(out, record.messages)
	return out, nil
}

func Create(ctx context.Context, store Store, opts CreateOptions) (Session, error) {
	if store == nil {
		return Session{}, fmt.Errorf("session store is required")
	}
	if extended, ok := store.(StoreWithCreateOptions); ok {
		return extended.CreateWithOptions(ctx, opts)
	}
	return store.Create(ctx)
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
