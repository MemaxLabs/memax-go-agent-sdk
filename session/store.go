package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
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

type StoreWithGet interface {
	Get(context.Context, string) (Session, error)
}

type StoreWithList interface {
	List(context.Context) ([]Session, error)
}

type ForkOptions struct {
	ParentID         string
	ThroughMessageID string
}

type StoreWithFork interface {
	Fork(context.Context, string, ForkOptions) (Session, error)
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
	if msg.ID == "" {
		var err error
		msg.ID, err = newID()
		if err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.sessions[id]
	if !ok {
		return fmt.Errorf("unknown session: %s", id)
	}
	record.messages = append(record.messages, cloneMessage(msg))
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
	return cloneMessages(record.messages), nil
}

func (s *MemoryStore) Get(_ context.Context, id string) (Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.sessions[id]
	if !ok {
		return Session{}, fmt.Errorf("unknown session: %s", id)
	}
	return record.session, nil
}

func (s *MemoryStore) List(context.Context) ([]Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Session, 0, len(s.sessions))
	for _, record := range s.sessions {
		out = append(out, record.session)
	}
	sortSessions(out)
	return out, nil
}

func (s *MemoryStore) Fork(_ context.Context, id string, opts ForkOptions) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	source, ok := s.sessions[id]
	if !ok {
		return Session{}, fmt.Errorf("unknown session: %s", id)
	}
	messages, err := forkMessages(source.messages, opts.ThroughMessageID)
	if err != nil {
		return Session{}, err
	}
	newID, err := newID()
	if err != nil {
		return Session{}, err
	}
	parentID := opts.ParentID
	if parentID == "" {
		parentID = id
	}
	session := Session{ID: newID, ParentID: parentID, CreatedAt: time.Now().UTC()}
	s.sessions[newID] = memorySession{
		session:  session,
		messages: cloneMessages(messages),
	}
	return session, nil
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

func Get(ctx context.Context, store Store, id string) (Session, error) {
	if store == nil {
		return Session{}, fmt.Errorf("session store is required")
	}
	if extended, ok := store.(StoreWithGet); ok {
		return extended.Get(ctx, id)
	}
	if _, err := store.Messages(ctx, id); err != nil {
		return Session{}, err
	}
	return Session{ID: id}, nil
}

func List(ctx context.Context, store Store) ([]Session, error) {
	if store == nil {
		return nil, fmt.Errorf("session store is required")
	}
	if extended, ok := store.(StoreWithList); ok {
		return extended.List(ctx)
	}
	return nil, fmt.Errorf("session store does not support listing")
}

func Fork(ctx context.Context, store Store, id string, opts ForkOptions) (Session, error) {
	if store == nil {
		return Session{}, fmt.Errorf("session store is required")
	}
	if extended, ok := store.(StoreWithFork); ok {
		return extended.Fork(ctx, id, opts)
	}
	return Session{}, fmt.Errorf("session store does not support forking")
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
	return cloneMessages(messages[:limit]), nil
}

func cloneMessages(messages []model.Message) []model.Message {
	if len(messages) == 0 {
		return nil
	}
	out := make([]model.Message, len(messages))
	for i, msg := range messages {
		out[i] = cloneMessage(msg)
	}
	return out
}

func cloneMessage(msg model.Message) model.Message {
	if len(msg.Content) > 0 {
		msg.Content = cloneBlocks(msg.Content)
	}
	if len(msg.Metadata) > 0 {
		msg.Metadata = cloneMetadata(msg.Metadata)
	}
	if msg.ToolResult != nil {
		result := *msg.ToolResult
		result.Metadata = cloneMetadata(result.Metadata)
		msg.ToolResult = &result
	}
	return msg
}

func cloneBlocks(blocks []model.ContentBlock) []model.ContentBlock {
	out := make([]model.ContentBlock, len(blocks))
	for i, block := range blocks {
		out[i] = block
		if block.ToolUse != nil {
			use := *block.ToolUse
			use.Input = append([]byte(nil), block.ToolUse.Input...)
			out[i].ToolUse = &use
		}
	}
	return out
}

func cloneMetadata(metadata map[string]any) map[string]any {
	if metadata == nil {
		return nil
	}
	out := make(map[string]any, len(metadata))
	for key, value := range metadata {
		out[key] = value
	}
	return out
}

func sortSessions(sessions []Session) {
	sort.SliceStable(sessions, func(i int, j int) bool {
		left := sessions[i]
		right := sessions[j]
		if !left.CreatedAt.Equal(right.CreatedAt) {
			return left.CreatedAt.Before(right.CreatedAt)
		}
		return left.ID < right.ID
	})
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
