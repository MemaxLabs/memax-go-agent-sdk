// Package scheduling defines host-owned calendar and scheduling contracts for
// personal-intelligence adapters.
package scheduling

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

// Status describes the lifecycle state of one scheduled event.
type Status string

const (
	StatusScheduled Status = "scheduled"
	StatusCancelled Status = "cancelled"
)

// Participant describes one organizer or attendee on an event.
type Participant struct {
	ID      string
	Name    string
	Address string
	Role    string
}

// Event is one host-owned scheduling object exposed to agents.
type Event struct {
	ID          string
	Title       string
	Summary     string
	Description string
	Location    string
	Organizer   Participant
	Attendees   []Participant
	Start       time.Time
	End         time.Time
	TimeZone    string
	Status      Status
	Tags        []string
	Metadata    map[string]any
}

// SearchRequest carries event-search context and bounds.
type SearchRequest struct {
	SessionID       string
	ParentSessionID string
	Identity        identity.Identity
	Messages        []model.Message
	Query           string
	WindowStart     time.Time
	WindowEnd       time.Time
	Limit           int
}

// ReadRequest identifies one event to load by ID or title.
type ReadRequest struct {
	SessionID       string
	ParentSessionID string
	Identity        identity.Identity
	ID              string
	Title           string
}

// CreateRequest describes one new scheduled event.
type CreateRequest struct {
	SessionID       string
	ParentSessionID string
	Identity        identity.Identity
	Event           Event
}

// RescheduleRequest describes one event reschedule.
type RescheduleRequest struct {
	SessionID       string
	ParentSessionID string
	Identity        identity.Identity
	ID              string
	Title           string
	Start           time.Time
	End             time.Time
	TimeZone        string
	Metadata        map[string]any
}

// CancelRequest describes one event cancellation.
type CancelRequest struct {
	SessionID       string
	ParentSessionID string
	Identity        identity.Identity
	ID              string
	Title           string
	Reason          string
	Metadata        map[string]any
}

// Searcher searches event metadata without requiring full description loading.
type Searcher interface {
	SearchEvents(context.Context, SearchRequest) ([]Event, error)
}

// Reader loads one event's full content.
type Reader interface {
	ReadEvent(context.Context, ReadRequest) (Event, error)
}

// Creator is an optional event-creation capability.
type Creator interface {
	CreateEvent(context.Context, CreateRequest) (CreateResult, error)
}

// Rescheduler is an optional event-reschedule capability.
type Rescheduler interface {
	RescheduleEvent(context.Context, RescheduleRequest) (RescheduleResult, error)
}

// Canceller is an optional event-cancel capability.
type Canceller interface {
	CancelEvent(context.Context, CancelRequest) (CancelResult, error)
}

// CreateResult is the outcome of an event creation.
type CreateResult struct {
	Event Event
}

// RescheduleResult is the outcome of an event reschedule.
type RescheduleResult struct {
	Event       Event
	Previous    Event
	Rescheduled bool
}

// CancelResult is the outcome of an event cancellation.
type CancelResult struct {
	Event     Event
	Previous  Event
	Cancelled bool
}

// SearcherFunc adapts a function to Searcher.
type SearcherFunc func(context.Context, SearchRequest) ([]Event, error)

// SearchEvents calls f(ctx, req).
func (f SearcherFunc) SearchEvents(ctx context.Context, req SearchRequest) ([]Event, error) {
	if f == nil {
		return nil, fmt.Errorf("scheduling: nil SearcherFunc")
	}
	return f(ctx, req)
}

// ReaderFunc adapts a function to Reader.
type ReaderFunc func(context.Context, ReadRequest) (Event, error)

// ReadEvent calls f(ctx, req).
func (f ReaderFunc) ReadEvent(ctx context.Context, req ReadRequest) (Event, error) {
	if f == nil {
		return Event{}, fmt.Errorf("scheduling: nil ReaderFunc")
	}
	return f(ctx, req)
}

// CreatorFunc adapts a function to Creator.
type CreatorFunc func(context.Context, CreateRequest) (CreateResult, error)

