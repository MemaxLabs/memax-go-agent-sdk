package commandtools

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// CommandTranscriptStore persists managed command-session snapshots and output
// chunks. It is intentionally separate from session.Store: agent sessions own
// the model conversation, while command transcript stores are tool-owned state
// for hosts that want command output to survive manager restarts or be
// inspected outside the live process manager.
type CommandTranscriptStore interface {
	// SaveCommandSession creates or replaces a command-session snapshot while
	// preserving any output chunks already stored for the same command ID.
	SaveCommandSession(context.Context, CommandSession) error
	// AppendCommandOutput appends ordered output chunks for a known command
	// session. Implementations must preserve chunk order and stable Seq values.
	AppendCommandOutput(context.Context, string, []OutputChunk) error
	// CommandSession returns a stored command-session snapshot by command ID.
	CommandSession(context.Context, string) (CommandSession, error)
	// DeleteCommandSession removes a stored command-session transcript.
	DeleteCommandSession(context.Context, string) error
	// ReadCommandOutput reads persisted output using the same visibility rules
	// as live command managers: scoped reads are denied across different
	// non-empty agent session IDs, while unscoped reads remain host-visible.
	Reader
	// ListCommands applies the stricter live-manager listing rule: when scoped
	// to an agent session, records without a matching non-empty SessionID are
	// hidden from the list.
	Lister
}

type commandTranscriptRecord struct {
	session CommandSession
	chunks  []OutputChunk
}

// MemoryCommandTranscriptStore is a concurrency-safe in-memory implementation
// of CommandTranscriptStore. It is useful for tests, examples, and as the
// reference semantics for durable store adapters.
type MemoryCommandTranscriptStore struct {
	mu      sync.RWMutex
	records map[string]commandTranscriptRecord
}

var _ CommandTranscriptStore = (*MemoryCommandTranscriptStore)(nil)

// NewMemoryCommandTranscriptStore returns an empty in-memory command
// transcript store.
func NewMemoryCommandTranscriptStore() *MemoryCommandTranscriptStore {
	return &MemoryCommandTranscriptStore{records: map[string]commandTranscriptRecord{}}
}

