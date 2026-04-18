// Package caldavstore adapts a CalDAV calendar collection to the scheduling
// contracts.
//
// SearchEvents returns metadata-only scheduling.Event values. Full description
// content remains available through ReadEvent, while the schedule tool layer
// still formats search results without descriptions as a defensive backstop.
package caldavstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/scheduling"
	"github.com/MemaxLabs/memax-go-agent-sdk/scheduling/caldavclient"
)

const (
	defaultConflictRetries = 2

	metadataAdapter          = "adapter"
	metadataConcurrencyToken = "concurrency_token"
)

// Store adapts one CalDAV calendar collection to scheduling.Searcher, Reader,
// Creator, Rescheduler, and Canceller.
type Store struct {
	client             *caldavclient.Client
	maxConflictRetries int
}

// Option mutates one store configuration field.
type Option func(*Store)

// WithMaxConflictRetries sets the number of retries after an ETag conflict.
func WithMaxConflictRetries(max int) Option {
	return func(s *Store) {
		s.maxConflictRetries = max
	}
}

// New returns a scheduling store over one CalDAV client.
func New(client *caldavclient.Client, opts ...Option) (*Store, error) {
	if client == nil {
		return nil, fmt.Errorf("caldav scheduling client is required")
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
		return nil, fmt.Errorf("caldav scheduling store is nil")
	}
	resources, err := s.client.Query(ctx, caldavclient.QueryRequest{
		Text:        strings.TrimSpace(req.Query),
		WindowStart: req.WindowStart,
		WindowEnd:   req.WindowEnd,
	})
	if err != nil {
		return nil, fmt.Errorf("search caldav events: %w", err)
	}
	items := make([]scheduling.Event, 0, len(resources))
	for _, resource := range resources {
		items = append(items, s.toSchedulingEvent(resource, false))
	}
	return (scheduling.Selector{MaxEvents: req.Limit}).Select(items, req.Query, req.WindowStart, req.WindowEnd), nil
}

// ReadEvent loads one full event by UID or title.
func (s *Store) ReadEvent(ctx context.Context, req scheduling.ReadRequest) (scheduling.Event, error) {
	if err := contextError(ctx); err != nil {
		return scheduling.Event{}, err
	}
	if s == nil {
		return scheduling.Event{}, fmt.Errorf("caldav scheduling store is nil")
	}
	resource, err := s.resolveResource(ctx, req.ID, req.Title)
	if err != nil {
		return scheduling.Event{}, err
	}
	return s.toSchedulingEvent(resource, true), nil
}

// CreateEvent creates one new event by UID.
func (s *Store) CreateEvent(ctx context.Context, req scheduling.CreateRequest) (scheduling.CreateResult, error) {
	if err := contextError(ctx); err != nil {
		return scheduling.CreateResult{}, err
	}
	if s == nil {
		return scheduling.CreateResult{}, fmt.Errorf("caldav scheduling store is nil")
	}
	item, err := normalizeCreateEvent(req.Event)
	if err != nil {
		return scheduling.CreateResult{}, err
	}
	uid := item.ID
	if uid == "" {
		uid, err = newUID()
		if err != nil {
			return scheduling.CreateResult{}, err
		}
	}
	event := toCalendarEvent(uid, item)
	href := hrefForUID(uid)
	putResult, err := s.client.Put(ctx, caldavclient.PutRequest{
		Href:        href,
		Event:       event,
		IfNoneMatch: true,
	})
	if err != nil {
		return scheduling.CreateResult{}, fmt.Errorf("create caldav event %s: %w", uid, err)
	}
	resource := caldavclient.Resource{
		Href: href,
		ETag: putResult.ETag,
		Event: caldavclient.CalendarEvent{
			UID:         event.UID,
			Summary:     event.Summary,
			Description: event.Description,
			Location:    event.Location,
			Organizer:   event.Organizer,
			Attendees:   cloneParticipants(event.Attendees),
			Start:       event.Start,
			End:         event.End,
			TimeZone:    event.TimeZone,
			Status:      event.Status,
			Metadata:    model.CloneMetadata(event.Metadata),
		},
	}
	return scheduling.CreateResult{Event: s.toSchedulingEvent(resource, true)}, nil
}