// CreateEvent calls f(ctx, req).
func (f CreatorFunc) CreateEvent(ctx context.Context, req CreateRequest) (CreateResult, error) {
	if f == nil {
		return CreateResult{}, fmt.Errorf("scheduling: nil CreatorFunc")
	}
	return f(ctx, req)
}

// ReschedulerFunc adapts a function to Rescheduler.
type ReschedulerFunc func(context.Context, RescheduleRequest) (RescheduleResult, error)

// RescheduleEvent calls f(ctx, req).
func (f ReschedulerFunc) RescheduleEvent(ctx context.Context, req RescheduleRequest) (RescheduleResult, error) {
	if f == nil {
		return RescheduleResult{}, fmt.Errorf("scheduling: nil ReschedulerFunc")
	}
	return f(ctx, req)
}

// CancellerFunc adapts a function to Canceller.
type CancellerFunc func(context.Context, CancelRequest) (CancelResult, error)

// CancelEvent calls f(ctx, req).
func (f CancellerFunc) CancelEvent(ctx context.Context, req CancelRequest) (CancelResult, error) {
	if f == nil {
		return CancelResult{}, fmt.Errorf("scheduling: nil CancellerFunc")
	}
	return f(ctx, req)
}

// EventStore is a concurrency-safe in-memory Searcher, Reader, Creator,
// Rescheduler, and Canceller for tests, examples, and short-lived agents.
type EventStore struct {
	mu     sync.RWMutex
	events map[string]Event
	order  []string
	next   int
}

// NewEventStore returns an in-memory event store seeded with events.
func NewEventStore(events []Event) *EventStore {
	store := &EventStore{
		events: make(map[string]Event),
		next:   1,
	}
	for _, item := range events {
		_, _ = store.insert(item)
	}
	return store
}

// SearchEvents returns a deterministic relevant subset of scheduling metadata.
func (s *EventStore) SearchEvents(ctx context.Context, req SearchRequest) ([]Event, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("scheduling: nil EventStore")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]Event, 0, len(s.order))
	for _, id := range s.order {
		if item, ok := s.events[id]; ok {
			items = append(items, cloneEvent(item))
		}
	}
	return (Selector{MaxEvents: req.Limit}).Select(items, req.Query, req.WindowStart, req.WindowEnd), nil
}

