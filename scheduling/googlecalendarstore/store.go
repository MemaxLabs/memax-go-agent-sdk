// Package googlecalendarstore adapts the Google Calendar REST API to the
// scheduling contracts.
//
// SearchEvents returns metadata-only scheduling.Event values. Full description
// content remains available through ReadEvent, while the schedule tool layer
// still formats search results without descriptions as a defensive backstop.
package googlecalendarstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/scheduling"
	"github.com/MemaxLabs/memax-go-agent-sdk/scheduling/googlecalendarclient"
)

const (
	defaultConflictRetries = 2

	metadataAdapter          = "adapter"
	metadataConcurrencyToken = "concurrency_token"
	metadataPrivateBlob      = "memax_metadata"
)

// Store adapts one Google Calendar to scheduling.Searcher, Reader, Creator,
// Rescheduler, and Canceller.
type Store struct {
	client             *googlecalendarclient.Client
	maxConflictRetries int
}

// Option mutates one store configuration field.
type Option func(*Store)

// WithMaxConflictRetries sets the number of retries after an update conflict.
func WithMaxConflictRetries(max int) Option {
	return func(s *Store) {
		s.maxConflictRetries = max
	}
}

// New returns a scheduling store over one Google Calendar client.
func New(client *googlecalendarclient.Client, opts ...Option) (*Store, error) {
	if client == nil {
		return nil, fmt.Errorf("google calendar scheduling client is required")
	}
	store := &Store{
		client:             client,
		maxConflictRetries: defaultConflictRetries,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(store)
		}
	}
	if store.maxConflictRetries < 0 {
		store.maxConflictRetries = 0
	}
	return store, nil
}

// SearchEvents searches event metadata within one time window.
func (s *Store) SearchEvents(ctx context.Context, req scheduling.SearchRequest) ([]scheduling.Event, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("google calendar scheduling store is nil")
	}
	resources, err := s.client.List(ctx, googlecalendarclient.ListRequest{
		Q:          strings.TrimSpace(req.Query),
		TimeMin:    req.WindowStart,
		TimeMax:    req.WindowEnd,
		MaxResults: searchPageSize(req.Limit),
	})
	if err != nil {
		return nil, fmt.Errorf("search google calendar events: %w", err)
	}
	items := make([]scheduling.Event, 0, len(resources))
	for _, resource := range resources {
		items = append(items, s.toSchedulingEvent(resource, false))
	}
	return (scheduling.Selector{MaxEvents: req.Limit}).Select(items, req.Query, req.WindowStart, req.WindowEnd), nil
}

// ReadEvent loads one full event by ID or exact title.
func (s *Store) ReadEvent(ctx context.Context, req scheduling.ReadRequest) (scheduling.Event, error) {
	if err := contextError(ctx); err != nil {
		return scheduling.Event{}, err
	}
	if s == nil {
		return scheduling.Event{}, fmt.Errorf("google calendar scheduling store is nil")
	}
	event, err := s.resolveEvent(ctx, req.ID, req.Title)
	if err != nil {
		return scheduling.Event{}, err
	}
	return s.toSchedulingEvent(event, true), nil
}

// CreateEvent creates one new event.
func (s *Store) CreateEvent(ctx context.Context, req scheduling.CreateRequest) (scheduling.CreateResult, error) {
	if err := contextError(ctx); err != nil {
		return scheduling.CreateResult{}, err
	}
	if s == nil {
		return scheduling.CreateResult{}, fmt.Errorf("google calendar scheduling store is nil")
	}
	item, err := normalizeCreateEvent(req.Event)
	if err != nil {
		return scheduling.CreateResult{}, err
	}
	eventID := item.ID
	if eventID == "" {
		eventID, err = newEventID()
		if err != nil {
			return scheduling.CreateResult{}, err
		}
	}
	event := toGoogleEvent(eventID, item)
	created, err := s.client.Insert(ctx, event)
	if err != nil {
		return scheduling.CreateResult{}, fmt.Errorf("create google calendar event %s: %w", eventID, err)
	}
	return scheduling.CreateResult{Event: s.toSchedulingEvent(created, true)}, nil
}

