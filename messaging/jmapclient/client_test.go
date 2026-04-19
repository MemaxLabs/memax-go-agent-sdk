package jmapclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientQueryEmailsUsesCollapseThreadsAndTextFilter(t *testing.T) {
	t.Parallel()

	var seen struct {
		MethodCalls [][]json.RawMessage `json:"methodCalls"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Fatalf("Authorization = %q, want bearer token", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&seen); err != nil {
			t.Fatalf("Decode(request) error = %v", err)
		}
		_, _ = w.Write([]byte(`{"methodResponses":[["Email/query",{"accountId":"acc","ids":["email-2","email-1"]},"0"]]}`))
	}))
	defer server.Close()

	client, err := New(server.URL, "acc", WithBearerToken("secret"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ids, err := client.QueryEmails(context.Background(), QueryRequest{
		Text: "passport urgent",
		Filter: Filter{
			Mailboxes: []string{"inbox", "important"},
			From:      []string{"alex@example.com"},
			Since:     mustTime(t, "2026-04-19T00:00:00Z"),
			Until:     mustTime(t, "2026-04-20T00:00:00Z"),
			Unread:    boolPtr(true),
		},
		Limit:           5,
		CollapseThreads: true,
	})
	if err != nil {
		t.Fatalf("QueryEmails() error = %v", err)
	}
	if len(ids) != 2 || ids[0] != "email-2" || ids[1] != "email-1" {
		t.Fatalf("QueryEmails() ids = %#v", ids)
	}
	if len(seen.MethodCalls) != 1 {
		t.Fatalf("MethodCalls = %d, want 1", len(seen.MethodCalls))
	}
	if len(seen.MethodCalls[0]) != 3 {
		t.Fatalf("MethodCall[0] len = %d, want 3", len(seen.MethodCalls[0]))
	}
	var args map[string]any
	if err := json.Unmarshal(seen.MethodCalls[0][1], &args); err != nil {
		t.Fatalf("Unmarshal(args) error = %v", err)
	}
	if got := args["collapseThreads"]; got != true {
		t.Fatalf("collapseThreads = %#v, want true", got)
	}
	rawFilter, err := json.Marshal(args["filter"])
	if err != nil {
		t.Fatalf("Marshal(filter) error = %v", err)
	}
	filterText := string(rawFilter)
	for _, want := range []string{
		`"text":"passport urgent"`,
		`"inMailbox":"inbox"`,
		`"inMailbox":"important"`,
		`"from":"alex@example.com"`,
		`"after":"2026-04-19T00:00:00Z"`,
		`"before":"2026-04-20T00:00:00Z"`,
		`"notKeyword":"$seen"`,
	} {
		if !strings.Contains(filterText, want) {
			t.Fatalf("filter JSON = %s, want substring %s", filterText, want)
		}
	}
}

func TestClientGetEmailsFetchesBodyValues(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope responseEnvelope
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Fatalf("Decode(request) error = %v", err)
		}
		_, _ = w.Write([]byte(`{"methodResponses":[["Email/get",{"list":[{"id":"email-1","threadId":"thread-1","subject":"Trip","preview":"Bring your passport","receivedAt":"2026-04-19T09:00:00Z","from":[{"name":"Alex","email":"alex@example.com"}],"to":[{"name":"Sam","email":"sam@example.com"}],"textBody":[{"partId":"1"}],"bodyValues":{"1":{"value":"Bring your passport and boarding pass.","isTruncated":false}}}]},"0"]]}`))
	}))
	defer server.Close()

	client, err := New(server.URL, "acc")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	items, err := client.GetEmails(context.Background(), EmailGetRequest{
		IDs:                 []string{"email-1"},
		Properties:          []string{"id", "threadId", "subject", "preview", "receivedAt", "from", "to", "textBody", "bodyValues"},
		FetchTextBodyValues: true,
		MaxBodyValueBytes:   4096,
	})
	if err != nil {
		t.Fatalf("GetEmails() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("GetEmails() len = %d, want 1", len(items))
	}
	if got := items[0].BodyValues["1"].Value; !strings.Contains(got, "passport") {
		t.Fatalf("bodyValues[1] = %q, want decoded body text", got)
	}
}

func TestClientGetThreads(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"methodResponses":[["Thread/get",{"list":[{"id":"thread-1","emailIds":["email-1","email-2"]}]},"0"]]}`))
	}))
	defer server.Close()

	client, err := New(server.URL, "acc")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	threads, err := client.GetThreads(context.Background(), []string{"thread-1"})
	if err != nil {
		t.Fatalf("GetThreads() error = %v", err)
	}
	if len(threads) != 1 || threads[0].ID != "thread-1" {
		t.Fatalf("GetThreads() = %#v", threads)
	}
	if len(threads[0].EmailIDs) != 2 {
		t.Fatalf("EmailIDs = %#v, want two ids", threads[0].EmailIDs)
	}
}

func boolPtr(value bool) *bool {
	return &value
}

func mustTime(t *testing.T, raw string) time.Time {
	t.Helper()
	value, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t.Fatalf("time.Parse(%q) error = %v", raw, err)
	}
	return value
}