// ReadEvent loads one event by ID or title.
func (s *EventStore) ReadEvent(ctx context.Context, req ReadRequest) (Event, error) {
	if err := contextError(ctx); err != nil {
		return Event{}, err
	}
	if s == nil {
		return Event{}, fmt.Errorf("scheduling: nil EventStore")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if id := strings.TrimSpace(req.ID); id != "" {
		item, ok := s.events[id]
		if !ok {
			return Event{}, fmt.Errorf("event not found: %s", id)
		}
		return cloneEvent(item), nil
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		return Event{}, fmt.Errorf("scheduling: read requires id or title")
	}
	for _, id := range s.order {
		item, ok := s.events[id]
		if ok && item.Title == title {
			return cloneEvent(item), nil
		}
	}
	return Event{}, fmt.Errorf("event not found: %s", title)
}

// CreateEvent creates one new scheduled event.
func (s *EventStore) CreateEvent(ctx context.Context, req CreateRequest) (CreateResult, error) {
	if err := contextError(ctx); err != nil {
		return CreateResult{}, err
	}
	if s == nil {
		return CreateResult{}, fmt.Errorf("scheduling: nil EventStore")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	item, err := s.insert(req.Event)
	if err != nil {
		return CreateResult{}, err
	}
	return CreateResult{Event: item}, nil
}

// RescheduleEvent updates one existing event's start/end/time zone.
func (s *EventStore) RescheduleEvent(ctx context.Context, req RescheduleRequest) (RescheduleResult, error) {
	if err := contextError(ctx); err != nil {
		return RescheduleResult{}, err
	}
	if s == nil {
		return RescheduleResult{}, fmt.Errorf("scheduling: nil EventStore")
	}
	if err := validateTimeRange(req.Start, req.End); err != nil {
		return RescheduleResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id, item, err := s.lookupLocked(req.ID, req.Title)
	if err != nil {
		return RescheduleResult{}, err
	}
	previous := cloneEvent(item)
	item.Start = req.Start.UTC()
	item.End = req.End.UTC()
	if tz := strings.TrimSpace(req.TimeZone); tz != "" {
		item.TimeZone = tz
	}
	item.Metadata = mergeMetadata(item.Metadata, req.Metadata)
	s.events[id] = cloneEvent(item)
	return RescheduleResult{
		Event:       cloneEvent(item),
		Previous:    previous,
		Rescheduled: true,
	}, nil
}

// CancelEvent marks one existing event cancelled.
func (s *EventStore) CancelEvent(ctx context.Context, req CancelRequest) (CancelResult, error) {
	if err := contextError(ctx); err != nil {
		return CancelResult{}, err
	}
	if s == nil {
		return CancelResult{}, fmt.Errorf("scheduling: nil EventStore")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id, item, err := s.lookupLocked(req.ID, req.Title)
	if err != nil {
		return CancelResult{}, err
	}
	previous := cloneEvent(item)
	item.Status = StatusCancelled
	item.Metadata = mergeMetadata(item.Metadata, req.Metadata)
	if reason := strings.TrimSpace(req.Reason); reason != "" {
		if item.Metadata == nil {
			item.Metadata = make(map[string]any, 1)
		}
		item.Metadata["cancel_reason"] = reason
	}
	s.events[id] = cloneEvent(item)
	return CancelResult{
		Event:     cloneEvent(item),
		Previous:  previous,
		Cancelled: true,
	}, nil
}

// Selector deterministically selects relevant events. It is exported so hosts
// can reuse the default metadata-first ranking logic in their own Searcher
// implementations.
type Selector struct {
	MaxEvents int
}

// Select returns a stable relevant subset of events for query and time window.
func (s Selector) Select(events []Event, query string, windowStart, windowEnd time.Time) []Event {
	if len(events) == 0 {
		return nil
	}
	query = strings.ToLower(strings.TrimSpace(query))
	tokens := tokenize(query)
	items := make([]scoredEvent, 0, len(events))
	for i, item := range events {
		if !matchesWindow(item, windowStart, windowEnd) {
			continue
		}
		score := scoreEvent(item, query, tokens)
		if score > 0 || query == "" {
			items = append(items, scoredEvent{Event: cloneEvent(item), index: i, score: score})
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		left, right := items[i], items[j]
		if left.score != right.score {
			return left.score > right.score
		}
		if !left.Start.Equal(right.Start) {
			return left.Start.Before(right.Start)
		}
		if left.Title != right.Title {
			return left.Title < right.Title
		}
		return left.index < right.index
	})
	if s.MaxEvents > 0 && len(items) > s.MaxEvents {
		items = items[:s.MaxEvents]
	}
	out := make([]Event, len(items))
	for i, item := range items {
		out[i] = item.Event
	}
	return out
}

type scoredEvent struct {
	Event
	index int
	score int
}

func (s *EventStore) insert(item Event) (Event, error) {
	s.ensureLocked()
	item = normalizeEvent(item)
	if err := validateCreate(item); err != nil {
		return Event{}, err
	}
	if item.ID == "" {
		item.ID = s.nextIDLocked()
	}
	if _, ok := s.events[item.ID]; !ok {
		s.order = append(s.order, item.ID)
	}
	s.events[item.ID] = cloneEvent(item)
	s.bumpNextLocked(item.ID)
	return cloneEvent(item), nil
}

func (s *EventStore) lookupLocked(id, title string) (string, Event, error) {
	id = strings.TrimSpace(id)
	if id != "" {
		item, ok := s.events[id]
		if !ok {
			return "", Event{}, fmt.Errorf("event not found: %s", id)
		}
		return id, cloneEvent(item), nil
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return "", Event{}, fmt.Errorf("scheduling: request requires id or title")
	}
	for _, existingID := range s.order {
		item, ok := s.events[existingID]
		if ok && item.Title == title {
			return existingID, cloneEvent(item), nil
		}
	}
	return "", Event{}, fmt.Errorf("event not found: %s", title)
}

func (s *EventStore) ensureLocked() {
	if s.events == nil {
		s.events = make(map[string]Event)
	}
	if s.next <= 0 {
		s.next = 1
	}
}

func validateCreate(item Event) error {
	if strings.TrimSpace(item.Title) == "" {
		return fmt.Errorf("scheduling: title is required")
	}
	if err := validateTimeRange(item.Start, item.End); err != nil {
		return err
	}
	if len(item.Attendees) == 0 && participantKey(item.Organizer) == "" {
		return fmt.Errorf("scheduling: organizer or attendee is required")
	}
	return nil
}

func validateTimeRange(start, end time.Time) error {
	if start.IsZero() || end.IsZero() {
		return fmt.Errorf("scheduling: start and end are required")
	}
	if !end.After(start) {
		return fmt.Errorf("scheduling: end must be after start")
	}
	return nil
}

func matchesWindow(item Event, start, end time.Time) bool {
	if start.IsZero() && end.IsZero() {
		return true
	}
	if !start.IsZero() && !item.End.After(start) {
		return false
	}
	if !end.IsZero() && (item.Start.After(end) || item.Start.Equal(end)) {
		return false
	}
	return true
}

func scoreEvent(item Event, query string, tokens []string) int {
	if query == "" {
		return 1
	}
	score := 0
	title := strings.ToLower(item.Title)
	summary := strings.ToLower(item.Summary)
	location := strings.ToLower(item.Location)
	status := strings.ToLower(string(item.Status))
	timeZone := strings.ToLower(item.TimeZone)
	if strings.Contains(title, query) {
		score += 8
	}
	if strings.Contains(summary, query) {
		score += 5
	}
	if strings.Contains(location, query) {
		score += 4
	}
	if strings.Contains(status, query) || strings.Contains(timeZone, query) {
		score += 2
	}
	if participantMatches(item.Organizer, query) {
		score += 4
	}
	for _, participant := range item.Attendees {
		if participantMatches(participant, query) {
			score += 3
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
		case strings.Contains(title, token):
			score += 4
		case strings.Contains(summary, token):
			score += 3
		case strings.Contains(location, token):
			score += 2
		}
		if strings.Contains(status, token) || strings.Contains(timeZone, token) {
			score += 1
		}
		if participantMatches(item.Organizer, token) {
			score += 2
		}
		for _, participant := range item.Attendees {
			if participantMatches(participant, token) {
				score += 2
				break
			}
		}
	}
	return score
}

func participantMatches(item Participant, query string) bool {
	name := strings.ToLower(item.Name)
	address := strings.ToLower(item.Address)
	role := strings.ToLower(item.Role)
	return strings.Contains(name, query) || strings.Contains(address, query) || strings.Contains(role, query)
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

func normalizeEvent(item Event) Event {
	item.ID = strings.TrimSpace(item.ID)
	item.Title = strings.TrimSpace(item.Title)
	item.Summary = strings.TrimSpace(item.Summary)
	item.Description = strings.TrimSpace(item.Description)
	item.Location = strings.TrimSpace(item.Location)
	item.TimeZone = strings.TrimSpace(item.TimeZone)
	item.Organizer = cloneParticipant(item.Organizer)
	item.Attendees = cloneParticipants(item.Attendees)
	item.Tags = trimStrings(item.Tags)
	item.Metadata = model.CloneMetadata(item.Metadata)
	item.Start = item.Start.UTC()
	item.End = item.End.UTC()
	if item.Status == "" {
		item.Status = StatusScheduled
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

func cloneEvent(item Event) Event {
	item.Organizer = cloneParticipant(item.Organizer)
	item.Attendees = cloneParticipants(item.Attendees)
	item.Tags = append([]string(nil), item.Tags...)
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

func mergeMetadata(existing, extra map[string]any) map[string]any {
	if len(existing) == 0 && len(extra) == 0 {
		return nil
	}
	out := model.CloneMetadata(existing)
	if out == nil {
		out = make(map[string]any, len(extra))
	}
	for key, value := range extra {
		out[key] = value
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

func (s *EventStore) nextIDLocked() string {
	for {
		id := fmt.Sprintf("event-%d", s.next)
		s.next++
		if _, ok := s.events[id]; !ok {
			return id
		}
	}
}

func (s *EventStore) bumpNextLocked(id string) {
	var n int
	if _, err := fmt.Sscanf(id, "event-%d", &n); err == nil && n >= s.next {
		s.next = n + 1
	}
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
