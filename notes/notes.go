// Package notes defines host-owned note and lightweight document contracts for
// personal-intelligence adapters.
package notes

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/MemaxLabs/memax-go-agent-sdk/identity"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

// Note is a host-owned note or lightweight document that personal agents can
// search, read, and optionally mutate through explicit tools.
type Note struct {
	ID        string
	Title     string
	Kind      string
	Summary   string
	Content   string
	CreatedAt time.Time
	UpdatedAt time.Time
	Tags      []string
	Metadata  map[string]any
}

// SearchRequest carries note-search context and bounds.
type SearchRequest struct {
	SessionID       string
	ParentSessionID string
	Identity        identity.Identity
	Messages        []model.Message
	Query           string
	Limit           int
}

// ReadRequest identifies one note to load by ID or title.
type ReadRequest struct {
	SessionID       string
	ParentSessionID string
	Identity        identity.Identity
	ID              string
	Title           string
}

// PutRequest describes one durable note write.
type PutRequest struct {
	SessionID       string
	ParentSessionID string
	Identity        identity.Identity
	Note            Note
}

// DeleteRequest describes one note delete by ID or title.
type DeleteRequest struct {
	SessionID       string
	ParentSessionID string
	Identity        identity.Identity
	ID              string
	Title           string
}

// Searcher searches note metadata without requiring full content injection.
type Searcher interface {
	SearchNotes(context.Context, SearchRequest) ([]Note, error)
}

// Reader loads one note's full content.
type Reader interface {
	ReadNote(context.Context, ReadRequest) (Note, error)
}

// Writer is an optional note mutation capability.
type Writer interface {
	PutNote(context.Context, PutRequest) (PutResult, error)
}

// PutResult is the outcome of a note write.
type PutResult struct {
	Note    Note
	Created bool
	Updated bool
}

// Deleter is an optional note deletion capability.
type Deleter interface {
	DeleteNote(context.Context, DeleteRequest) error
}

// SearcherFunc adapts a function to Searcher.
type SearcherFunc func(context.Context, SearchRequest) ([]Note, error)

// SearchNotes calls f(ctx, req).
func (f SearcherFunc) SearchNotes(ctx context.Context, req SearchRequest) ([]Note, error) {
	if f == nil {
		return nil, fmt.Errorf("notes: nil SearcherFunc")
	}
	return f(ctx, req)
}

// ReaderFunc adapts a function to Reader.
type ReaderFunc func(context.Context, ReadRequest) (Note, error)

// ReadNote calls f(ctx, req).
func (f ReaderFunc) ReadNote(ctx context.Context, req ReadRequest) (Note, error) {
	if f == nil {
		return Note{}, fmt.Errorf("notes: nil ReaderFunc")
	}
	return f(ctx, req)
}

// NoteStore is a concurrency-safe in-memory Searcher, Reader, Writer, and
// Deleter for tests, examples, and short-lived agents.
type NoteStore struct {
	mu    sync.RWMutex
	notes map[string]Note
	order []string
	next  int
}

// NewNoteStore returns an in-memory note store seeded with notes.
func NewNoteStore(notes []Note) *NoteStore {
	store := &NoteStore{
		notes: make(map[string]Note),
		next:  1,
	}
	for _, item := range notes {
		_, _ = store.insert(item)
	}
	return store
}

// SearchNotes returns a deterministic relevant subset of notes using local
// metadata-first scoring. Full content is only returned by ReadNote.
func (s *NoteStore) SearchNotes(ctx context.Context, req SearchRequest) ([]Note, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("notes: nil NoteStore")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]Note, 0, len(s.order))
	for _, id := range s.order {
		if item, ok := s.notes[id]; ok {
			items = append(items, clone(item))
		}
	}
	return (Selector{MaxNotes: req.Limit}).Select(items, req.Query), nil
}

