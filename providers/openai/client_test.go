package openai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

func TestClientStreamsTextAndToolCalls(t *testing.T) {
	var gotRequest responsesRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&gotRequest); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","delta":"hello"}

data: {"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","arguments":""}}

data: {"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"path\""}

data: {"type":"response.function_call_arguments.delta","output_index":1,"delta":":\"README.md\"}"}

data: {"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"README.md\"}"}}

data: {"type":"response.completed"}

`))
	}))
	defer server.Close()

	client := &Client{
		APIKey:   "test-key",
		Model:    "test-model",
		Endpoint: server.URL,
	}
	stream, err := client.Stream(context.Background(), model.Request{
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
			{
				Name:        "read_file",
				Description: "Read file",
				InputSchema: map[string]any{
					"type": "object",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	defer stream.Close()

	first, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv text returned error: %v", err)
	}
	if first.Kind != model.StreamText || first.Text != "hello" {
		t.Fatalf("first event = %#v, want text hello", first)
	}

	second, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv tool returned error: %v", err)
	}
	if second.Kind != model.StreamToolUse || second.ToolUse.ID != "call_1" || second.ToolUse.Name != "read_file" {
		t.Fatalf("second event = %#v, want tool use", second)
	}
	if string(second.ToolUse.Input) != `{"path":"README.md"}` {
		t.Fatalf("tool input = %s", second.ToolUse.Input)
	}

	_, err = stream.Recv()
	if err != model.ErrEndOfStream {
		t.Fatalf("final Recv error = %v, want ErrEndOfStream", err)
	}

	if gotRequest.Model != "test-model" || !gotRequest.Stream || gotRequest.Store {
		t.Fatalf("request flags = %#v", gotRequest)
	}
	if gotRequest.Instructions != "system\n\nappend" {
		t.Fatalf("instructions = %q", gotRequest.Instructions)
	}
	if len(gotRequest.Tools) != 1 || gotRequest.Tools[0].Name != "read_file" {
		t.Fatalf("tools = %#v", gotRequest.Tools)
	}
}

func TestAPIErrorMarksContextWindowExceeded(t *testing.T) {
	err := &apiError{Code: "context_length_exceeded", Message: "maximum context length reached"}
	if !errors.Is(err, model.ErrContextWindowExceeded) {
		t.Fatalf("errors.Is(%v, ErrContextWindowExceeded) = false", err)
	}
}

func TestClientParsesSSEEventAndDataPairs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.output_text.delta\n"))
		_, _ = w.Write([]byte("data: {\"delta\":\"hello\"}\n\n"))
		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte("data: {}\n\n"))
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

	event, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv returned error: %v", err)
	}
	if event.Kind != model.StreamText || event.Text != "hello" {
		t.Fatalf("event = %#v, want text hello", event)
	}
	_, err = stream.Recv()
	if err != model.ErrEndOfStream {
		t.Fatalf("final Recv error = %v, want ErrEndOfStream", err)
	}
}

func TestClientStreamsUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"usage":{"input_tokens":3,"output_tokens":5,"total_tokens":8}}}

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

	event, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv returned error: %v", err)
	}
	if event.Kind != model.StreamUsage || event.Usage == nil {
		t.Fatalf("event = %#v, want usage", event)
	}
	if event.Usage.Provider != "openai" || event.Usage.Model != "test-model" || event.Usage.InputTokens != 3 || event.Usage.OutputTokens != 5 || event.Usage.TotalTokens != 8 {
		t.Fatalf("usage = %#v, want provider/model token counts", event.Usage)
	}
	_, err = stream.Recv()
	if err != model.ErrEndOfStream {
		t.Fatalf("final Recv error = %v, want ErrEndOfStream", err)
	}
}

func TestClientRequestOptions(t *testing.T) {
	temperature := 0.2
	topP := 0.9
	client := &Client{
		Model:           "test",
		Store:           true,
		MaxOutputTokens: 123,
		Temperature:     &temperature,
		TopP:            &topP,
	}

	body := client.requestBody(model.Request{})
	if !body.Store {
		t.Fatal("Store = false, want true")
	}
	if body.MaxOutputTokens == nil || *body.MaxOutputTokens != 123 {
		t.Fatalf("MaxOutputTokens = %#v, want 123", body.MaxOutputTokens)
	}
	if body.Temperature == nil || *body.Temperature != temperature {
		t.Fatalf("Temperature = %#v, want %v", body.Temperature, temperature)
	}
	if body.TopP == nil || *body.TopP != topP {
		t.Fatalf("TopP = %#v, want %v", body.TopP, topP)
	}
}

func TestClientMapsToolResultsToFunctionOutputs(t *testing.T) {
	client := &Client{Model: "test"}
	body := client.requestBody(model.Request{
		Messages: []model.Message{
			{
				Role: model.RoleAssistant,
				Content: []model.ContentBlock{
					{Type: model.ContentToolUse, ToolUse: &model.ToolUse{
						ID:    "call_1",
						Name:  "read_file",
						Input: json.RawMessage(`{"path":"README.md"}`),
					}},
				},
			},
			{
				Role: model.RoleTool,
				ToolResult: &model.ToolResult{
					ToolUseID: "call_1",
					Name:      "read_file",
					Content:   "contents",
				},
			},
		},
	})

	if len(body.Input) != 2 {
		t.Fatalf("len(input) = %d, want 2", len(body.Input))
	}
	if body.Input[0]["type"] != "function_call" || body.Input[0]["call_id"] != "call_1" {
		t.Fatalf("function call item = %#v", body.Input[0])
	}
	if body.Input[1]["type"] != "function_call_output" || body.Input[1]["output"] != "contents" {
		t.Fatalf("function output item = %#v", body.Input[1])
	}
}