// RescheduleEvent updates one event timing. On conflicts it re-reads the
// current event and retries a bounded number of times.
func (s *Store) RescheduleEvent(ctx context.Context, req scheduling.RescheduleRequest) (scheduling.RescheduleResult, error) {
	if err := contextError(ctx); err != nil {
		return scheduling.RescheduleResult{}, err
	}
	if s == nil {
		return scheduling.RescheduleResult{}, fmt.Errorf("google calendar scheduling store is nil")
	}
	if err := validateTimeRange(req.Start, req.End); err != nil {
		return scheduling.RescheduleResult{}, err
	}
	current, err := s.resolveEvent(ctx, req.ID, req.Title)
	if err != nil {
		return scheduling.RescheduleResult{}, err
	}
	eventID := current.ID
	for attempt := 0; ; attempt++ {
		previous := s.toSchedulingEvent(current, true)
		updated := current
		updated.Start = req.Start.UTC()
		updated.End = req.End.UTC()
		if tz := strings.TrimSpace(req.TimeZone); tz != "" {
			updated.TimeZone = tz
		}
		updated.ExtendedPropertiesPrivate = encodeMetadata(mergeMetadata(decodeMetadata(updated.ExtendedPropertiesPrivate), filterInternalMetadata(req.Metadata)))
		saved, err := s.client.Update(ctx, eventID, updated, current.ETag)
		if err != nil {
			if errors.Is(err, googlecalendarclient.ErrConflict) && attempt < s.maxConflictRetries {
				current, err = s.client.Get(ctx, eventID)
				if err != nil {
					return scheduling.RescheduleResult{}, fmt.Errorf("read google calendar event %s after conflict: %w", eventID, err)
				}
				continue
			}
			return scheduling.RescheduleResult{}, fmt.Errorf("reschedule google calendar event %s: %w", eventID, err)
		}
		return scheduling.RescheduleResult{
			Event:       s.toSchedulingEvent(saved, true),
			Previous:    previous,
			Rescheduled: true,
		}, nil
	}
}

// CancelEvent marks one event cancelled and preserves the previous state.
func (s *Store) CancelEvent(ctx context.Context, req scheduling.CancelRequest) (scheduling.CancelResult, error) {
	if err := contextError(ctx); err != nil {
		return scheduling.CancelResult{}, err
	}
	if s == nil {
		return scheduling.CancelResult{}, fmt.Errorf("google calendar scheduling store is nil")
	}
	current, err := s.resolveEvent(ctx, req.ID, req.Title)
	if err != nil {
		return scheduling.CancelResult{}, err
	}
	eventID := current.ID
	for attempt := 0; ; attempt++ {
		previous := s.toSchedulingEvent(current, true)
		updated := current
		updated.Status = "cancelled"
		metadata := mergeMetadata(decodeMetadata(updated.ExtendedPropertiesPrivate), filterInternalMetadata(req.Metadata))
		if reason := strings.TrimSpace(req.Reason); reason != "" {
			metadata["cancel_reason"] = reason
		}
		updated.ExtendedPropertiesPrivate = encodeMetadata(metadata)
		saved, err := s.client.Update(ctx, eventID, updated, current.ETag)
		if err != nil {
			if errors.Is(err, googlecalendarclient.ErrConflict) && attempt < s.maxConflictRetries {
				current, err = s.client.Get(ctx, eventID)
				if err != nil {
					return scheduling.CancelResult{}, fmt.Errorf("read google calendar event %s after conflict: %w", eventID, err)
				}
				continue
			}
			return scheduling.CancelResult{}, fmt.Errorf("cancel google calendar event %s: %w", eventID, err)
		}
		return scheduling.CancelResult{
			Event:     s.toSchedulingEvent(saved, true),
			Previous:  previous,
			Cancelled: true,
		}, nil
	}
}

func (s *Store) resolveEvent(ctx context.Context, id, title string) (googlecalendarclient.Event, error) {
	id = strings.TrimSpace(id)
	if id != "" {
		event, err := s.client.Get(ctx, id)
		if err != nil {
			if errors.Is(err, googlecalendarclient.ErrNotFound) {
				return googlecalendarclient.Event{}, fmt.Errorf("event not found: %s", id)
			}
			return googlecalendarclient.Event{}, fmt.Errorf("read google calendar event %s: %w", id, err)
		}
		return event, nil
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return googlecalendarclient.Event{}, fmt.Errorf("scheduling: read requires id or title")
	}
	// Use Google's fuzzy q filter to reduce network payload, then keep exact
	// summary matching local so title lookup stays deterministic across servers.
	items, err := s.client.List(ctx, googlecalendarclient.ListRequest{
		Q:          title,
		MaxResults: 50,
	})
	if err != nil {
		return googlecalendarclient.Event{}, fmt.Errorf("read google calendar event %s: %w", title, err)
	}
	matches := make([]googlecalendarclient.Event, 0, 1)
	for _, item := range items {
		if strings.TrimSpace(item.Summary) == title {
			matches = append(matches, item)
		}
	}
	if len(matches) == 0 {
		return googlecalendarclient.Event{}, fmt.Errorf("event not found: %s", title)
	}
	sort.Slice(matches, func(i, j int) bool {
		if !matches[i].Start.Equal(matches[j].Start) {
			return matches[i].Start.Before(matches[j].Start)
		}
		if matches[i].Summary != matches[j].Summary {
			return matches[i].Summary < matches[j].Summary
		}
		return matches[i].ID < matches[j].ID
	})
	return matches[0], nil
}

