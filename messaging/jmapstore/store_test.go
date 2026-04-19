package jmapstore

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/messaging"
	"github.com/MemaxLabs/memax-go-agent-sdk/messaging/jmapclient"
)

func TestStoreSearchAndReadAreProgressive(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope struct {
			MethodCalls [][]json.RawMessage `json:"methodCalls"`
		}
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Fatalf("Decode(request) error = %v", err)
		}
		if len(envelope.MethodCalls) != 1 {
			t.Fatalf("MethodCalls = %d, want 1", len(envelope.MethodCalls))
		}
		var method string
		if err := json.Unmarshal(envelope.MethodCalls[0][0], &method); err != nil {
			t.Fatalf("Unmarshal(method) error = %v", err)
		}
		switch method {
		case "Email/query":
			_, _ = w.Write([]byte(`{"methodResponses":[["Email/query",{"accountId":"acc","ids":["email-2"]},"0"]]}`))
		case "Email/get":
			var args map[string]any
			if err := json.Unmarshal(envelope.MethodCalls[0][1], &args); err != nil {
				t.Fatalf("Unmarshal(args) error = %v", err)
			}
			ids, _ := args["ids"].([]any)
			if len(ids) == 1 && ids[0] == "email-2" {
				_, _ = w.Write([]byte(`{"methodResponses":[["Email/get",{"list":[{"id":"email-2","threadId":"thread-1","subject":"Travel plans","preview":"Bring your passport before departure","receivedAt":"2026-04-19T10:00:00Z","from":[{"name":"Alex","email":"alex@example.com"}],"to":[{"name":"Sam","email":"sam@example.com"}],"keywords":{"$seen":true}}]},"0"]]}`))
				return
			}
			_, _ = w.Write([]byte(`{"methodResponses":[["Email/get",{"list":[{"id":"email-1","threadId":"thread-1","subject":"Travel plans","preview":"Ticket attached","receivedAt":"2026-04-18T09:00:00Z","from":[{"name":"Sam","email":"sam@example.com"}],"to":[{"name":"Alex","email":"alex@example.com"}],"textBody":[{"partId":"1"}],"bodyValues":{"1":{"value":"Can you send the boarding pass?","isTruncated":false}}},{"id":"email-2","threadId":"thread-1","subject":"Travel plans","preview":"Bring your passport before departure","receivedAt":"2026-04-19T10:00:00Z","from":[{"name":"Alex","email":"alex@example.com"}],"to":[{"name":"Sam","email":"sam@example.com"}],"textBody":[{"partId":"1"}],"bodyValues":{"1":{"value":"Bring your passport and boarding pass before departure.","isTruncated":false}}}]},"0"]]}`))
		case "Thread/get":
			_, _ = w.Write([]byte(`{"methodResponses":[["Thread/get",{"list":[{"id":"thread-1","emailIds":["email-1","email-2"]}]},"0"]]}`))
		default:
			t.Fatalf("unexpected method %q", method)
		}
	}))
	defer server.Close()

	client, err := jmapclient.New(server.URL, "acc")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	store, err := New(client)
	if err != nil {
		t.Fatalf("New(store) error = %v", err)
	}

	items, err := store.SearchThreads(context.Background(), messaging.SearchRequest{
		Query: "passport departure",
		Limit: 3,
	})
	if err != nil {
		t.Fatalf("SearchThreads() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("SearchThreads() len = %d, want 1", len(items))
	}
	if got := items[0].Summary; !strings.Contains(got, "passport") {
		t.Fatalf("SearchThreads() summary = %q, want preview", got)
	}
	if len(items[0].Messages) != 0 {
		t.Fatalf("SearchThreads() leaked full messages: %#v", items[0].Messages)
	}

	thread, err := store.ReadThread(context.Background(), messaging.ReadRequest{ThreadID: "thread-1"})
	if err != nil {
		t.Fatalf("ReadThread() error = %v", err)
	}
	if len(thread.Messages) != 2 {
		t.Fatalf("ReadThread() messages = %d, want 2", len(thread.Messages))
	}
	if !strings.Contains(thread.Messages[1].Body, "boarding pass") {
		t.Fatalf("ReadThread() body = %q, want full decoded content", thread.Messages[1].Body)
	}
}

