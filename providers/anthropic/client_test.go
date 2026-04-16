package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

func TestClientStreamsUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":7}}}

event: message_delta
data: {"type":"message_delta","usage":{"output_tokens":5}}

event: message_delta
data: {"type":"message_delta","usage":{"output_tokens":11}}

event: message_stop
data: {"type":"message_stop"}

`))
	}))
	defer server.Close()

	stream, err := (&Client{
		APIKey:   "test-key",
		Model:    "test-model",
		Endpoint: server.URL,
	}).Stream(context.Background(), model.Request{})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	defer stream.Close()

	input, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv input usage returned error: %v", err)
	}
	if input.Kind != model.StreamUsage || input.Usage == nil || input.Usage.Provider != "anthropic" || input.Usage.Model != "test-model" || input.Usage.InputTokens != 7 {
		t.Fatalf("input usage event = %#v", input)
	}
	firstOutput, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv output usage returned error: %v", err)
	}
	if firstOutput.Kind != model.StreamUsage || firstOutput.Usage == nil || firstOutput.Usage.OutputTokens != 5 || firstOutput.Usage.TotalTokens != 5 {
		t.Fatalf("first output usage event = %#v", firstOutput)
	}
	secondOutput, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv second output usage returned error: %v", err)
	}
	if secondOutput.Kind != model.StreamUsage || secondOutput.Usage == nil || secondOutput.Usage.OutputTokens != 6 || secondOutput.Usage.TotalTokens != 6 {
		t.Fatalf("second output usage event = %#v", secondOutput)
	}
	_, err = stream.Recv()
	if err != model.ErrEndOfStream {
		t.Fatalf("final Recv error = %v, want ErrEndOfStream", err)
	}
}

func TestClientUsesBaseURL(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`event: message_stop
data: {"type":"message_stop"}

`))
	}))
	defer server.Close()

	stream, err := (&Client{
		APIKey:  "test-key",
		Model:   "test-model",
		BaseURL: server.URL + "/v1/",
	}).Stream(context.Background(), model.Request{})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	defer stream.Close()

	if gotPath != "/v1/messages" {
		t.Fatalf("path = %q, want /v1/messages", gotPath)
	}
}

func TestClientEndpointOverridesBaseURL(t *testing.T) {
	if got := (&Client{Endpoint: "https://endpoint.test/messages", BaseURL: "https://base.test/v1"}).endpoint(); got != "https://endpoint.test/messages" {
		t.Fatalf("endpoint = %q, want explicit endpoint", got)
	}
}

func TestNewFromEnvUsesBaseURL(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "env-key")
	t.Setenv("ANTHROPIC_MODEL", "env-model")
	t.Setenv("ANTHROPIC_BASE_URL", "https://gateway.test/v1")

	client := NewFromEnv("")
	if client.APIKey != "env-key" {
		t.Fatalf("APIKey = %q, want env-key", client.APIKey)
	}
	if client.Model != "env-model" {
		t.Fatalf("Model = %q, want env-model", client.Model)
	}
	if client.BaseURL != "https://gateway.test/v1" {
		t.Fatalf("BaseURL = %q, want env value", client.BaseURL)
	}
	if got := client.endpoint(); got != "https://gateway.test/v1/messages" {
		t.Fatalf("endpoint = %q, want gateway messages endpoint", got)
	}
}

func TestClientOptions(t *testing.T) {
	httpClient := &http.Client{}
	client := New("key", "model",
		WithBaseURL("https://gateway.test/v1"),
		WithEndpoint("https://endpoint.test/messages"),
		WithHTTPClient(httpClient),
		WithTimeout(3*time.Second),
		WithMaxTokens(123),
		WithTemperature(0.2),
		WithTopP(0.9),
		nil,
	)

	if client.APIKey != "key" || client.Model != "model" {
		t.Fatalf("client identity = %#v, want key/model", client)
	}
	if client.BaseURL != "https://gateway.test/v1" {
		t.Fatalf("BaseURL = %q, want gateway", client.BaseURL)
	}
	if client.Endpoint != "https://endpoint.test/messages" {
		t.Fatalf("Endpoint = %q, want endpoint", client.Endpoint)
	}
	if client.HTTPClient != httpClient {
		t.Fatalf("HTTPClient = %#v, want configured client", client.HTTPClient)
	}
	if client.Timeout != 3*time.Second {
		t.Fatalf("Timeout = %s, want 3s", client.Timeout)
	}
	if client.MaxTokens != 123 {
		t.Fatalf("MaxTokens = %d, want 123", client.MaxTokens)
	}
	if client.Temperature == nil || *client.Temperature != 0.2 {
		t.Fatalf("Temperature = %#v, want 0.2", client.Temperature)
	}
	if client.TopP == nil || *client.TopP != 0.9 {
		t.Fatalf("TopP = %#v, want 0.9", client.TopP)
	}
}

func TestNewFromEnvOptionsOverrideEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "env-key")
	t.Setenv("ANTHROPIC_MODEL", "env-model")
	t.Setenv("ANTHROPIC_BASE_URL", "https://env.test/v1")

	client := NewFromEnv("", WithBaseURL("https://option.test/v1"))
	if client.APIKey != "env-key" || client.Model != "env-model" {
		t.Fatalf("client = %#v, want env identity", client)
	}
	if client.BaseURL != "https://option.test/v1" {
		t.Fatalf("BaseURL = %q, want option override", client.BaseURL)
	}
}

func TestNewFromEnvIgnoresEmptyBaseURLEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "env-key")
	t.Setenv("ANTHROPIC_MODEL", "env-model")
	t.Setenv("ANTHROPIC_BASE_URL", "")

	client := NewFromEnv("")
	if client.BaseURL != "" {
		t.Fatalf("BaseURL = %q, want empty", client.BaseURL)
	}
}

func TestWithTimeout(t *testing.T) {
	httpClient := &http.Client{}
	client := New("key", "model", WithHTTPClient(httpClient), WithTimeout(2*time.Second))
	if client.HTTPClient != httpClient {
		t.Fatalf("HTTPClient = %#v, want configured client", client.HTTPClient)
	}
	if client.Timeout != 2*time.Second {
		t.Fatalf("timeout = %s, want 2s", client.Timeout)
	}
}

func TestWithTimeoutKeepsReturnedStreamReadable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}

event: message_stop
data: {"type":"message_stop"}

`))
	}))
	defer server.Close()

	stream, err := New("test-key", "test-model",
		WithEndpoint(server.URL),
		WithTimeout(time.Second),
	).Stream(context.Background(), model.Request{})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	defer stream.Close()

	event, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv returned error: %v", err)
	}
	if event.Kind != model.StreamText || event.Text != "hello" {
		t.Fatalf("event = %#v, want text hello", event)
	}
}