func (s *Store) toSchedulingEvent(event googlecalendarclient.Event, includeDescription bool) scheduling.Event {
	metadata := decodeMetadata(event.ExtendedPropertiesPrivate)
	if metadata == nil {
		metadata = make(map[string]any, 2)
	}
	metadata[metadataAdapter] = "google_calendar"
	if includeDescription && strings.TrimSpace(event.ETag) != "" {
		metadata[metadataConcurrencyToken] = strings.TrimSpace(event.ETag)
	}
	item := scheduling.Event{
		ID:        strings.TrimSpace(event.ID),
		Title:     strings.TrimSpace(event.Summary),
		Summary:   strings.TrimSpace(event.Summary),
		Location:  strings.TrimSpace(event.Location),
		Organizer: cloneParticipant(event.Organizer),
		Attendees: cloneParticipants(event.Attendees),
		Start:     event.Start.UTC(),
		End:       event.End.UTC(),
		TimeZone:  strings.TrimSpace(event.TimeZone),
		Status:    normalizeStatus(event.Status),
		Metadata:  metadata,
	}
	if includeDescription {
		item.Description = event.Description
	}
	return item
}

func toGoogleEvent(eventID string, item scheduling.Event) googlecalendarclient.Event {
	return googlecalendarclient.Event{
		ID:                        eventID,
		Summary:                   strings.TrimSpace(item.Title),
		Description:               strings.TrimSpace(item.Description),
		Location:                  strings.TrimSpace(item.Location),
		Organizer:                 cloneParticipant(item.Organizer),
		Attendees:                 cloneParticipants(item.Attendees),
		Start:                     item.Start.UTC(),
		End:                       item.End.UTC(),
		TimeZone:                  strings.TrimSpace(item.TimeZone),
		Status:                    googleStatus(item.Status),
		ExtendedPropertiesPrivate: encodeMetadata(filterInternalMetadata(item.Metadata)),
	}
}

func searchPageSize(limit int) int {
	if limit <= 0 {
		return 50
	}
	size := limit * 4
	if size < 50 {
		size = 50
	}
	if size > 250 {
		size = 250
	}
	return size
}

func normalizeCreateEvent(item scheduling.Event) (scheduling.Event, error) {
	item.ID = strings.TrimSpace(item.ID)
	item.Title = strings.TrimSpace(item.Title)
	item.Summary = strings.TrimSpace(item.Summary)
	item.Description = strings.TrimSpace(item.Description)
	item.Location = strings.TrimSpace(item.Location)
	item.Organizer = cloneParticipant(item.Organizer)
	item.Attendees = cloneParticipants(item.Attendees)
	item.Start = item.Start.UTC()
	item.End = item.End.UTC()
	item.TimeZone = strings.TrimSpace(item.TimeZone)
	item.Metadata = filterInternalMetadata(item.Metadata)
	if item.Title == "" {
		return scheduling.Event{}, fmt.Errorf("scheduling: title is required")
	}
	if err := validateTimeRange(item.Start, item.End); err != nil {
		return scheduling.Event{}, err
	}
	if len(item.Attendees) == 0 && participantKey(item.Organizer) == "" {
		return scheduling.Event{}, fmt.Errorf("scheduling: organizer or attendee is required")
	}
	return item, nil
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

func googleStatus(status scheduling.Status) string {
	if status == scheduling.StatusCancelled {
		return "cancelled"
	}
	return "confirmed"
}

func normalizeStatus(status string) scheduling.Status {
	if strings.EqualFold(strings.TrimSpace(status), "cancelled") {
		return scheduling.StatusCancelled
	}
	return scheduling.StatusScheduled
}

func participantKey(item scheduling.Participant) string {
	switch {
	case item.Address != "":
		return strings.ToLower(item.Address)
	case item.Name != "":
		return strings.ToLower(item.Name)
	default:
		return strings.ToLower(item.ID)
	}
}

func encodeMetadata(metadata map[string]any) map[string]string {
	if len(metadata) == 0 {
		return nil
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return nil
	}
	return map[string]string{metadataPrivateBlob: string(raw)}
}

func decodeMetadata(private map[string]string) map[string]any {
	if len(private) == 0 {
		return nil
	}
	raw := strings.TrimSpace(private[metadataPrivateBlob])
	if raw == "" {
		return nil
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
		return nil
	}
	return model.CloneMetadata(metadata)
}

func mergeMetadata(existing, extra map[string]any) map[string]any {
	if len(existing) == 0 && len(extra) == 0 {
		return nil
	}
	merged := model.CloneMetadata(existing)
	if merged == nil {
		merged = make(map[string]any, len(extra))
	}
	for key, value := range extra {
		merged[key] = value
	}
	return merged
}

func filterInternalMetadata(metadata map[string]any) map[string]any {
	if len(metadata) == 0 {
		return nil
	}
	cloned := model.CloneMetadata(metadata)
	delete(cloned, metadataAdapter)
	delete(cloned, metadataConcurrencyToken)
	if len(cloned) == 0 {
		return nil
	}
	return cloned
}

func cloneParticipant(item scheduling.Participant) scheduling.Participant {
	return scheduling.Participant{
		ID:      strings.TrimSpace(item.ID),
		Name:    strings.TrimSpace(item.Name),
		Address: strings.TrimSpace(item.Address),
		Role:    strings.TrimSpace(item.Role),
	}
}

func cloneParticipants(items []scheduling.Participant) []scheduling.Participant {
	if len(items) == 0 {
		return nil
	}
	out := make([]scheduling.Participant, len(items))
	for i, item := range items {
		out[i] = cloneParticipant(item)
	}
	return out
}

func newEventID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generate google calendar event id: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
