package anthropic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

func TestClientStreamsTextAndToolCalls(t *testing.T) {
	var gotRequest messagesRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-key" {
			t.Fatalf("x-api-key = %q", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") == "" {
			t.Fatal("missing anthropic-version header")
		}
		if err := json.NewDecoder(r.Body).Decode(&gotRequest); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"read_file","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\""}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":":\"README.md\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_stop
data: {"type":"message_stop"}

`))
	}))
	defer server.Close()

	temperature := 0.1
	stream, err := (&Client{
		APIKey:      "test-key",
		Model:       "test-model",
		Endpoint:    server.URL,
		MaxTokens:   123,
		Temperature: &temperature,
	}).Stream(context.Background(), model.Request{
		SystemPrompt:       "system",
		AppendSystemPrompt: "append",
		Messages: []model.Message{
			{
				Role: model.RoleUser,
				Content: []model.ContentBlock{
					{Type: model.ContentText, Text: "hi"},
				},
			},
		},
		Tools: []model.ToolSpec{
			{Name: "read_file", Description: "Read file", InputSchema: map[string]any{"type": "object"}},
		},
	})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	defer stream.Close()

	text, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv text returned error: %v", err)
	}
	if text.Kind != model.StreamText || text.Text != "hello" {
		t.Fatalf("text event = %#v, want hello", text)
	}

	toolUse, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv tool returned error: %v", err)
	}
	if toolUse.Kind != model.StreamToolUse || toolUse.ToolUse.ID != "toolu_1" || toolUse.ToolUse.Name != "read_file" {
		t.Fatalf("tool event = %#v", toolUse)
	}
	if string(toolUse.ToolUse.Input) != `{"path":"README.md"}` {
		t.Fatalf("tool input = %s", toolUse.ToolUse.Input)
	}

	_, err = stream.Recv()
	if err != model.ErrEndOfStream {
		t.Fatalf("final Recv error = %v, want ErrEndOfStream", err)
	}

	if gotRequest.Model != "test-model" || !gotRequest.Stream || gotRequest.MaxTokens != 123 {
		t.Fatalf("request = %#v", gotRequest)
	}
	if gotRequest.System != "system\n\nappend" {
		t.Fatalf("system = %q", gotRequest.System)
	}
	if len(gotRequest.Tools) != 1 || gotRequest.Tools[0].Name != "read_file" {
		t.Fatalf("tools = %#v", gotRequest.Tools)
	}
	if gotRequest.Temperature == nil || *gotRequest.Temperature != temperature {
		t.Fatalf("temperature = %#v", gotRequest.Temperature)
	}
}

func TestClientMapsToolResultsToToolResultBlocks(t *testing.T) {
	body := (&Client{Model: "test"}).requestBody(model.Request{
		Messages: []model.Message{
			{
				Role: model.RoleAssistant,
				Content: []model.ContentBlock{
					{Type: model.ContentToolUse, ToolUse: &model.ToolUse{
						ID:    "toolu_1",
						Name:  "read_file",
						Input: json.RawMessage(`{"path":"README.md"}`),
					}},
				},
			},
			{
				Role: model.RoleTool,
				ToolResult: &model.ToolResult{
					ToolUseID: "toolu_1",
					Name:      "read_file",
					Content:   "contents",
				},
			},
		},
	})

	if len(body.Messages) != 2 {
		t.Fatalf("len(messages) = %d, want 2", len(body.Messages))
	}
	if body.Messages[0].Content[0]["type"] != "tool_use" || body.Messages[0].Content[0]["id"] != "toolu_1" {
		t.Fatalf("assistant tool use = %#v", body.Messages[0].Content[0])
	}
	if body.Messages[1].Role != "user" || body.Messages[1].Content[0]["type"] != "tool_result" {
		t.Fatalf("tool result message = %#v", body.Messages[1])
	}
}
