// Package messaging defines host-owned messaging and thread contracts for
// personal-intelligence adapters. Agent-driven deletion is intentionally left
// to host-admin surfaces rather than exposed as a default agent capability.
package messaging

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

// Direction identifies whether a message is inbound or outbound from the
// user's perspective.
type Direction string

const (
	DirectionInbound  Direction = "inbound"
	DirectionOutbound Direction = "outbound"
)

// Participant describes one person or endpoint on a thread.
type Participant struct {
	ID      string
	Name    string
	Address string
	Role    string
}

// Message is one concrete message inside a host-owned thread.
type Message struct {
	ID         string
	ThreadID   string
	Subject    string
	Summary    string
	Body       string
	Direction  Direction
	Sender     Participant
	Recipients []Participant
	SentAt     time.Time
	Metadata   map[string]any
}

// Thread is the metadata-first messaging unit exposed to agents.
type Thread struct {
	ID            string
	Subject       string
	Summary       string
	Snippet       string
	Participants  []Participant
	Tags          []string
	LastMessageAt time.Time
	Messages      []Message
	Metadata      map[string]any
}

// SearchFilter carries portable message-thread predicates that adapters can
// translate to their native search backends.
type SearchFilter struct {
	Mailboxes []string
	From      []string
	Since     time.Time
	Until     time.Time
	Unread    *bool
}

// SearchRequest carries thread-search context and bounds.
type SearchRequest struct {
	SessionID       string
	ParentSessionID string
	Identity        identity.Identity
	Messages        []model.Message
	Query           string
	Filter          SearchFilter
	Limit           int
}

// ReadRequest identifies one thread to load by ID or subject.
type ReadRequest struct {
	SessionID       string
	ParentSessionID string
	Identity        identity.Identity
	ThreadID        string
	Subject         string
}

// SendRequest describes one outbound message send.
type SendRequest struct {
	SessionID       string
	ParentSessionID string
	Identity        identity.Identity
	ThreadID        string
	Subject         string
	Summary         string
	Body            string
	Recipients      []Participant
	Metadata        map[string]any
}

// Searcher searches thread metadata without requiring full-content loading.
type Searcher interface {
	SearchThreads(context.Context, SearchRequest) ([]Thread, error)
}

// Reader loads one full thread.
type Reader interface {
	ReadThread(context.Context, ReadRequest) (Thread, error)
}

// Sender is an optional outbound messaging capability.
type Sender interface {
	SendMessage(context.Context, SendRequest) (SendResult, error)
}

// SendResult is the outcome of one outbound send.
type SendResult struct {
	Thread        Thread
	Message       Message
	CreatedThread bool
}

// SearcherFunc adapts a function to Searcher.
type SearcherFunc func(context.Context, SearchRequest) ([]Thread, error)

// SearchThreads calls f(ctx, req).
func (f SearcherFunc) SearchThreads(ctx context.Context, req SearchRequest) ([]Thread, error) {
	if f == nil {
		return nil, fmt.Errorf("messaging: nil SearcherFunc")
	}
	return f(ctx, req)
}

// ReaderFunc adapts a function to Reader.
type ReaderFunc func(context.Context, ReadRequest) (Thread, error)

// ReadThread calls f(ctx, req).
func (f ReaderFunc) ReadThread(ctx context.Context, req ReadRequest) (Thread, error) {
	if f == nil {
		return Thread{}, fmt.Errorf("messaging: nil ReaderFunc")
	}
	return f(ctx, req)
}

// SenderFunc adapts a function to Sender.
type SenderFunc func(context.Context, SendRequest) (SendResult, error)

// SendMessage calls f(ctx, req).
func (f SenderFunc) SendMessage(ctx context.Context, req SendRequest) (SendResult, error) {
	if f == nil {
		return SendResult{}, fmt.Errorf("messaging: nil SenderFunc")
	}
	return f(ctx, req)
}

