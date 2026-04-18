package caldavclient

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

func TestParseAndFormatCalendarDataRoundTrip(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 4, 20, 15, 0, 0, 0, time.UTC)
	end := start.Add(45 * time.Minute)
	input := CalendarEvent{
		UID:         "kickoff-1",
		Summary:     "Project kickoff",
		Description: "Agenda\nReview roadmap",
		Location:    "Room 7",
		Organizer:   participant("alex@example.com", "Alex"),
		Attendees: []scheduling.Participant{
			participant("jordan@example.com", "Jordan"),
		},
		Start:    start,
		End:      end,
		TimeZone: "America/New_York",
		Status:   "CONFIRMED",
		Metadata: map[string]any{"source": "test"},
	}

	formatted, err := FormatCalendarData(input)
	if err != nil {
		t.Fatalf("FormatCalendarData() error = %v", err)
	}
	parsed, err := ParseCalendarData(formatted)
	if err != nil {
		t.Fatalf("ParseCalendarData() error = %v", err)
	}
	if parsed.UID != input.UID {
		t.Fatalf("UID = %q, want %q", parsed.UID, input.UID)
	}
	if parsed.Summary != input.Summary {
		t.Fatalf("Summary = %q, want %q", parsed.Summary, input.Summary)
	}
	if parsed.Description != input.Description {
		t.Fatalf("Description = %q, want %q", parsed.Description, input.Description)
	}
	if parsed.TimeZone != input.TimeZone {
		t.Fatalf("TimeZone = %q, want %q", parsed.TimeZone, input.TimeZone)
	}
	if got := parsed.Metadata["source"]; got != "test" {
		t.Fatalf("Metadata[source] = %#v, want %q", got, "test")
	}
	if !parsed.Start.Equal(start) {
		t.Fatalf("Start = %s, want %s", parsed.Start, start)
	}
	if !parsed.End.Equal(end) {
		t.Fatalf("End = %s, want %s", parsed.End, end)
	}
}

func TestQueryParsesMultiStatusAndSendsFilters(t *testing.T) {
	t.Parallel()

	var method string
	var depth string
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		depth = r.Header.Get("Depth")
		payload, _ := io.ReadAll(r.Body)
		body = string(payload)
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<multistatus xmlns="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">
  <response>
    <href>/calendar/events/kickoff-1.ics</href>
    <propstat>
      <status>HTTP/1.1 200 OK</status>
      <prop>
        <getetag>"etag-1"</getetag>
        <calendar-data>BEGIN:VCALENDAR
BEGIN:VEVENT
UID:kickoff-1
SUMMARY:Project kickoff
DTSTART:20260420T150000Z
DTEND:20260420T154500Z
END:VEVENT
END:VCALENDAR</calendar-data>
      </prop>
    </propstat>
  </response>
</multistatus>`))
	}))
	defer server.Close()

	client, err := New(server.URL+"/calendar/", WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	start := time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	items, err := client.Query(context.Background(), QueryRequest{
		UID:         "kickoff-1",
		Text:        "Project kickoff",
		WindowStart: start,
		WindowEnd:   end,
	})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if method != "REPORT" {
		t.Fatalf("method = %q, want REPORT", method)
	}
	if depth != "1" {
		t.Fatalf("Depth = %q, want 1", depth)
	}
	if !strings.Contains(body, `<c:prop-filter name="UID">`) {
		t.Fatalf("query body missing UID filter: %s", body)
	}
	if !strings.Contains(body, `<c:prop-filter name="SUMMARY">`) {
		t.Fatalf("query body missing SUMMARY filter: %s", body)
	}
	if !strings.Contains(body, `start="20260420T000000Z"`) || !strings.Contains(body, `end="20260421T000000Z"`) {
		t.Fatalf("query body missing time-range: %s", body)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if items[0].Event.UID != "kickoff-1" {
		t.Fatalf("UID = %q, want kickoff-1", items[0].Event.UID)
	}
	if items[0].ETag != `"etag-1"` {
		t.Fatalf("ETag = %q, want %q", items[0].ETag, `"etag-1"`)
	}
}

func TestGetPutDelete(t *testing.T) {
	t.Parallel()

	var lastIfMatch string
	var lastIfNoneMatch string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("ETag", `"etag-get"`)
			_, _ = w.Write([]byte(`BEGIN:VCALENDAR
BEGIN:VEVENT
UID:kickoff-1
SUMMARY:Project kickoff
DTSTART:20260420T150000Z
DTEND:20260420T154500Z
END:VEVENT
END:VCALENDAR`))
		case http.MethodPut:
			lastIfMatch = r.Header.Get("If-Match")
			lastIfNoneMatch = r.Header.Get("If-None-Match")
			w.Header().Set("ETag", `"etag-put"`)
			w.WriteHeader(http.StatusCreated)
		case http.MethodDelete:
			lastIfMatch = r.Header.Get("If-Match")
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()

	client, err := New(server.URL+"/calendar/", WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	resource, err := client.Get(context.Background(), "kickoff-1.ics")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if resource.ETag != `"etag-get"` {
		t.Fatalf("Get() ETag = %q, want %q", resource.ETag, `"etag-get"`)
	}

	putResult, err := client.Put(context.Background(), PutRequest{
		Href: "kickoff-1.ics",
		Event: CalendarEvent{
			UID:     "kickoff-1",
			Summary: "Project kickoff",
			Start:   time.Date(2026, 4, 20, 15, 0, 0, 0, time.UTC),
			End:     time.Date(2026, 4, 20, 15, 45, 0, 0, time.UTC),
		},
		ETag:        `"etag-get"`,
		IfNoneMatch: false,
	})
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if putResult.ETag != `"etag-put"` {
		t.Fatalf("Put() ETag = %q, want %q", putResult.ETag, `"etag-put"`)
	}
	if lastIfMatch != `"etag-get"` {
		t.Fatalf("If-Match = %q, want %q", lastIfMatch, `"etag-get"`)
	}

	if _, err := client.Put(context.Background(), PutRequest{
		Href: "kickoff-2.ics",
		Event: CalendarEvent{
			UID:     "kickoff-2",
			Summary: "Kickoff 2",
			Start:   time.Date(2026, 4, 20, 16, 0, 0, 0, time.UTC),
			End:     time.Date(2026, 4, 20, 16, 45, 0, 0, time.UTC),
		},
		IfNoneMatch: true,
	}); err != nil {
		t.Fatalf("Put() with If-None-Match error = %v", err)
	}
	if lastIfNoneMatch != "*" {
		t.Fatalf("If-None-Match = %q, want *", lastIfNoneMatch)
	}

	if err := client.Delete(context.Background(), "kickoff-1.ics", `"etag-put"`); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if lastIfMatch != `"etag-put"` {
		t.Fatalf("Delete If-Match = %q, want %q", lastIfMatch, `"etag-put"`)
	}
}

func participant(address, name string) scheduling.Participant {
	return scheduling.Participant{Address: address, Name: name}
}