// ReadNote loads one note by ID or title.
func (s *NoteStore) ReadNote(ctx context.Context, req ReadRequest) (Note, error) {
	if err := contextError(ctx); err != nil {
		return Note{}, err
	}
	if s == nil {
		return Note{}, fmt.Errorf("notes: nil NoteStore")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if id := strings.TrimSpace(req.ID); id != "" {
		item, ok := s.notes[id]
		if !ok {
			return Note{}, fmt.Errorf("note not found: %s", id)
		}
		return clone(item), nil
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		return Note{}, fmt.Errorf("notes: read requires id or title")
	}
	for _, id := range s.order {
		item, ok := s.notes[id]
		if ok && item.Title == title {
			return clone(item), nil
		}
	}
	return Note{}, fmt.Errorf("note not found: %s", title)
}

// PutNote creates or replaces a note by ID when provided, otherwise by title
// when title is present.
func (s *NoteStore) PutNote(ctx context.Context, req PutRequest) (PutResult, error) {
	if err := contextError(ctx); err != nil {
		return PutResult{}, err
	}
	if s == nil {
		return PutResult{}, fmt.Errorf("notes: nil NoteStore")
	}
	if strings.TrimSpace(req.Note.Content) == "" {
		return PutResult{}, fmt.Errorf("notes: content is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.insert(req.Note)
}

// DeleteNote deletes a note by ID or title.
func (s *NoteStore) DeleteNote(ctx context.Context, req DeleteRequest) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if s == nil {
		return fmt.Errorf("notes: nil NoteStore")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := strings.TrimSpace(req.ID)
	if id == "" && req.Title != "" {
		for _, existingID := range s.order {
			item, ok := s.notes[existingID]
			if ok && item.Title == strings.TrimSpace(req.Title) {
				id = existingID
				break
			}
		}
	}
	if id == "" {
		return fmt.Errorf("notes: delete requires id or title")
	}
	if _, ok := s.notes[id]; !ok {
		return fmt.Errorf("note not found: %s", id)
	}
	delete(s.notes, id)
	for i, existingID := range s.order {
		if existingID == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	return nil
}

// Selector deterministically selects relevant notes. It is exported so hosts
// can reuse the default metadata-first ranking logic inside their own Searcher
// implementations.
type Selector struct {
	MaxNotes int
}

// Select returns a stable relevant subset of notes for query.
func (s Selector) Select(notes []Note, query string) []Note {
	if len(notes) == 0 {
		return nil
	}
	query = strings.ToLower(strings.TrimSpace(query))
	tokens := tokenize(query)
	items := make([]scoredNote, 0, len(notes))
	for i, item := range notes {
		score := scoreNote(item, query, tokens)
		if score > 0 || query == "" {
			items = append(items, scoredNote{Note: clone(item), index: i, score: score})
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		left, right := items[i], items[j]
		if left.score != right.score {
			return left.score > right.score
		}
		if !left.UpdatedAt.Equal(right.UpdatedAt) {
			return left.UpdatedAt.After(right.UpdatedAt)
		}
		if !left.CreatedAt.Equal(right.CreatedAt) {
			return left.CreatedAt.After(right.CreatedAt)
		}
		if left.Title != right.Title {
			return left.Title < right.Title
		}
		return left.index < right.index
	})
	if s.MaxNotes > 0 && len(items) > s.MaxNotes {
		items = items[:s.MaxNotes]
	}
	out := make([]Note, len(items))
	for i, item := range items {
		out[i] = item.Note
	}
	return out
}

type scoredNote struct {
	Note
	index int
	score int
}

func (s *NoteStore) insert(item Note) (PutResult, error) {
	item = clone(item)
	now := time.Now().UTC()
	if item.ID == "" && item.Title != "" {
		for _, existingID := range s.order {
			existing, ok := s.notes[existingID]
			if ok && existing.Title == item.Title {
				item.ID = existingID
				break
			}
		}
	}
	if item.ID == "" {
		item.ID = s.nextIDLocked()
	}
	if existing, ok := s.notes[item.ID]; ok {
		if item.CreatedAt.IsZero() {
			item.CreatedAt = existing.CreatedAt
		}
		if item.CreatedAt.IsZero() {
			item.CreatedAt = now
		}
		if item.UpdatedAt.IsZero() {
			item.UpdatedAt = now
		}
		s.notes[item.ID] = item
		s.bumpNextLocked(item.ID)
		return PutResult{Note: clone(item), Updated: true}, nil
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = now
	}
	if item.UpdatedAt.IsZero() {
		item.UpdatedAt = item.CreatedAt
	}
	s.notes[item.ID] = item
	s.order = append(s.order, item.ID)
	s.bumpNextLocked(item.ID)
	return PutResult{Note: clone(item), Created: true}, nil
}

func scoreNote(item Note, query string, tokens []string) int {
	if query == "" {
		return 1
	}
	score := 0
	title := strings.ToLower(item.Title)
	kind := strings.ToLower(item.Kind)
	summary := strings.ToLower(item.Summary)
	content := strings.ToLower(item.Content)
	if strings.Contains(title, query) {
		score += 8
	}
	if strings.Contains(summary, query) {
		score += 5
	}
	if strings.Contains(kind, query) {
		score += 3
	}
	if strings.Contains(content, query) {
		score += 2
	}
	for _, tag := range item.Tags {
		if strings.Contains(strings.ToLower(tag), query) {
			score += 4
		}
	}
	for _, token := range tokens {
		if token == "" {
			continue
		}
		switch {
		case strings.Contains(title, token):
			score += 4
		case strings.Contains(summary, token):
			score += 3
		case strings.Contains(kind, token):
			score += 2
		case strings.Contains(content, token):
			score++
		}
		for _, tag := range item.Tags {
			if strings.Contains(strings.ToLower(tag), token) {
				score += 2
				break
			}
		}
	}
	return score
}

func tokenize(query string) []string {
	fields := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := fields[:0]
	for _, field := range fields {
		if field != "" {
			out = append(out, field)
		}
	}
	return out
}

func clone(item Note) Note {
	item.Tags = append([]string(nil), item.Tags...)
	item.Metadata = model.CloneMetadata(item.Metadata)
	item.Title = strings.TrimSpace(item.Title)
	item.Kind = strings.TrimSpace(item.Kind)
	item.Summary = strings.TrimSpace(item.Summary)
	item.Content = strings.TrimSpace(item.Content)
	return item
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func (s *NoteStore) nextIDLocked() string {
	for {
		id := fmt.Sprintf("note-%d", s.next)
		s.next++
		if _, ok := s.notes[id]; !ok {
			return id
		}
	}
}

func (s *NoteStore) bumpNextLocked(id string) {
	var n int
	if _, err := fmt.Sscanf(id, "note-%d", &n); err == nil && n >= s.next {
		s.next = n + 1
	}
}
