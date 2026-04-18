package caldavstore

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/scheduling"
	"github.com/MemaxLabs/memax-go-agent-sdk/scheduling/caldavclient"
)

func TestStoreSearchAndReadMetadataFirst(t *testing.T) {
	t.Parallel()

	server := newCalDAVTestServer()
	server.put(eventResource("kickoff-1", "Project kickoff", "Secret agenda", time.Date(2026, 4, 20, 15, 0, 0, 0, time.UTC), 45*time.Minute, "America/New_York"), "")
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	client, err := caldavclient.New(httpServer.URL+"/calendar/", caldavclient.WithHTTPClient(httpServer.Client()))
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
	if items[0].ID != "kickoff-1" {
		t.Fatalf("ID = %q, want kickoff-1", items[0].ID)
	}
	if got := items[0].Metadata[metadataAdapter]; got != "caldav" {
		t.Fatalf("SearchEvents() %s = %#v, want %q", metadataAdapter, got, "caldav")
	}
	if got := items[0].Metadata[metadataConcurrencyToken]; got != nil {
		t.Fatalf("SearchEvents() leaked %s: %#v", metadataConcurrencyToken, got)
	}

	item, err := store.ReadEvent(context.Background(), scheduling.ReadRequest{ID: "kickoff-1"})
	if err != nil {
		t.Fatalf("ReadEvent() error = %v", err)
	}
	if item.Description != "Secret agenda" {
		t.Fatalf("Description = %q, want %q", item.Description, "Secret agenda")
	}
	if got := item.Metadata[metadataConcurrencyToken]; got == nil {
		t.Fatalf("ReadEvent() missing %s", metadataConcurrencyToken)
	}
}