// ThreadStore is a concurrency-safe in-memory Searcher, Reader, and Sender for
// tests, examples, and short-lived agents.
type ThreadStore struct {
	mu      sync.RWMutex
	threads map[string]Thread
	order   []string
	next    int
}

// NewThreadStore returns an in-memory thread store seeded with threads.
func NewThreadStore(threads []Thread) *ThreadStore {
	store := &ThreadStore{
		threads: make(map[string]Thread),
		next:    1,
	}
	for _, item := range threads {
		_, _ = store.insertThread(item)
	}
	return store
}

// SearchThreads returns a deterministic relevant subset of thread metadata.
func (s *ThreadStore) SearchThreads(ctx context.Context, req SearchRequest) ([]Thread, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("messaging: nil ThreadStore")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]Thread, 0, len(s.order))
	for _, id := range s.order {
		if item, ok := s.threads[id]; ok {
			cloned := cloneThread(item)
			if req.Filter.matchesThread(cloned) {
				items = append(items, cloned)
			}
		}
	}
	return (Selector{MaxThreads: req.Limit}).Select(items, req.Query), nil
}

// ReadThread loads one full thread by ID or subject.
func (s *ThreadStore) ReadThread(ctx context.Context, req ReadRequest) (Thread, error) {
	if err := contextError(ctx); err != nil {
		return Thread{}, err
	}
	if s == nil {
		return Thread{}, fmt.Errorf("messaging: nil ThreadStore")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if id := strings.TrimSpace(req.ThreadID); id != "" {
		item, ok := s.threads[id]
		if !ok {
			return Thread{}, fmt.Errorf("thread not found: %s", id)
		}
		return cloneThread(item), nil
	}
	subject := strings.TrimSpace(req.Subject)
	if subject == "" {
		return Thread{}, fmt.Errorf("messaging: read requires thread_id or subject")
	}
	for _, id := range s.order {
		item, ok := s.threads[id]
		if ok && item.Subject == subject {
			return cloneThread(item), nil
		}
	}
	return Thread{}, fmt.Errorf("thread not found: %s", subject)
}

// SendMessage appends an outbound message to an existing thread or creates a
// new thread when ThreadID is empty.
func (s *ThreadStore) SendMessage(ctx context.Context, req SendRequest) (SendResult, error) {
	if err := contextError(ctx); err != nil {
		return SendResult{}, err
	}
	if s == nil {
		return SendResult{}, fmt.Errorf("messaging: nil ThreadStore")
	}
	if strings.TrimSpace(req.Body) == "" {
		return SendResult{}, fmt.Errorf("messaging: body is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLocked()

	threadID := strings.TrimSpace(req.ThreadID)
	subject := strings.TrimSpace(req.Subject)
	var thread Thread
	createdThread := false
	if threadID != "" {
		item, ok := s.threads[threadID]
		if !ok {
			return SendResult{}, fmt.Errorf("thread not found: %s", threadID)
		}
		thread = cloneThread(item)
	} else {
		if subject == "" {
			return SendResult{}, fmt.Errorf("messaging: subject is required when creating a thread")
		}
		threadID = s.nextIDLocked()
		now := time.Now().UTC()
		thread = Thread{
			ID:            threadID,
			Subject:       subject,
			Summary:       strings.TrimSpace(req.Summary),
			Participants:  cloneParticipants(req.Recipients),
			LastMessageAt: now,
			Metadata:      model.CloneMetadata(req.Metadata),
		}
		createdThread = true
		s.order = append(s.order, threadID)
	}

	now := time.Now().UTC()
	message := Message{
		ID:         s.nextMessageIDLocked(thread),
		ThreadID:   threadID,
		Subject:    firstNonEmpty(subject, thread.Subject),
		Summary:    strings.TrimSpace(req.Summary),
		Body:       strings.TrimSpace(req.Body),
		Direction:  DirectionOutbound,
		Recipients: cloneParticipants(req.Recipients),
		SentAt:     now,
		Metadata:   model.CloneMetadata(req.Metadata),
	}
	if message.Subject == "" {
		return SendResult{}, fmt.Errorf("messaging: subject is required when creating a thread")
	}
	if thread.Subject == "" {
		thread.Subject = message.Subject
	}
	if thread.Summary == "" && message.Summary != "" {
		thread.Summary = message.Summary
	}
	thread.Messages = append(thread.Messages, message)
	thread.LastMessageAt = now
	if thread.Snippet == "" {
		thread.Snippet = messageSnippet(message)
	}
	thread.Participants = mergeParticipants(thread.Participants, message.Recipients)
	s.threads[threadID] = cloneThread(thread)
	s.bumpNextLocked(threadID)
	return SendResult{
		Thread:        cloneThread(thread),
		Message:       cloneMessage(message),
		CreatedThread: createdThread,
	}, nil
}

// Selector deterministically selects relevant threads. It is exported so hosts
// can reuse the default metadata-first ranking logic in their own Searcher
// implementations.
type Selector struct {
	MaxThreads int
}

// Select returns a stable relevant subset of threads for query.
func (s Selector) Select(threads []Thread, query string) []Thread {
	if len(threads) == 0 {
		return nil
	}
	query = strings.ToLower(strings.TrimSpace(query))
	tokens := tokenize(query)
	items := make([]scoredThread, 0, len(threads))
	for i, item := range threads {
		score := scoreThread(item, query, tokens)
		if score > 0 || query == "" {
			items = append(items, scoredThread{Thread: cloneThread(item), index: i, score: score})
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		left, right := items[i], items[j]
		if left.score != right.score {
			return left.score > right.score
		}
		if !left.LastMessageAt.Equal(right.LastMessageAt) {
			return left.LastMessageAt.After(right.LastMessageAt)
		}
		if left.Subject != right.Subject {
			return left.Subject < right.Subject
		}
		return left.index < right.index
	})
	if s.MaxThreads > 0 && len(items) > s.MaxThreads {
		items = items[:s.MaxThreads]
	}
	out := make([]Thread, len(items))
	for i, item := range items {
		out[i] = item.Thread
	}
	return out
}

type scoredThread struct {
	Thread
	index int
	score int
}

func (s *ThreadStore) insertThread(item Thread) (Thread, error) {
	s.ensureLocked()
	item = normalizeThread(item)
	if item.ID == "" {
		item.ID = s.nextIDLocked()
	}
	if item.LastMessageAt.IsZero() && len(item.Messages) > 0 {
		item.LastMessageAt = item.Messages[len(item.Messages)-1].SentAt
	}
	if _, ok := s.threads[item.ID]; !ok {
		s.order = append(s.order, item.ID)
	}
	s.threads[item.ID] = cloneThread(item)
	s.bumpNextLocked(item.ID)
	return cloneThread(item), nil
}

func (s *ThreadStore) ensureLocked() {
	if s.threads == nil {
		s.threads = make(map[string]Thread)
	}
	if s.next <= 0 {
		s.next = 1
	}
}

func scoreThread(item Thread, query string, tokens []string) int {
	if query == "" {
		return 1
	}
	score := 0
	subject := strings.ToLower(item.Subject)
	summary := strings.ToLower(item.Summary)
	snippet := strings.ToLower(item.Snippet)
	if strings.Contains(subject, query) {
		score += 8
	}
	if strings.Contains(summary, query) {
		score += 5
	}
	if strings.Contains(snippet, query) {
		score += 3
	}
	for _, participant := range item.Participants {
		name := strings.ToLower(participant.Name)
		address := strings.ToLower(participant.Address)
		if strings.Contains(name, query) || strings.Contains(address, query) {
			score += 4
		}
	}
	for _, tag := range item.Tags {
		if strings.Contains(strings.ToLower(tag), query) {
			score += 3
		}
	}
	for _, token := range tokens {
		if token == "" {
			continue
		}
		switch {
		case strings.Contains(subject, token):
			score += 4
		case strings.Contains(summary, token):
			score += 3
		case strings.Contains(snippet, token):
			score += 2
		}
		for _, participant := range item.Participants {
			if strings.Contains(strings.ToLower(participant.Name), token) || strings.Contains(strings.ToLower(participant.Address), token) {
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

func normalizeThread(item Thread) Thread {
	item.ID = strings.TrimSpace(item.ID)
	item.Subject = strings.TrimSpace(item.Subject)
	item.Summary = strings.TrimSpace(item.Summary)
	item.Snippet = strings.TrimSpace(item.Snippet)
	item.Participants = cloneParticipants(item.Participants)
	item.Tags = trimStrings(item.Tags)
	item.Metadata = model.CloneMetadata(item.Metadata)
	item.Messages = cloneMessages(item.Messages)
	if item.LastMessageAt.IsZero() && len(item.Messages) > 0 {
		item.LastMessageAt = item.Messages[len(item.Messages)-1].SentAt
	}
	return item
}

func trimStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func messageSnippet(message Message) string {
	if message.Summary != "" {
		return truncateSnippet(message.Summary)
	}
	return ""
}

func truncateSnippet(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	const max = 160
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return strings.TrimSpace(string(runes[:max])) + "..."
}

func cloneThread(item Thread) Thread {
	item.Participants = cloneParticipants(item.Participants)
	item.Tags = append([]string(nil), item.Tags...)
	item.Messages = cloneMessages(item.Messages)
	item.Metadata = model.CloneMetadata(item.Metadata)
	return item
}

func cloneMessages(messages []Message) []Message {
	if len(messages) == 0 {
		return nil
	}
	out := make([]Message, len(messages))
	for i, item := range messages {
		out[i] = cloneMessage(item)
	}
	return out
}

func cloneMessage(item Message) Message {
	item.Subject = strings.TrimSpace(item.Subject)
	item.Summary = strings.TrimSpace(item.Summary)
	item.Body = strings.TrimSpace(item.Body)
	item.Sender = cloneParticipant(item.Sender)
	item.Recipients = cloneParticipants(item.Recipients)
	item.Metadata = model.CloneMetadata(item.Metadata)
	return item
}

func cloneParticipants(items []Participant) []Participant {
	if len(items) == 0 {
		return nil
	}
	out := make([]Participant, len(items))
	for i, item := range items {
		out[i] = cloneParticipant(item)
	}
	return out
}

func cloneParticipant(item Participant) Participant {
	item.ID = strings.TrimSpace(item.ID)
	item.Name = strings.TrimSpace(item.Name)
	item.Address = strings.TrimSpace(item.Address)
	item.Role = strings.TrimSpace(item.Role)
	return item
}

func mergeParticipants(existing, extra []Participant) []Participant {
	if len(existing) == 0 && len(extra) == 0 {
		return nil
	}
	out := cloneParticipants(existing)
	seen := map[string]struct{}{}
	for _, item := range out {
		seen[participantKey(item)] = struct{}{}
	}
	for _, item := range extra {
		key := participantKey(item)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, cloneParticipant(item))
	}
	return out
}

func participantKey(item Participant) string {
	if item.Address != "" {
		return strings.ToLower(item.Address)
	}
	if item.Name != "" {
		return strings.ToLower(item.Name)
	}
	return strings.ToLower(item.ID)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func (s *ThreadStore) nextIDLocked() string {
	for {
		id := fmt.Sprintf("thread-%d", s.next)
		s.next++
		if _, ok := s.threads[id]; !ok {
			return id
		}
	}
}

func (s *ThreadStore) nextMessageIDLocked(thread Thread) string {
	return fmt.Sprintf("%s-msg-%d", thread.ID, len(thread.Messages)+1)
}

func (s *ThreadStore) bumpNextLocked(id string) {
	var n int
	if _, err := fmt.Sscanf(id, "thread-%d", &n); err == nil && n >= s.next {
		s.next = n + 1
	}
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func (f SearchFilter) matchesThread(thread Thread) bool {
	if !f.matchesMailboxes(thread) {
		return false
	}
	if !f.matchesSenders(thread) {
		return false
	}
	if !f.matchesWindow(thread.LastMessageAt) {
		return false
	}
	if !f.matchesUnread(thread) {
		return false
	}
	return true
}

func (f SearchFilter) matchesMailboxes(thread Thread) bool {
	if len(f.Mailboxes) == 0 {
		return true
	}
	values := mailboxValues(thread)
	if len(values) == 0 {
		return false
	}
	for _, wanted := range f.Mailboxes {
		wanted = strings.TrimSpace(wanted)
		if wanted == "" {
			continue
		}
		for _, value := range values {
			if strings.EqualFold(value, wanted) {
				return true
			}
		}
	}
	return false
}

func (f SearchFilter) matchesSenders(thread Thread) bool {
	if len(f.From) == 0 {
		return true
	}
	senders := senderValues(thread)
	if len(senders) == 0 {
		return false
	}
	for _, wanted := range f.From {
		wanted = strings.TrimSpace(wanted)
		if wanted == "" {
			continue
		}
		for _, sender := range senders {
			if strings.EqualFold(sender, wanted) {
				return true
			}
		}
	}
	return false
}

func (f SearchFilter) matchesWindow(last time.Time) bool {
	if f.Since.IsZero() && f.Until.IsZero() {
		return true
	}
	if last.IsZero() {
		return false
	}
	last = last.UTC()
	if !f.Since.IsZero() && last.Before(f.Since.UTC()) {
		return false
	}
	if !f.Until.IsZero() && last.After(f.Until.UTC()) {
		return false
	}
	return true
}

func (f SearchFilter) matchesUnread(thread Thread) bool {
	if f.Unread == nil {
		return true
	}
	value, ok := metadataBool(thread.Metadata, "unread")
	if !ok {
		return false
	}
	return value == *f.Unread
}

func mailboxValues(thread Thread) []string {
	seen := make(map[string]struct{}, len(thread.Tags))
	out := make([]string, 0, len(thread.Tags))
	for _, tag := range thread.Tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		key := strings.ToLower(tag)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, tag)
	}
	for _, value := range metadataStrings(thread.Metadata, "mailboxes") {
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}

func senderValues(thread Thread) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, message := range thread.Messages {
		appendSenderValue(&out, seen, message.Sender.Name)
		appendSenderValue(&out, seen, message.Sender.Address)
	}
	for _, participant := range thread.Participants {
		if participant.Role != "" && !strings.EqualFold(participant.Role, "from") {
			continue
		}
		appendSenderValue(&out, seen, participant.Name)
		appendSenderValue(&out, seen, participant.Address)
	}
	return out
}

func appendSenderValue(dst *[]string, seen map[string]struct{}, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	key := strings.ToLower(value)
	if _, ok := seen[key]; ok {
		return
	}
	seen[key] = struct{}{}
	*dst = append(*dst, value)
}

func metadataStrings(metadata map[string]any, key string) []string {
	if len(metadata) == 0 {
		return nil
	}
	value, ok := metadata[key]
	if !ok {
		return nil
	}
	switch typed := value.(type) {
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			item = strings.TrimSpace(item)
			if item != "" {
				out = append(out, item)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				continue
			}
			text = strings.TrimSpace(text)
			if text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func metadataBool(metadata map[string]any, key string) (bool, bool) {
	if len(metadata) == 0 {
		return false, false
	}
	value, ok := metadata[key]
	if !ok {
		return false, false
	}
	switch typed := value.(type) {
	case bool:
		return typed, true
	default:
		return false, false
	}
}