// RescheduleEvent updates one event timing. On ETag conflicts it re-reads the
// latest event state and retries a bounded number of times.
func (s *Store) RescheduleEvent(ctx context.Context, req scheduling.RescheduleRequest) (scheduling.RescheduleResult, error) {
	if err := contextError(ctx); err != nil {
		return scheduling.RescheduleResult{}, err
	}
	if s == nil {
		return scheduling.RescheduleResult{}, fmt.Errorf("caldav scheduling store is nil")
	}
	if err := validateTimeRange(req.Start, req.End); err != nil {
		return scheduling.RescheduleResult{}, err
	}
	current, err := s.resolveResource(ctx, req.ID, req.Title)
	if err != nil {
		return scheduling.RescheduleResult{}, err
	}
	uid := current.Event.UID
	href := current.Href
	for attempt := 0; ; attempt++ {
		previous := s.toSchedulingEvent(current, true)
		updated := current.Event
		updated.Start = req.Start.UTC()
		updated.End = req.End.UTC()
		if tz := strings.TrimSpace(req.TimeZone); tz != "" {
			updated.TimeZone = tz
		}
		updated.Metadata = mergeMetadata(updated.Metadata, filterInternalMetadata(req.Metadata))
		putResult, err := s.client.Put(ctx, caldavclient.PutRequest{
			Href:  current.Href,
			ETag:  current.ETag,
			Event: updated,
		})
		if err != nil {
			if errors.Is(err, caldavclient.ErrConflict) && attempt < s.maxConflictRetries {
				current, err = s.resolveResource(ctx, uid, "")
				if err != nil {
					return scheduling.RescheduleResult{}, err
				}
				if current.Href != href {
					return scheduling.RescheduleResult{}, fmt.Errorf("reschedule caldav event %s: event resource changed during retry", uid)
				}
				continue
			}
			return scheduling.RescheduleResult{}, fmt.Errorf("reschedule caldav event %s: %w", uid, err)
		}
		current.ETag = firstNonEmpty(putResult.ETag, current.ETag)
		current.Event = updated
		return scheduling.RescheduleResult{
			Event:       s.toSchedulingEvent(current, true),
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
		return scheduling.CancelResult{}, fmt.Errorf("caldav scheduling store is nil")
	}
	current, err := s.resolveResource(ctx, req.ID, req.Title)
	if err != nil {
		return scheduling.CancelResult{}, err
	}
	uid := current.Event.UID
	href := current.Href
	for attempt := 0; ; attempt++ {
		previous := s.toSchedulingEvent(current, true)
		updated := current.Event
		updated.Status = "CANCELLED"
		updated.Metadata = mergeMetadata(updated.Metadata, filterInternalMetadata(req.Metadata))
		if reason := strings.TrimSpace(req.Reason); reason != "" {
			updated.Metadata = mergeMetadata(updated.Metadata, map[string]any{"cancel_reason": reason})
		}
		putResult, err := s.client.Put(ctx, caldavclient.PutRequest{
			Href:  current.Href,
			ETag:  current.ETag,
			Event: updated,
		})
		if err != nil {
			if errors.Is(err, caldavclient.ErrConflict) && attempt < s.maxConflictRetries {
				current, err = s.resolveResource(ctx, uid, "")
				if err != nil {
					return scheduling.CancelResult{}, err
				}
				if current.Href != href {
					return scheduling.CancelResult{}, fmt.Errorf("cancel caldav event %s: event resource changed during retry", uid)
				}
				continue
			}
			return scheduling.CancelResult{}, fmt.Errorf("cancel caldav event %s: %w", uid, err)
		}
		current.ETag = firstNonEmpty(putResult.ETag, current.ETag)
		current.Event = updated
		return scheduling.CancelResult{
			Event:     s.toSchedulingEvent(current, true),
			Previous:  previous,
			Cancelled: true,
		}, nil
	}
}

func (s *Store) resolveResource(ctx context.Context, id, title string) (caldavclient.Resource, error) {
	id = strings.TrimSpace(id)
	if id != "" {
		items, err := s.client.Query(ctx, caldavclient.QueryRequest{UID: id})
		if err != nil {
			return caldavclient.Resource{}, fmt.Errorf("read caldav event %s: %w", id, err)
		}
		if len(items) == 0 {
			return caldavclient.Resource{}, fmt.Errorf("event not found: %s", id)
		}
		return items[0], nil
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return caldavclient.Resource{}, fmt.Errorf("scheduling: read requires id or title")
	}
	items, err := s.client.Query(ctx, caldavclient.QueryRequest{Text: title})
	if err != nil {
		return caldavclient.Resource{}, fmt.Errorf("read caldav event %s: %w", title, err)
	}
	matches := make([]caldavclient.Resource, 0, 1)
	for _, item := range items {
		if strings.TrimSpace(item.Event.Summary) == title {
			matches = append(matches, item)
		}
	}
	if len(matches) == 0 {
		return caldavclient.Resource{}, fmt.Errorf("event not found: %s", title)
	}
	sort.Slice(matches, func(i, j int) bool {
		if !matches[i].Event.Start.Equal(matches[j].Event.Start) {
			return matches[i].Event.Start.Before(matches[j].Event.Start)
		}
		if matches[i].Event.Summary != matches[j].Event.Summary {
			return matches[i].Event.Summary < matches[j].Event.Summary
		}
		return matches[i].Event.UID < matches[j].Event.UID
	})
	return matches[0], nil
}

func (s *Store) toSchedulingEvent(resource caldavclient.Resource, includeDescription bool) scheduling.Event {
	metadata := filterInternalMetadata(resource.Event.Metadata)
	if metadata == nil {
		metadata = make(map[string]any, 2)
	}
	metadata[metadataAdapter] = "caldav"
	if includeDescription && resource.ETag != "" {
		metadata[metadataConcurrencyToken] = resource.ETag
	}
	item := scheduling.Event{
		ID:        resource.Event.UID,
		Title:     strings.TrimSpace(resource.Event.Summary),
		Summary:   strings.TrimSpace(resource.Event.Summary),
		Location:  strings.TrimSpace(resource.Event.Location),
		Organizer: cloneParticipant(resource.Event.Organizer),
		Attendees: cloneParticipants(resource.Event.Attendees),
		Start:     resource.Event.Start.UTC(),
		End:       resource.Event.End.UTC(),
		TimeZone:  strings.TrimSpace(resource.Event.TimeZone),
		Status:    normalizeStatus(resource.Event.Status),
		Metadata:  metadata,
	}
	if includeDescription {
		item.Description = resource.Event.Description
	}
	return item
}

func toCalendarEvent(uid string, item scheduling.Event) caldavclient.CalendarEvent {
	return caldavclient.CalendarEvent{
		UID:         uid,
		Summary:     strings.TrimSpace(item.Title),
		Description: strings.TrimSpace(item.Description),
		Location:    strings.TrimSpace(item.Location),
		Organizer:   cloneParticipant(item.Organizer),
		Attendees:   cloneParticipants(item.Attendees),
		Start:       item.Start.UTC(),
		End:         item.End.UTC(),
		TimeZone:    strings.TrimSpace(item.TimeZone),
		Status:      calendarStatus(item.Status),
		Metadata:    filterInternalMetadata(item.Metadata),
	}
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

func calendarStatus(status scheduling.Status) string {
	if status == scheduling.StatusCancelled {
		return "CANCELLED"
	}
	return "CONFIRMED"
}

func normalizeStatus(status string) scheduling.Status {
	if strings.EqualFold(strings.TrimSpace(status), "CANCELLED") {
		return scheduling.StatusCancelled
	}
	return scheduling.StatusScheduled
}

func hrefForUID(uid string) string {
	return path.Clean(url.PathEscape(uid) + ".ics")
}

func newUID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generate caldav uid: %w", err)
	}
	return hex.EncodeToString(buf[:]) + "@memax", nil
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
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

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