func TestStoreCreateAndCancel(t *testing.T) {
	t.Parallel()

	server := newCalDAVTestServer()
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	client, err := caldavclient.New(httpServer.URL+"/calendar/", caldavclient.WithHTTPClient(httpServer.Client()))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	store, err := New(client)
	if err != nil {
		t.Fatalf("New store error = %v", err)
	}

	created, err := store.CreateEvent(context.Background(), scheduling.CreateRequest{
		Event: scheduling.Event{
			ID:    "kickoff-2",
			Title: "Project kickoff",
			Start: time.Date(2026, 4, 20, 15, 0, 0, 0, time.UTC),
			End:   time.Date(2026, 4, 20, 15, 45, 0, 0, time.UTC),
			Organizer: scheduling.Participant{
				Address: "alex@example.com",
				Name:    "Alex",
			},
			TimeZone: "America/New_York",
			Metadata: map[string]any{"source": "test"},
		},
	})
	if err != nil {
		t.Fatalf("CreateEvent() error = %v", err)
	}
	if created.Event.ID != "kickoff-2" {
		t.Fatalf("CreateEvent().Event.ID = %q, want kickoff-2", created.Event.ID)
	}
	cancelled, err := store.CancelEvent(context.Background(), scheduling.CancelRequest{
		ID:     "kickoff-2",
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

	server := newCalDAVTestServer()
	server.put(eventResource("kickoff-3", "Project kickoff", "Agenda", time.Date(2026, 4, 20, 15, 0, 0, 0, time.UTC), 45*time.Minute, "America/New_York"), "")
	server.failNextPutWithConflict("kickoff-3")
	server.onConflict = func(uid string) {
		server.put(eventResource(uid, "Project kickoff", "Agenda", time.Date(2026, 4, 20, 16, 0, 0, 0, time.UTC), 45*time.Minute, "America/New_York"), "")
	}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	client, err := caldavclient.New(httpServer.URL+"/calendar/", caldavclient.WithHTTPClient(httpServer.Client()))
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
		ID:       "kickoff-3",
		Start:    start,
		End:      end,
		TimeZone: "America/New_York",
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

func TestStoreRescheduleAbortsWhenConflictResolvesToDifferentHref(t *testing.T) {
	t.Parallel()

	server := newCalDAVTestServer()
	server.put(eventResource("kickoff-4", "Project kickoff", "Agenda", time.Date(2026, 4, 20, 15, 0, 0, 0, time.UTC), 45*time.Minute, "America/New_York"), "")
	server.failNextPutWithConflict("kickoff-4")
	server.onConflict = func(uid string) {
		server.replace(uid, testResource{
			uid:  uid,
			href: "/calendar/recreated-kickoff-4.ics",
			data: mustCalendarData(uid, "Project kickoff", "Agenda", time.Date(2026, 4, 20, 16, 0, 0, 0, time.UTC), 45*time.Minute, "America/New_York"),
			etag: nextETag(uid),
		})
	}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	client, err := caldavclient.New(httpServer.URL+"/calendar/", caldavclient.WithHTTPClient(httpServer.Client()))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	store, err := New(client)
	if err != nil {
		t.Fatalf("New store error = %v", err)
	}

	_, err = store.RescheduleEvent(context.Background(), scheduling.RescheduleRequest{
		ID:       "kickoff-4",
		Start:    time.Date(2026, 4, 20, 17, 0, 0, 0, time.UTC),
		End:      time.Date(2026, 4, 20, 17, 45, 0, 0, time.UTC),
		TimeZone: "America/New_York",
	})
	if err == nil || !strings.Contains(err.Error(), "event resource changed during retry") {
		t.Fatalf("RescheduleEvent() error = %v, want resource-changed retry error", err)
	}
}

type calDAVTestServer struct {
	mu         sync.Mutex
	resources  map[string]testResource
	conflicts  map[string]int
	onConflict func(uid string)
}

type testResource struct {
	uid  string
	href string
	etag string
	data string
}

func newCalDAVTestServer() *calDAVTestServer {
	return &calDAVTestServer{
		resources: make(map[string]testResource),
		conflicts: make(map[string]int),
	}
}

func (s *calDAVTestServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "REPORT":
		s.handleReport(w, r)
	case http.MethodPut:
		s.handlePut(w, r)
	case http.MethodGet:
		s.handleGet(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *calDAVTestServer) handleReport(w http.ResponseWriter, r *http.Request) {
	bodyBytes, _ := io.ReadAll(r.Body)
	body := string(bodyBytes)
	s.mu.Lock()
	defer s.mu.Unlock()

	uidFilter := extractBetween(body, "<c:text-match collation=\"i;octet\">", "</c:text-match>")
	summaryFilter := extractBetween(body, "<c:prop-filter name=\"SUMMARY\"><c:text-match collation=\"i;unicode-casemap\">", "</c:text-match></c:prop-filter>")
	startFilter := extractAttr(body, "start")
	endFilter := extractAttr(body, "end")
	var selected []testResource
	for _, resource := range s.resources {
		if uidFilter != "" && resource.uid != uidFilter {
			continue
		}
		if summaryFilter != "" && !resourceMatchesSummary(resource.data, summaryFilter) {
			continue
		}
		if !matchesResourceWindow(resource.data, startFilter, endFilter) {
			continue
		}
		selected = append(selected, resource)
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].uid < selected[j].uid })

	type prop struct {
		GetETag      string `xml:"getetag"`
		CalendarData string `xml:"calendar-data"`
	}
	type propstat struct {
		Status string `xml:"status"`
		Prop   prop   `xml:"prop"`
	}
	type response struct {
		Href     string     `xml:"href"`
		Propstat []propstat `xml:"propstat"`
	}
	type multistatus struct {
		XMLName   xml.Name   `xml:"multistatus"`
		Xmlns     string     `xml:"xmlns,attr"`
		XmlnsCal  string     `xml:"xmlns:c,attr"`
		Responses []response `xml:"response"`
	}
	reply := multistatus{
		Xmlns:    "DAV:",
		XmlnsCal: "urn:ietf:params:xml:ns:caldav",
	}
	for _, resource := range selected {
		reply.Responses = append(reply.Responses, response{
			Href: resource.href,
			Propstat: []propstat{{
				Status: "HTTP/1.1 200 OK",
				Prop: prop{
					GetETag:      resource.etag,
					CalendarData: resource.data,
				},
			}},
		})
	}
	payload, _ := xml.Marshal(reply)
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	_, _ = w.Write(payload)
}

func (s *calDAVTestServer) handlePut(w http.ResponseWriter, r *http.Request) {
	bodyBytes, _ := io.ReadAll(r.Body)
	event, err := caldavclient.ParseCalendarData(string(bodyBytes))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	uid := event.UID

	s.mu.Lock()
	callback := s.onConflict
	if s.conflicts[uid] > 0 {
		s.conflicts[uid]--
		s.mu.Unlock()
		if callback != nil {
			callback(uid)
		}
		w.WriteHeader(http.StatusPreconditionFailed)
		return
	}

	resource := testResource{
		uid:  uid,
		href: r.URL.Path,
		etag: nextETag(uid),
		data: string(bodyBytes),
	}
	s.resources[uid] = resource
	s.mu.Unlock()
	w.Header().Set("ETag", resource.etag)
	w.WriteHeader(http.StatusCreated)
}

func (s *calDAVTestServer) handleGet(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, resource := range s.resources {
		if resource.href == r.URL.Path {
			w.Header().Set("ETag", resource.etag)
			_, _ = w.Write([]byte(resource.data))
			return
		}
	}
	http.NotFound(w, r)
}

func (s *calDAVTestServer) put(resource testResource, etag string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if etag != "" {
		resource.etag = etag
	}
	if resource.etag == "" {
		resource.etag = nextETag(resource.uid)
	}
	s.resources[resource.uid] = resource
}

func (s *calDAVTestServer) replace(uid string, resource testResource) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resources[uid] = resource
}

func (s *calDAVTestServer) failNextPutWithConflict(uid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conflicts[uid]++
}

func eventResource(uid, summary, description string, start time.Time, duration time.Duration, tz string) testResource {
	data := mustCalendarData(uid, summary, description, start, duration, tz)
	return testResource{
		uid:  uid,
		href: "/calendar/" + uid + ".ics",
		data: data,
	}
}

func mustCalendarData(uid, summary, description string, start time.Time, duration time.Duration, tz string) string {
	data, err := caldavclient.FormatCalendarData(caldavclient.CalendarEvent{
		UID:         uid,
		Summary:     summary,
		Description: description,
		Start:       start,
		End:         start.Add(duration),
		TimeZone:    tz,
		Organizer: scheduling.Participant{
			Address: "alex@example.com",
			Name:    "Alex",
		},
	})
	if err != nil {
		panic(err)
	}
	return data
}

func matchesResourceWindow(data, startFilter, endFilter string) bool {
	event, err := caldavclient.ParseCalendarData(data)
	if err != nil {
		return false
	}
	var start time.Time
	var end time.Time
	if startFilter != "" {
		start, _ = time.Parse("20060102T150405Z", startFilter)
	}
	if endFilter != "" {
		end, _ = time.Parse("20060102T150405Z", endFilter)
	}
	return matchesWindow(event.Start.UTC(), event.End.UTC(), start, end)
}

func resourceMatchesSummary(data, summary string) bool {
	event, err := caldavclient.ParseCalendarData(data)
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(event.Summary), strings.ToLower(summary))
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

func extractBetween(body, start, end string) string {
	i := strings.Index(body, start)
	if i < 0 {
		return ""
	}
	i += len(start)
	j := strings.Index(body[i:], end)
	if j < 0 {
		return ""
	}
	return body[i : i+j]
}

func extractAttr(body, name string) string {
	needle := name + `="`
	i := strings.Index(body, needle)
	if i < 0 {
		return ""
	}
	i += len(needle)
	j := strings.IndexByte(body[i:], '"')
	if j < 0 {
		return ""
	}
	return body[i : i+j]
}

func nextETag(uid string) string {
	return `"` + uid + `-etag-` + time.Now().UTC().Format("150405.000000") + `"`
}
