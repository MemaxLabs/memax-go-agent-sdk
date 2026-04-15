package memaxagent

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

func TestQueryEventStreamGolden(t *testing.T) {
	registry := tool.NewRegistry(tool.Definition{
		ToolSpec: model.ToolSpec{Name: "read", ReadOnly: true, ConcurrencySafe: true},
		Handler: func(context.Context, tool.Call) (model.ToolResult, error) {
			return model.ToolResult{Content: "content from README.md"}, nil
		},
	})
	events, err := Query(context.Background(), "read the file", Options{
		Model: &fakeModel{turns: [][]model.StreamEvent{
			{
				{
					Kind: model.StreamToolUse,
					ToolUse: model.ToolUse{
						ID:    "tool-1",
						Name:  "read",
						Input: json.RawMessage(`{"path":"README.md"}`),
					},
				},
			},
			{{Kind: model.StreamText, Text: "done"}},
		}},
		Tools: registry,
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}

	var got []goldenEvent
	for event := range events {
		if event.Kind == EventError {
			t.Fatalf("query error: %v", event.Err)
		}
		got = append(got, normalizeGoldenEvent(event))
	}

	data, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("marshal golden events: %v", err)
	}
	data = append(data, '\n')

	want, err := os.ReadFile("testdata/golden/basic_event_stream.json")
	if err != nil {
		t.Fatalf("read golden file: %v", err)
	}
	if strings.TrimSpace(string(data)) != strings.TrimSpace(string(want)) {
		t.Fatalf("event stream golden mismatch\n got:\n%s\nwant:\n%s", data, want)
	}
}

type goldenEvent struct {
	Kind       EventKind `json:"kind"`
	Turn       int       `json:"turn,omitempty"`
	Text       string    `json:"text,omitempty"`
	ToolID     string    `json:"tool_id,omitempty"`
	ToolName   string    `json:"tool_name,omitempty"`
	ToolResult string    `json:"tool_result,omitempty"`
	Result     string    `json:"result,omitempty"`
}

func normalizeGoldenEvent(event Event) goldenEvent {
	out := goldenEvent{Kind: event.Kind, Turn: event.Turn}
	switch event.Kind {
	case EventAssistant:
		if event.Message != nil {
			out.Text = event.Message.PlainText()
		}
	case EventToolUse:
		if event.ToolUse != nil {
			out.ToolID = event.ToolUse.ID
			out.ToolName = event.ToolUse.Name
		}
	case EventToolResult:
		if event.ToolResult != nil {
			out.ToolID = event.ToolResult.ToolUseID
			out.ToolName = event.ToolResult.Name
			out.ToolResult = event.ToolResult.Content
		}
	case EventResult:
		out.Result = event.Result
	}
	return out
}