func TestAPIErrorMarksContextWindowExceeded(t *testing.T) {
	err := &apiError{Message: "prompt is too long"}
	if !errors.Is(err, model.ErrContextWindowExceeded) {
		t.Fatalf("errors.Is(%v, ErrContextWindowExceeded) = false", err)
	}
}

func TestAPIErrorDoesNotMarkUnrelatedContextType(t *testing.T) {
	err := &apiError{Type: "context_canceled", Message: "request canceled"}
	if errors.Is(err, model.ErrContextWindowExceeded) {
		t.Fatalf("errors.Is(%v, ErrContextWindowExceeded) = true, want false", err)
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

func TestClientMergesConsecutiveToolResults(t *testing.T) {
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
					{Type: model.ContentToolUse, ToolUse: &model.ToolUse{
						ID:    "toolu_2",
						Name:  "read_file",
						Input: json.RawMessage(`{"path":"AGENTS.md"}`),
					}},
				},
			},
			{
				Role: model.RoleTool,
				ToolResult: &model.ToolResult{
					ToolUseID: "toolu_1",
					Name:      "read_file",
					Content:   "readme",
				},
			},
			{
				Role: model.RoleTool,
				ToolResult: &model.ToolResult{
					ToolUseID: "toolu_2",
					Name:      "read_file",
					Content:   "agents",
				},
			},
		},
	})

	if len(body.Messages) != 2 {
		t.Fatalf("len(messages) = %d, want 2", len(body.Messages))
	}
	toolResults := body.Messages[1]
	if toolResults.Role != "user" {
		t.Fatalf("tool result role = %q, want user", toolResults.Role)
	}
	if len(toolResults.Content) != 2 {
		t.Fatalf("len(tool result content) = %d, want 2", len(toolResults.Content))
	}
	if toolResults.Content[0]["tool_use_id"] != "toolu_1" || toolResults.Content[1]["tool_use_id"] != "toolu_2" {
		t.Fatalf("tool result content = %#v", toolResults.Content)
	}
}
