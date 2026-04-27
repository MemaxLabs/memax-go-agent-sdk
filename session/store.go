package session

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/google/uuid"
)

var sessionIDPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type Session struct {
	ID        string
	ParentID  string
	CreatedAt time.Time
}

// ValidID reports whether id is a syntactically valid SDK session ID.
func ValidID(id string) bool {
	_, ok := CanonicalID(id)
	return ok
}

// CanonicalID returns id in the SDK's lowercase UUID form.
func CanonicalID(id string) (string, bool) {
	if !sessionIDPattern.MatchString(id) {
		return "", false
	}
	return strings.ToLower(id), true
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

// StoreWithChildren is implemented by stores that can enumerate sessions with
// the given parent ID. An empty parent ID returns root sessions.
type StoreWithChildren interface {
	Children(context.Context, string) ([]Session, error)
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
	session    Session
	messages   []model.Message
	compaction *CompactionCheckpoint
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{sessions: make(map[string]memorySession)}
}

func (s *MemoryStore) Create(ctx context.Context) (Session, error) {
	return s.CreateWithOptions(ctx, CreateOptions{})
}

func (s *MemoryStore) CreateWithOptions(_ context.Context, opts CreateOptions) (Session, error) {
	parentID, err := canonicalParentID(opts.ParentID)
	if err != nil {
		return Session{}, err
	}
	id, err := newID()
	if err != nil {
		return Session{}, err
	}
	session := Session{ID: id, ParentID: parentID, CreatedAt: time.Now().UTC()}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[id] = memorySession{session: session}
	return session, nil
}

func (s *MemoryStore) Append(_ context.Context, id string, msg model.Message) error {
	id, err := canonicalRequiredID(id)
	if err != nil {
		return err
	}
	if msg.ID == "" {
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
	record.messages = append(record.messages, model.CloneMessage(msg))
	s.sessions[id] = record
	return nil
}

func (s *MemoryStore) Messages(_ context.Context, id string) ([]model.Message, error) {
	id, err := canonicalRequiredID(id)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.sessions[id]
	if !ok {
		return nil, fmt.Errorf("unknown session: %s", id)
	}
	return model.CloneMessages(record.messages), nil
}

func (s *MemoryStore) MessageView(_ context.Context, id string) (MessageView, error) {
	id, err := canonicalRequiredID(id)
	if err != nil {
		return MessageView{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.sessions[id]
	if !ok {
		return MessageView{}, fmt.Errorf("unknown session: %s", id)
	}
	return messageView(record.messages, record.compaction)
}

func (s *MemoryStore) SaveCompaction(_ context.Context, id string, checkpoint CompactionCheckpoint) error {
	id, err := canonicalRequiredID(id)
	if err != nil {
		return err
	}
	checkpoint, err = normalizeCompactionCheckpoint(checkpoint)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.sessions[id]
	if !ok {
		return fmt.Errorf("unknown session: %s", id)
	}
	if checkpoint.RawMessageCount > len(record.messages) {
		return fmt.Errorf("compaction raw message count %d exceeds transcript length %d", checkpoint.RawMessageCount, len(record.messages))
	}
	// MemoryStore keeps only the active checkpoint. Durable stores may retain
	// checkpoint history, but the in-memory reference store only needs current
	// model-visible state.
	record.compaction = &checkpoint
	s.sessions[id] = record
	return nil
}

func (s *MemoryStore) Get(_ context.Context, id string) (Session, error) {
	id, err := canonicalRequiredID(id)
	if err != nil {
		return Session{}, err
	}
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

// Children returns sessions whose ParentID matches parentID. An empty parentID
// returns root sessions.
func (s *MemoryStore) Children(_ context.Context, parentID string) ([]Session, error) {
	parentID, err := canonicalParentID(parentID)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Session
	for _, record := range s.sessions {
		if record.session.ParentID == parentID {
			out = append(out, record.session)
		}
	}
	sortSessions(out)
	return out, nil
}

func (s *MemoryStore) Fork(_ context.Context, id string, opts ForkOptions) (Session, error) {
	id, err := canonicalRequiredID(id)
	if err != nil {
		return Session{}, err
	}
	parentID, err := canonicalParentID(opts.ParentID)
	if err != nil {
		return Session{}, err
	}
	if parentID == "" {
		parentID = id
	}
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
	session := Session{ID: newID, ParentID: parentID, CreatedAt: time.Now().UTC()}
	s.sessions[newID] = memorySession{
		session:    session,
		messages:   model.CloneMessages(messages),
		compaction: nil,
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

// Children returns sessions whose ParentID matches parentID. An empty parentID
// returns root sessions. Stores without native child enumeration fall back to
// List when available.
func Children(ctx context.Context, store Store, parentID string) ([]Session, error) {
	if store == nil {
		return nil, fmt.Errorf("session store is required")
	}
	parentID, err := canonicalParentID(parentID)
	if err != nil {
		return nil, err
	}
	if extended, ok := store.(StoreWithChildren); ok {
		return extended.Children(ctx, parentID)
	}
	sessions, err := List(ctx, store)
	if err != nil {
		return nil, err
	}
	children := make([]Session, 0, len(sessions))
	for _, sess := range sessions {
		if sess.ParentID == parentID {
			children = append(children, sess)
		}
	}
	sortSessions(children)
	return children, nil
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
	return model.CloneMessages(messages[:limit]), nil
}

func canonicalRequiredID(id string) (string, error) {
	canonical, ok := CanonicalID(id)
	if !ok {
		return "", fmt.Errorf("invalid session id: %q", id)
	}
	return canonical, nil
}

func canonicalParentID(id string) (string, error) {
	if id == "" {
		return "", nil
	}
	canonical, ok := CanonicalID(id)
	if !ok {
		return "", fmt.Errorf("invalid parent session id: %q", id)
	}
	return canonical, nil
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
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}
	return id.String(), nil
}
