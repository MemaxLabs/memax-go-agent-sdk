package googlecalendarstore

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/scheduling"
	"github.com/MemaxLabs/memax-go-agent-sdk/scheduling/googlecalendarclient"
)

func TestStoreSearchAndReadMetadataFirst(t *testing.T) {
	t.Parallel()

	server := newGoogleCalendarServer()
	server.put(eventRecord{
		ID:          "google-event-1",
		ICalUID:     "kickoff-1@example.com",
		ETag:        `"etag-1"`,
		Status:      "confirmed",
		Summary:     "Project kickoff",
		Description: "Secret agenda",
		Location:    "Room 7",
		Start:       time.Date(2026, 4, 20, 15, 0, 0, 0, time.UTC),
		End:         time.Date(2026, 4, 20, 15, 45, 0, 0, time.UTC),
		TimeZone:    "UTC",
		Organizer:   participant("alex@example.com", "Alex"),
		Attendees:   []scheduling.Participant{participant("jordan@example.com", "Jordan")},
		Private:     encodeMetadata(map[string]any{"source": "test"}),
	})
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	client, err := googlecalendarclient.New("primary",
		googlecalendarclient.WithBaseURL(httpServer.URL),
		googlecalendarclient.WithHTTPClient(httpServer.Client()),
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	store, err := New(client)
	if err != nil {
		t.Fatalf("New store error = %v", err)
	}

	items, err := store.SearchEvents(context.Background(), scheduling.SearchRequest{
		Query:       "kickoff",
		WindowStart: time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC),
		WindowEnd:   time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
		Limit:       8,
	})
	if err != nil {
		t.Fatalf("SearchEvents() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if items[0].Description != "" {
		t.Fatalf("SearchEvents() leaked description: %q", items[0].Description)
	}
	if got := items[0].Metadata[metadataConcurrencyToken]; got != nil {
		t.Fatalf("SearchEvents() leaked %s: %#v", metadataConcurrencyToken, got)
	}
	if got := items[0].Metadata["source"]; got != "test" {
		t.Fatalf("SearchEvents() source metadata = %#v, want %q", got, "test")
	}

	item, err := store.ReadEvent(context.Background(), scheduling.ReadRequest{ID: "google-event-1"})
	if err != nil {
		t.Fatalf("ReadEvent() error = %v", err)
	}
	if item.Description != "Secret agenda" {
		t.Fatalf("Description = %q, want Secret agenda", item.Description)
	}
	if got := item.Metadata[metadataConcurrencyToken]; got != `"etag-1"` {
		t.Fatalf("ReadEvent() %s = %#v, want %q", metadataConcurrencyToken, got, `"etag-1"`)
	}
}

func TestStoreCreateAndCancel(t *testing.T) {
	t.Parallel()

	server := newGoogleCalendarServer()
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	client, err := googlecalendarclient.New("primary",
		googlecalendarclient.WithBaseURL(httpServer.URL),
		googlecalendarclient.WithHTTPClient(httpServer.Client()),
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	store, err := New(client)
	if err != nil {
		t.Fatalf("New store error = %v", err)
	}

	created, err := store.CreateEvent(context.Background(), scheduling.CreateRequest{
		Event: scheduling.Event{
			ID:    "google-event-2",
			Title: "Project kickoff",
			Start: time.Date(2026, 4, 20, 15, 0, 0, 0, time.UTC),
			End:   time.Date(2026, 4, 20, 15, 45, 0, 0, time.UTC),
			Organizer: scheduling.Participant{
				Address: "alex@example.com",
				Name:    "Alex",
			},
			Metadata: map[string]any{"source": "test"},
		},
	})
	if err != nil {
		t.Fatalf("CreateEvent() error = %v", err)
	}
	if created.Event.ID != "google-event-2" {
		t.Fatalf("CreateEvent().Event.ID = %q, want google-event-2", created.Event.ID)
	}
	cancelled, err := store.CancelEvent(context.Background(), scheduling.CancelRequest{
		ID:     "google-event-2",
		Reason: "rescheduled elsewhere",
	})
	if err != nil {
		t.Fatalf("CancelEvent() error = %v", err)
	}
	if cancelled.Previous.Status != scheduling.StatusScheduled {
		t.Fatalf("Previous.Status = %q, want %q", cancelled.Previous.Status, scheduling.StatusScheduled)
	}
	if cancelled.Event.Status != scheduling.StatusCancelled {
		t.Fatalf("Event.Status = %q, want %q", cancelled.Event.Status, scheduling.StatusCancelled)
	}
	if got := cancelled.Event.Metadata["cancel_reason"]; got != "rescheduled elsewhere" {
		t.Fatalf("cancel_reason = %#v, want %q", got, "rescheduled elsewhere")
	}
}

func TestStoreRescheduleRetriesOnConflict(t *testing.T) {
	t.Parallel()

	server := newGoogleCalendarServer()
	server.put(eventRecord{
		ID:        "google-event-3",
		ICalUID:   "kickoff-3@example.com",
		ETag:      `"etag-1"`,
		Status:    "confirmed",
		Summary:   "Project kickoff",
		Start:     time.Date(2026, 4, 20, 15, 0, 0, 0, time.UTC),
		End:       time.Date(2026, 4, 20, 15, 45, 0, 0, time.UTC),
		TimeZone:  "UTC",
		Organizer: participant("alex@example.com", "Alex"),
	})
	server.failNextUpdateWithConflict("google-event-3")
	server.onConflict = func(id string) {
		server.put(eventRecord{
			ID:        id,
			ICalUID:   "kickoff-3@example.com",
			ETag:      `"etag-2"`,
			Status:    "confirmed",
			Summary:   "Project kickoff",
			Start:     time.Date(2026, 4, 20, 16, 0, 0, 0, time.UTC),
			End:       time.Date(2026, 4, 20, 16, 45, 0, 0, time.UTC),
			TimeZone:  "UTC",
			Organizer: participant("alex@example.com", "Alex"),
		})
	}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	client, err := googlecalendarclient.New("primary",
		googlecalendarclient.WithBaseURL(httpServer.URL),
		googlecalendarclient.WithHTTPClient(httpServer.Client()),
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	store, err := New(client)
	if err != nil {
		t.Fatalf("New store error = %v", err)
	}

	start := time.Date(2026, 4, 20, 17, 0, 0, 0, time.UTC)
	end := start.Add(45 * time.Minute)
	result, err := store.RescheduleEvent(context.Background(), scheduling.RescheduleRequest{
		ID:    "google-event-3",
		Start: start,
		End:   end,
	})
	if err != nil {
		t.Fatalf("RescheduleEvent() error = %v", err)
	}
	if !result.Previous.Start.Equal(time.Date(2026, 4, 20, 16, 0, 0, 0, time.UTC)) {
		t.Fatalf("Previous.Start = %s, want %s", result.Previous.Start, time.Date(2026, 4, 20, 16, 0, 0, 0, time.UTC))
	}
	if !result.Event.Start.Equal(start) {
		t.Fatalf("Event.Start = %s, want %s", result.Event.Start, start)
	}
}

type googleCalendarServer struct {
	mu         sync.Mutex
	events     map[string]eventRecord
	conflicts  map[string]int
	onConflict func(id string)
}

type eventRecord struct {
	ID          string
	ICalUID     string
	ETag        string
	Status      string
	Summary     string
	Description string
	Location    string
	Organizer   scheduling.Participant
	Attendees   []scheduling.Participant
	Start       time.Time
	End         time.Time
	TimeZone    string
	Created     time.Time
	Updated     time.Time
	Private     map[string]string
}

func newGoogleCalendarServer() *googleCalendarServer {
	return &googleCalendarServer{
		events:    make(map[string]eventRecord),
		conflicts: make(map[string]int),
	}
}

func (s *googleCalendarServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/events"):
		s.handleList(w, r)
	case r.Method == http.MethodGet:
		s.handleGet(w, r)
	case r.Method == http.MethodPost:
		s.handleInsert(w, r)
	case r.Method == http.MethodPut:
		s.handleUpdate(w, r)
	case r.Method == http.MethodDelete:
		s.handleDelete(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *googleCalendarServer) handleList(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	icalUID := strings.TrimSpace(r.URL.Query().Get("iCalUID"))
	timeMin, _ := parseOptionalRFC3339(r.URL.Query().Get("timeMin"))
	timeMax, _ := parseOptionalRFC3339(r.URL.Query().Get("timeMax"))

	var items []eventRecord
	for _, item := range s.events {
		if icalUID != "" && item.ICalUID != icalUID {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(item.Summary), strings.ToLower(q)) {
			continue
		}
		if !matchesWindow(item.Start, item.End, timeMin, timeMax) {
			continue
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].Start.Equal(items[j].Start) {
			return items[i].Start.Before(items[j].Start)
		}
		return items[i].ID < items[j].ID
	})
	respondJSON(w, map[string]any{"items": itemsJSON(items)})
}

func (s *googleCalendarServer) handleGet(w http.ResponseWriter, r *http.Request) {
	id := pathBase(r.URL.Path)
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.events[id]
	if !ok {
		http.NotFound(w, r)
		return
	}
	respondJSON(w, eventJSON(item))
}

func (s *googleCalendarServer) handleInsert(w http.ResponseWriter, r *http.Request) {
	var resource map[string]any
	_ = json.NewDecoder(r.Body).Decode(&resource)
	record := recordFromJSON(resource)
	if record.ID == "" {
		record.ID = "generated-google-event"
	}
	if record.ETag == "" {
		record.ETag = nextETag(record.ID)
	}
	if record.Created.IsZero() {
		record.Created = time.Now().UTC()
	}
	record.Updated = time.Now().UTC()
	s.put(record)
	respondJSON(w, eventJSON(record))
}

func (s *googleCalendarServer) handleUpdate(w http.ResponseWriter, r *http.Request) {
	id := pathBase(r.URL.Path)
	var resource map[string]any
	_ = json.NewDecoder(r.Body).Decode(&resource)
	record := recordFromJSON(resource)
	record.ID = id

	s.mu.Lock()
	callback := s.onConflict
	if s.conflicts[id] > 0 {
		s.conflicts[id]--
		s.mu.Unlock()
		if callback != nil {
			callback(id)
		}
		w.WriteHeader(http.StatusPreconditionFailed)
		return
	}
	current, ok := s.events[id]
	if !ok {
		s.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	record.Created = current.Created
	record.Updated = time.Now().UTC()
	record.ETag = nextETag(id)
	s.events[id] = record
	s.mu.Unlock()
	respondJSON(w, eventJSON(record))
}

func (s *googleCalendarServer) handleDelete(w http.ResponseWriter, r *http.Request) {
	id := pathBase(r.URL.Path)
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.events, id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *googleCalendarServer) put(record eventRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if record.ETag == "" {
		record.ETag = nextETag(record.ID)
	}
	if record.Created.IsZero() {
		record.Created = time.Now().UTC()
	}
	record.Updated = time.Now().UTC()
	s.events[record.ID] = record
}

func (s *googleCalendarServer) failNextUpdateWithConflict(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conflicts[id]++
}

func itemsJSON(items []eventRecord) []map[string]any {
	out := make([]map[string]any, len(items))
	for i, item := range items {
		out[i] = eventJSON(item)
	}
	return out
}

func eventJSON(item eventRecord) map[string]any {
	return map[string]any{
		"id":          item.ID,
		"iCalUID":     item.ICalUID,
		"etag":        item.ETag,
		"status":      item.Status,
		"summary":     item.Summary,
		"description": item.Description,
		"location":    item.Location,
		"organizer": map[string]any{
			"email":       item.Organizer.Address,
			"displayName": item.Organizer.Name,
		},
		"attendees": attendeesJSON(item.Attendees),
		"start": map[string]any{
			"dateTime": item.Start.UTC().Format(time.RFC3339),
			"timeZone": item.TimeZone,
		},
		"end": map[string]any{
			"dateTime": item.End.UTC().Format(time.RFC3339),
			"timeZone": item.TimeZone,
		},
		"created": item.Created.UTC().Format(time.RFC3339),
		"updated": item.Updated.UTC().Format(time.RFC3339),
		"extendedProperties": map[string]any{
			"private": item.Private,
		},
	}
}

func attendeesJSON(items []scheduling.Participant) []map[string]any {
	if len(items) == 0 {
		return nil
	}
	out := make([]map[string]any, len(items))
	for i, item := range items {
		out[i] = map[string]any{
			"email":       item.Address,
			"displayName": item.Name,
		}
	}
	return out
}

func recordFromJSON(resource map[string]any) eventRecord {
	record := eventRecord{
		ID:          stringValue(resource["id"]),
		ICalUID:     stringValue(resource["iCalUID"]),
		ETag:        stringValue(resource["etag"]),
		Status:      stringValue(resource["status"]),
		Summary:     stringValue(resource["summary"]),
		Description: stringValue(resource["description"]),
		Location:    stringValue(resource["location"]),
		TimeZone:    nestedString(resource, "start", "timeZone"),
		Private:     nestedStringMap(resource, "extendedProperties", "private"),
		Organizer: scheduling.Participant{
			Address: nestedString(resource, "organizer", "email"),
			Name:    nestedString(resource, "organizer", "displayName"),
		},
		Attendees: nestedParticipants(resource["attendees"]),
	}
	record.Start, _ = parseOptionalRFC3339(nestedString(resource, "start", "dateTime"))
	record.End, _ = parseOptionalRFC3339(nestedString(resource, "end", "dateTime"))
	record.Created, _ = parseOptionalRFC3339(stringValue(resource["created"]))
	record.Updated, _ = parseOptionalRFC3339(stringValue(resource["updated"]))
	return record
}

func nestedParticipants(value any) []scheduling.Participant {
	items, _ := value.([]any)
	if len(items) == 0 {
		return nil
	}
	out := make([]scheduling.Participant, 0, len(items))
	for _, item := range items {
		m, _ := item.(map[string]any)
		if m == nil {
			continue
		}
		out = append(out, scheduling.Participant{
			Address: stringValue(m["email"]),
			Name:    stringValue(m["displayName"]),
		})
	}
	return out
}

func nestedString(resource map[string]any, keys ...string) string {
	var current any = resource
	for _, key := range keys {
		m, _ := current.(map[string]any)
		if m == nil {
			return ""
		}
		current = m[key]
	}
	return stringValue(current)
}

func nestedStringMap(resource map[string]any, keys ...string) map[string]string {
	var current any = resource
	for _, key := range keys {
		m, _ := current.(map[string]any)
		if m == nil {
			return nil
		}
		current = m[key]
	}
	m, _ := current.(map[string]any)
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for key, value := range m {
		out[key] = stringValue(value)
	}
	return out
}

func stringValue(value any) string {
	s, _ := value.(string)
	return s
}

func pathBase(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

func matchesWindow(eventStart, eventEnd, start, end time.Time) bool {
	if !start.IsZero() && !eventEnd.After(start) {
		return false
	}
	if !end.IsZero() && !eventStart.Before(end) {
		return false
	}
	return true
}

func parseOptionalRFC3339(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, raw)
}

func nextETag(id string) string {
	return `"` + id + `-etag-` + time.Now().UTC().Format("150405.000000") + `"`
}

func respondJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func participant(address, name string) scheduling.Participant {
	return scheduling.Participant{Address: address, Name: name}
}
