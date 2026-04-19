package messagetools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/messaging"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

func TestNewToolsRequiresCapability(t *testing.T) {
	t.Parallel()

	if _, err := NewTools(Config{}); err == nil {
		t.Fatal("NewTools() returned nil error without configured capabilities")
	}
}

func TestSearchAndReadToolsAreProgressive(t *testing.T) {
	t.Parallel()

	store := messaging.NewThreadStore([]messaging.Thread{{
		ID:      "thread-1",
		Subject: "Project kickoff follow-up",
		Summary: "Follow-up thread with concise owner-based updates",
		Messages: []messaging.Message{{
			ID:        "thread-1-msg-1",
			ThreadID:  "thread-1",
			Subject:   "Project kickoff follow-up",
			Body:      "Please keep follow-ups concise and include owners and due dates.",
			Direction: messaging.DirectionInbound,
			Sender:    messaging.Participant{Name: "Alex", Address: "alex@example.com"},
			SentAt:    time.Now().UTC(),
		}},
	}})
	searchTool, err := NewSearchTool(Config{Searcher: store})
	if err != nil {
		t.Fatalf("NewSearchTool() error = %v", err)
	}
	readTool, err := NewReadTool(Config{Reader: store})
	if err != nil {
		t.Fatalf("NewReadTool() error = %v", err)
	}

	searchResult := runTool(t, searchTool, SearchToolName, map[string]any{
		"query": "owners due dates kickoff",
		"limit": 3,
	})
	if searchResult.IsError {
		t.Fatalf("search result = %#v", searchResult)
	}
	if !strings.Contains(searchResult.Content, "Project kickoff follow-up") {
		t.Fatalf("search content = %q, want subject", searchResult.Content)
	}
	if strings.Contains(searchResult.Content, "Please keep follow-ups concise") {
		t.Fatalf("search content leaked full message body: %q", searchResult.Content)
	}

	readResult := runTool(t, readTool, ReadToolName, map[string]any{"thread_id": "thread-1"})
	if readResult.IsError {
		t.Fatalf("read result = %#v", readResult)
	}
	if !strings.Contains(readResult.Content, "Please keep follow-ups concise and include owners and due dates.") {
		t.Fatalf("read content = %q, want full thread content", readResult.Content)
	}
}

func TestSendTool(t *testing.T) {
	t.Parallel()

	store := messaging.NewThreadStore([]messaging.Thread{{
		ID:      "thread-1",
		Subject: "Project kickoff follow-up",
		Messages: []messaging.Message{{
			ID:        "thread-1-msg-1",
			ThreadID:  "thread-1",
			Subject:   "Project kickoff follow-up",
			Body:      "Please keep follow-ups concise.",
			Direction: messaging.DirectionInbound,
			SentAt:    time.Now().UTC(),
		}},
	}})
	sendTool, err := NewSendTool(Config{Sender: store})
	if err != nil {
		t.Fatalf("NewSendTool() error = %v", err)
	}

	sendResult := runTool(t, sendTool, SendToolName, map[string]any{
		"thread_id": "thread-1",
		"body":      "Thanks. I'll send concise updates with owners and due dates.",
		"recipients": []map[string]any{
			{"name": "Alex", "address": "alex@example.com"},
		},
	})
	if sendResult.IsError {
		t.Fatalf("send result = %#v", sendResult)
	}
	if sendResult.Metadata["thread_id"] != "thread-1" {
		t.Fatalf("send metadata = %#v, want thread_id", sendResult.Metadata)
	}
}

func TestSearchToolPassesPortableFilters(t *testing.T) {
	t.Parallel()

	var got messaging.SearchRequest
	searchTool, err := NewSearchTool(Config{
		Searcher: messaging.SearcherFunc(func(ctx context.Context, req messaging.SearchRequest) ([]messaging.Thread, error) {
			got = req
			return nil, nil
		}),
	})
	if err != nil {
		t.Fatalf("NewSearchTool() error = %v", err)
	}

	unread := true
	searchResult := runTool(t, searchTool, SearchToolName, map[string]any{
		"query":     "travel",
		"mailboxes": []string{"inbox"},
		"from":      []string{"alex@example.com"},
		"since":     "2026-04-19T00:00:00Z",
		"until":     "2026-04-20T00:00:00Z",
		"unread":    unread,
	})
	if searchResult.IsError {
		t.Fatalf("search result = %#v", searchResult)
	}
	if len(got.Filter.Mailboxes) != 1 || got.Filter.Mailboxes[0] != "inbox" {
		t.Fatalf("Filter.Mailboxes = %#v", got.Filter.Mailboxes)
	}
	if len(got.Filter.From) != 1 || got.Filter.From[0] != "alex@example.com" {
		t.Fatalf("Filter.From = %#v", got.Filter.From)
	}
	if got.Filter.Unread == nil || *got.Filter.Unread != unread {
		t.Fatalf("Filter.Unread = %#v", got.Filter.Unread)
	}
	if got.Filter.Since.Format(time.RFC3339) != "2026-04-19T00:00:00Z" {
		t.Fatalf("Filter.Since = %s", got.Filter.Since.Format(time.RFC3339))
	}
	if got.Filter.Until.Format(time.RFC3339) != "2026-04-20T00:00:00Z" {
		t.Fatalf("Filter.Until = %s", got.Filter.Until.Format(time.RFC3339))
	}
}

func runTool(t *testing.T, toolImpl tool.Tool, name string, input map[string]any) model.ToolResult {
	t.Helper()
	registry := tool.NewRegistry(toolImpl)
	exec := tool.Executor{Registry: registry}

	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("Marshal(%s) error = %v", name, err)
	}
	results := exec.Run(context.Background(), []model.ToolUse{{
		ID:    name + "-1",
		Name:  name,
		Input: payload,
	}})
	var out []model.ToolResult
	for item := range results {
		out = append(out, item)
	}
	if len(out) != 1 {
		t.Fatalf("Run(%s) results = %d, want 1", name, len(out))
	}
	return out[0]
}
