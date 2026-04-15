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
	CreatedAt time.Time
}

type Store interface {
	Create(context.Context) (Session, error)
	Append(context.Context, string, model.Message) error
	Messages(context.Context, string) ([]model.Message, error)
}

type MemoryStore struct {
	mu       sync.RWMutex
	sessions map[string][]model.Message
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{sessions: make(map[string][]model.Message)}
}

func (s *MemoryStore) Create(context.Context) (Session, error) {
	id, err := newID()
	if err != nil {
		return Session{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[id] = nil
	return Session{ID: id, CreatedAt: time.Now().UTC()}, nil
}

func (s *MemoryStore) Append(_ context.Context, id string, msg model.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[id]; !ok {
		return fmt.Errorf("unknown session: %s", id)
	}
	s.sessions[id] = append(s.sessions[id], msg)
	return nil
}

func (s *MemoryStore) Messages(_ context.Context, id string) ([]model.Message, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	msgs, ok := s.sessions[id]
	if !ok {
		return nil, fmt.Errorf("unknown session: %s", id)
	}
	out := make([]model.Message, len(msgs))
	copy(out, msgs)
	return out, nil
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