func TestStoreReadThreadByExactSubject(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope struct {
			MethodCalls [][]json.RawMessage `json:"methodCalls"`
		}
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Fatalf("Decode(request) error = %v", err)
		}
		var method string
		if err := json.Unmarshal(envelope.MethodCalls[0][0], &method); err != nil {
			t.Fatalf("Unmarshal(method) error = %v", err)
		}
		switch method {
		case "Email/query":
			_, _ = w.Write([]byte(`{"methodResponses":[["Email/query",{"ids":["email-3","email-2"]},"0"]]}`))
		case "Email/get":
			_, _ = w.Write([]byte(`{"methodResponses":[["Email/get",{"list":[{"id":"email-3","threadId":"thread-2","subject":"Project kickoff","preview":"Old thread","receivedAt":"2026-04-18T09:00:00Z"},{"id":"email-2","threadId":"thread-1","subject":"Project kickoff","preview":"Latest thread","receivedAt":"2026-04-19T09:00:00Z"}]},"0"]]}`))
		case "Thread/get":
			_, _ = w.Write([]byte(`{"methodResponses":[["Thread/get",{"list":[{"id":"thread-1","emailIds":["email-2"]}]},"0"]]}`))
		default:
			t.Fatalf("unexpected method %q", method)
		}
	}))
	defer server.Close()

	client, err := jmapclient.New(server.URL, "acc")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	store, err := New(client)
	if err != nil {
		t.Fatalf("New(store) error = %v", err)
	}

	thread, err := store.ReadThread(context.Background(), messaging.ReadRequest{Subject: "Project kickoff"})
	if err != nil {
		t.Fatalf("ReadThread() error = %v", err)
	}
	if thread.ID != "thread-1" {
		t.Fatalf("ReadThread() id = %q, want latest exact-subject thread", thread.ID)
	}
}

func TestStoreSearchThreadsPassesPortableFilter(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope struct {
			MethodCalls [][]json.RawMessage `json:"methodCalls"`
		}
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Fatalf("Decode(request) error = %v", err)
		}
		var args map[string]any
		if err := json.Unmarshal(envelope.MethodCalls[0][1], &args); err != nil {
			t.Fatalf("Unmarshal(args) error = %v", err)
		}
		rawFilter, err := json.Marshal(args["filter"])
		if err != nil {
			t.Fatalf("Marshal(filter) error = %v", err)
		}
		filterText := string(rawFilter)
		for _, want := range []string{
			`"inMailbox":"inbox"`,
			`"from":"alex@example.com"`,
			`"after":"2026-04-19T00:00:00Z"`,
			`"before":"2026-04-20T00:00:00Z"`,
			`"notKeyword":"$seen"`,
		} {
			if !strings.Contains(filterText, want) {
				t.Fatalf("filter JSON = %s, want substring %s", filterText, want)
			}
		}
		_, _ = w.Write([]byte(`{"methodResponses":[["Email/query",{"ids":[]},"0"]]}`))
	}))
	defer server.Close()

	client, err := jmapclient.New(server.URL, "acc")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	store, err := New(client)
	if err != nil {
		t.Fatalf("New(store) error = %v", err)
	}
	_, err = store.SearchThreads(context.Background(), messaging.SearchRequest{
		Query: "travel",
		Filter: messaging.SearchFilter{
			Mailboxes: []string{"inbox"},
			From:      []string{"alex@example.com"},
			Since:     mustTime(t, "2026-04-19T00:00:00Z"),
			Until:     mustTime(t, "2026-04-20T00:00:00Z"),
			Unread:    boolPtr(true),
		},
	})
	if err != nil {
		t.Fatalf("SearchThreads() error = %v", err)
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
