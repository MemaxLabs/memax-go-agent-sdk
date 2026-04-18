package googlecalendarclient

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/scheduling"
)

func TestListBuildsQueryAndParsesEvents(t *testing.T) {
	t.Parallel()

	var rawQueries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawQueries = append(rawQueries, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("pageToken") == "page-2" {
			_, _ = w.Write([]byte(`{
			"items": [{
				"id": "google-event-2",
				"iCalUID": "kickoff-2@example.com",
				"etag": "\"etag-2\"",
				"status": "confirmed",
				"summary": "Project kickoff follow-up",
				"description": "Agenda 2",
				"location": "Room 8",
				"organizer": {"email": "alex@example.com", "displayName": "Alex"},
				"attendees": [{"email": "jordan@example.com", "displayName": "Jordan"}],
				"start": {"dateTime": "2026-04-20T16:00:00Z", "timeZone": "UTC"},
				"end": {"dateTime": "2026-04-20T16:45:00Z", "timeZone": "UTC"},
				"created": "2026-04-01T09:00:00Z",
				"updated": "2026-04-02T09:00:00Z",
				"extendedProperties": {"private": {"memax_metadata": "{\"source\":\"test-2\"}"}}
			}]
		}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"items": [{
				"id": "google-event-1",
				"iCalUID": "kickoff-1@example.com",
				"etag": "\"etag-1\"",
				"status": "confirmed",
				"summary": "Project kickoff",
				"description": "Agenda",
				"location": "Room 7",
				"organizer": {"email": "alex@example.com", "displayName": "Alex"},
				"attendees": [{"email": "jordan@example.com", "displayName": "Jordan"}],
				"start": {"dateTime": "2026-04-20T15:00:00Z", "timeZone": "UTC"},
				"end": {"dateTime": "2026-04-20T15:45:00Z", "timeZone": "UTC"},
				"created": "2026-04-01T09:00:00Z",
				"updated": "2026-04-02T09:00:00Z",
				"extendedProperties": {"private": {"memax_metadata": "{\"source\":\"test\"}"}}
			}],
			"nextPageToken": "page-2"
		}`))
	}))
	defer server.Close()

	client, err := New("primary", WithBaseURL(server.URL), WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	items, err := client.List(context.Background(), ListRequest{
		Q:          "kickoff",
		ICalUID:    "kickoff-1@example.com",
		TimeMin:    time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC),
		TimeMax:    time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
		MaxResults: 10,
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	for _, fragment := range []string{
		"q=kickoff",
		"iCalUID=kickoff-1%40example.com",
		"timeMin=2026-04-20T00%3A00%3A00Z",
		"timeMax=2026-04-21T00%3A00%3A00Z",
		"maxResults=10",
		"singleEvents=true",
	} {
		if len(rawQueries) == 0 || !strings.Contains(rawQueries[0], fragment) {
			t.Fatalf("first raw query missing %q: %v", fragment, rawQueries)
		}
	}
	if len(rawQueries) != 2 {
		t.Fatalf("len(rawQueries) = %d, want 2", len(rawQueries))
	}
	if !strings.Contains(rawQueries[1], "pageToken=page-2") {
		t.Fatalf("second raw query missing pageToken: %s", rawQueries[1])
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(items))
	}
	if items[0].ID != "google-event-1" || items[1].ID != "google-event-2" {
		t.Fatalf("IDs = [%q %q], want [google-event-1 google-event-2]", items[0].ID, items[1].ID)
	}
	if got := items[1].ExtendedPropertiesPrivate["memax_metadata"]; got == "" {
		t.Fatalf("expected private extended property on second item, got empty")
	}
}

func TestGetInsertUpdateDelete(t *testing.T) {
	t.Parallel()

	var lastIfMatch string
	var auth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{
				"id": "google-event-1",
				"iCalUID": "kickoff-1@example.com",
				"etag": "\"etag-1\"",
				"summary": "Project kickoff",
				"start": {"dateTime": "2026-04-20T15:00:00Z", "timeZone": "UTC"},
				"end": {"dateTime": "2026-04-20T15:45:00Z", "timeZone": "UTC"}
			}`))
		case http.MethodPost, http.MethodPut:
			lastIfMatch = r.Header.Get("If-Match")
			body, _ := io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		case http.MethodDelete:
			lastIfMatch = r.Header.Get("If-Match")
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()

	client, err := New("primary", WithBaseURL(server.URL), WithHTTPClient(server.Client()), WithBearerToken("token-123"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	event, err := client.Get(context.Background(), "google-event-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if event.ID != "google-event-1" {
		t.Fatalf("Get().ID = %q, want google-event-1", event.ID)
	}
	inserted, err := client.Insert(context.Background(), Event{
		ID:      "google-event-2",
		Summary: "Project kickoff",
		Start:   time.Date(2026, 4, 20, 15, 0, 0, 0, time.UTC),
		End:     time.Date(2026, 4, 20, 15, 45, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Insert() error = %v", err)
	}
	if inserted.ID != "google-event-2" {
		t.Fatalf("Insert().ID = %q, want google-event-2", inserted.ID)
	}
	updated, err := client.Update(context.Background(), "google-event-1", Event{
		ID:      "google-event-1",
		Summary: "Project kickoff",
		Start:   time.Date(2026, 4, 20, 16, 0, 0, 0, time.UTC),
		End:     time.Date(2026, 4, 20, 16, 45, 0, 0, time.UTC),
	}, `"etag-1"`)
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if updated.ID != "google-event-1" {
		t.Fatalf("Update().ID = %q, want google-event-1", updated.ID)
	}
	if lastIfMatch != `"etag-1"` {
		t.Fatalf("If-Match = %q, want %q", lastIfMatch, `"etag-1"`)
	}
	if auth != "Bearer token-123" {
		t.Fatalf("Authorization = %q, want bearer token", auth)
	}
	if err := client.Delete(context.Background(), "google-event-1", `"etag-2"`); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if lastIfMatch != `"etag-2"` {
		t.Fatalf("Delete If-Match = %q, want %q", lastIfMatch, `"etag-2"`)
	}
}

func participant(address, name string) scheduling.Participant {
	return scheduling.Participant{Address: address, Name: name}
}