// SaveCommandSession stores a command-session snapshot.
func (s *MemoryCommandTranscriptStore) SaveCommandSession(ctx context.Context, session CommandSession) error {
	if s == nil {
		return fmt.Errorf("commandtools: nil MemoryCommandTranscriptStore")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if session.ID == "" {
		return fmt.Errorf("commandtools: command session id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensure()
	record := s.records[session.ID]
	record.session = cloneSession(session)
	if len(record.chunks) > 0 {
		lastSeq := record.chunks[len(record.chunks)-1].Seq
		if record.session.NextSeq <= lastSeq {
			record.session.NextSeq = lastSeq + 1
		}
	}
	s.records[session.ID] = record
	return nil
}

// AppendCommandOutput appends output chunks to a stored command session.
func (s *MemoryCommandTranscriptStore) AppendCommandOutput(ctx context.Context, id string, chunks []OutputChunk) error {
	if s == nil {
		return fmt.Errorf("commandtools: nil MemoryCommandTranscriptStore")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("commandtools: command session id is required")
	}
	if len(chunks) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensure()
	record, ok := s.records[id]
	if !ok {
		return commandSessionError(ErrCommandSessionUnknown, "commandtools: unknown command session %s", id)
	}
	lastSeq := 0
	if len(record.chunks) > 0 {
		lastSeq = record.chunks[len(record.chunks)-1].Seq
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
	record.chunks = append(record.chunks, cloneOutputChunks(chunks)...)
	if record.session.NextSeq <= lastSeq {
		record.session.NextSeq = lastSeq + 1
	}
	s.records[id] = record
	return nil
}

// CommandSession returns a stored command-session snapshot by command ID.
func (s *MemoryCommandTranscriptStore) CommandSession(ctx context.Context, id string) (CommandSession, error) {
	if s == nil {
		return CommandSession{}, fmt.Errorf("commandtools: nil MemoryCommandTranscriptStore")
	}
	if err := ctx.Err(); err != nil {
		return CommandSession{}, err
	}
	if id == "" {
		return CommandSession{}, fmt.Errorf("commandtools: command session id is required")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.records[id]
	if !ok {
		return CommandSession{}, commandSessionError(ErrCommandSessionUnknown, "commandtools: unknown command session %s", id)
	}
	return cloneSession(record.session), nil
}

// ReadCommandOutput reads stored command output using the same paging semantics
// as live managed command sessions.
func (s *MemoryCommandTranscriptStore) ReadCommandOutput(ctx context.Context, req ReadRequest) (ReadResult, error) {
	if s == nil {
		return ReadResult{}, fmt.Errorf("commandtools: nil MemoryCommandTranscriptStore")
	}
	if err := ctx.Err(); err != nil {
		return ReadResult{}, err
	}
	if req.ID == "" {
		return ReadResult{}, fmt.Errorf("commandtools: command session id is required")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.records[req.ID]
	if !ok {
		return ReadResult{}, commandSessionError(ErrCommandSessionUnknown, "commandtools: unknown command session %s", req.ID)
	}
	if req.SessionID != "" && record.session.SessionID != "" && record.session.SessionID != req.SessionID {
		return ReadResult{}, commandSessionError(ErrCommandSessionNotVisible, "commandtools: command session %s is not visible in this agent session", req.ID)
	}
	return paginateOutputChunks(record.session, record.chunks, req.AfterSeq, req.MaxChunks, req.MaxBytes), nil
}

// ListCommands lists stored command sessions visible to the requested agent
// session boundary.
func (s *MemoryCommandTranscriptStore) ListCommands(ctx context.Context, req ListRequest) ([]CommandSession, error) {
	if s == nil {
		return nil, fmt.Errorf("commandtools: nil MemoryCommandTranscriptStore")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	out := make([]CommandSession, 0, len(s.records))
	for _, record := range s.records {
		session := record.session
		if req.SessionID != "" && session.SessionID != req.SessionID {
			continue
		}
		if !req.IncludeCompleted && session.Status != SessionRunning {
			continue
		}
		out = append(out, cloneSession(session))
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].StartedAt.Before(out[j].StartedAt)
	})
	if req.Limit > 0 && len(out) > req.Limit {
		out = out[:req.Limit]
	}
	return out, nil
}

// DeleteCommandSession removes a stored command-session transcript.
func (s *MemoryCommandTranscriptStore) DeleteCommandSession(ctx context.Context, id string) error {
	if s == nil {
		return fmt.Errorf("commandtools: nil MemoryCommandTranscriptStore")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("commandtools: command session id is required")
	}
	s.mu.Lock()
	delete(s.records, id)
	s.mu.Unlock()
	return nil
}

func (s *MemoryCommandTranscriptStore) ensure() {
	if s.records == nil {
		s.records = map[string]commandTranscriptRecord{}
	}
}

func paginateOutputChunks(session CommandSession, chunks []OutputChunk, afterSeq, maxChunks, maxBytes int) ReadResult {
	if maxChunks <= 0 {
		maxChunks = defaultReadChunks
	}
	if maxBytes <= 0 {
		maxBytes = defaultReadBytes
	}
	session = cloneSession(session)
	var out []OutputChunk
	bytes := 0
	for _, chunk := range chunks {
		if chunk.Seq <= afterSeq {
			continue
		}
		if len(out) >= maxChunks {
			break
		}
		if bytes > 0 && bytes+len(chunk.Text) > maxBytes {
			break
		}
		out = append(out, cloneOutputChunk(chunk))
		bytes += len(chunk.Text)
	}
	return ReadResult{
		Session: session,
		Chunks:  out,
		NextSeq: session.NextSeq,
	}
}
