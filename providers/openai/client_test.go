package openai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

	start, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv tool start returned error: %v", err)
	}
	if start.Kind != model.StreamToolUseStart || start.ToolUse.ID != "call_1" || start.ToolUse.Name != "read_file" {
		t.Fatalf("start event = %#v, want tool-use start", start)
	}

	firstDelta, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv first tool delta returned error: %v", err)
	}
	if firstDelta.Kind != model.StreamToolUseDelta || firstDelta.ToolUseDelta != `{"path"` {
		t.Fatalf("first delta = %#v, want tool-use delta", firstDelta)
	}

	secondDelta, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv second tool delta returned error: %v", err)
	}
	if secondDelta.Kind != model.StreamToolUseDelta || secondDelta.ToolUseDelta != `:"README.md"}` {
		t.Fatalf("second delta = %#v, want tool-use delta", secondDelta)
	}

	second, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv complete tool returned error: %v", err)
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

func TestClientStreamsEmptyToolArgumentsAsObject(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"workspace_apply_patch","arguments":""}}

data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"workspace_apply_patch","arguments":""}}

data: {"type":"response.completed"}

`))
	}))
	defer server.Close()

	client := &Client{APIKey: "test-key", Model: "test-model", Endpoint: server.URL}
	stream, err := client.Stream(context.Background(), model.Request{
		Messages: []model.Message{{
			Role:    model.RoleUser,
			Content: []model.ContentBlock{{Type: model.ContentText, Text: "hi"}},
		}},
		Tools: []model.ToolSpec{{Name: "workspace_apply_patch", Description: "Apply patch"}},
	})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	defer stream.Close()

	start, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv tool start returned error: %v", err)
	}
	if start.Kind != model.StreamToolUseStart {
		t.Fatalf("first event = %#v, want tool-use start", start)
	}
	complete, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv complete tool returned error: %v", err)
	}
	if complete.Kind != model.StreamToolUse {
		t.Fatalf("second event = %#v, want tool use", complete)
	}
	if got := string(complete.ToolUse.Input); got != `{}` {
		t.Fatalf("tool input = %q, want {}", got)
	}
	if _, err := json.Marshal(complete.ToolUse); err != nil {
		t.Fatalf("Marshal complete tool use returned error: %v", err)
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

func TestClientStreamsReasoningArtifact(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"checked constraints"}],"encrypted_content":"opaque"}}

data: {"type":"response.output_text.delta","delta":"done"}

data: {"type":"response.completed"}

`))
	}))
	defer server.Close()

	stream, err := New("test-key", "test-model", WithEndpoint(server.URL), WithReasoningArtifacts()).Stream(context.Background(), model.Request{})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	defer stream.Close()

	first, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv artifact returned error: %v", err)
	}
	if first.Kind != model.StreamProviderArtifact || first.ProviderArtifact == nil {
		t.Fatalf("first event = %#v, want provider artifact", first)
	}
	if first.ProviderArtifact.Provider != "openai" || first.ProviderArtifact.Type != "reasoning" || first.ProviderArtifact.ID != "rs_1" {
		t.Fatalf("artifact = %#v", first.ProviderArtifact)
	}
	if !strings.Contains(string(first.ProviderArtifact.Data), `"encrypted_content":"opaque"`) {
		t.Fatalf("artifact data = %s", first.ProviderArtifact.Data)
	}

	second, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv text returned error: %v", err)
	}
	if second.Kind != model.StreamText || second.Text != "done" {
		t.Fatalf("second event = %#v, want text done", second)
	}
}

func TestClientSkipsReasoningArtifactWithoutInclude(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"checked constraints"}]}}

data: {"type":"response.output_text.delta","delta":"done"}

data: {"type":"response.completed"}

`))
	}))
	defer server.Close()

	stream, err := (&Client{APIKey: "test-key", Model: "test-model", Endpoint: server.URL}).Stream(context.Background(), model.Request{})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	defer stream.Close()

	first, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv returned error: %v", err)
	}
	if first.Kind != model.StreamText || first.Text != "done" {
		t.Fatalf("first event = %#v, want text done", first)
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

func TestClientUsesBaseURL(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.completed"}

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

	if gotPath != "/v1/responses" {
		t.Fatalf("path = %q, want /v1/responses", gotPath)
	}
}

func TestClientEndpointOverridesBaseURL(t *testing.T) {
	if got := (&Client{Endpoint: "https://endpoint.test/responses", BaseURL: "https://base.test/v1"}).endpoint(); got != "https://endpoint.test/responses" {
		t.Fatalf("endpoint = %q, want explicit endpoint", got)
	}
}

func TestNewFromEnvUsesBaseURL(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "env-key")
	t.Setenv("OPENAI_MODEL", "env-model")
	t.Setenv("OPENAI_BASE_URL", "https://gateway.test/v1")

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
	if got := client.endpoint(); got != "https://gateway.test/v1/responses" {
		t.Fatalf("endpoint = %q, want gateway responses endpoint", got)
	}
}

func TestClientOptions(t *testing.T) {
	httpClient := &http.Client{}
	client := New("key", "model",
		WithBaseURL("https://gateway.test/v1"),
		WithEndpoint("https://endpoint.test/responses"),
		WithHTTPClient(httpClient),
		WithTimeout(3*time.Second),
		WithStore(true),
		WithMaxOutputTokens(123),
		WithTemperature(0.2),
		WithTopP(0.9),
		WithReasoningEffort(ReasoningEffortHigh),
		WithTextVerbosity(TextVerbosityLow),
		WithServiceTier(ServiceTierPriority),
		nil,
	)

	if client.APIKey != "key" || client.Model != "model" {
		t.Fatalf("client identity = %#v, want key/model", client)
	}
	if client.BaseURL != "https://gateway.test/v1" {
		t.Fatalf("BaseURL = %q, want gateway", client.BaseURL)
	}
	if client.Endpoint != "https://endpoint.test/responses" {
		t.Fatalf("Endpoint = %q, want endpoint", client.Endpoint)
	}
	if client.HTTPClient != httpClient {
		t.Fatalf("HTTPClient = %#v, want configured client", client.HTTPClient)
	}
	if client.Timeout != 3*time.Second {
		t.Fatalf("Timeout = %s, want 3s", client.Timeout)
	}
	if !client.Store {
		t.Fatal("Store = false, want true")
	}
	if client.MaxOutputTokens != 123 {
		t.Fatalf("MaxOutputTokens = %d, want 123", client.MaxOutputTokens)
	}
	if client.Temperature == nil || *client.Temperature != 0.2 {
		t.Fatalf("Temperature = %#v, want 0.2", client.Temperature)
	}
	if client.TopP == nil || *client.TopP != 0.9 {
		t.Fatalf("TopP = %#v, want 0.9", client.TopP)
	}
	if client.Reasoning == nil || client.Reasoning.Effort != ReasoningEffortHigh {
		t.Fatalf("Reasoning = %#v, want high effort", client.Reasoning)
	}
	if client.Text == nil || client.Text.Verbosity != TextVerbosityLow {
		t.Fatalf("Text = %#v, want low verbosity", client.Text)
	}
	if client.ServiceTier != ServiceTierPriority {
		t.Fatalf("ServiceTier = %q, want priority", client.ServiceTier)
	}
}

func TestClientSerializesModelControlOptions(t *testing.T) {
	body := New("key", "gpt-5.4",
		WithReasoningEffort(ReasoningEffortXHigh),
		WithTextVerbosity(TextVerbosityHigh),
		WithServiceTier(ServiceTierPriority),
	).requestBody(model.Request{})

	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	if !strings.Contains(string(data), `"reasoning":{"effort":"xhigh"}`) {
		t.Fatalf("request missing reasoning effort: %s", data)
	}
	if !strings.Contains(string(data), `"text":{"verbosity":"high"}`) {
		t.Fatalf("request missing text verbosity: %s", data)
	}
	if !strings.Contains(string(data), `"service_tier":"priority"`) {
		t.Fatalf("request missing service tier: %s", data)
	}
}

func TestNewFromEnvOptionsOverrideEnv(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "env-key")
	t.Setenv("OPENAI_MODEL", "env-model")
	t.Setenv("OPENAI_BASE_URL", "https://env.test/v1")

	client := NewFromEnv("", WithBaseURL("https://option.test/v1"))
	if client.APIKey != "env-key" || client.Model != "env-model" {
		t.Fatalf("client = %#v, want env identity", client)
	}
	if client.BaseURL != "https://option.test/v1" {
		t.Fatalf("BaseURL = %q, want option override", client.BaseURL)
	}
}

func TestNewFromEnvIgnoresEmptyBaseURLEnv(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "env-key")
	t.Setenv("OPENAI_MODEL", "env-model")
	t.Setenv("OPENAI_BASE_URL", "")

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
		_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","delta":"hello"}

data: {"type":"response.completed"}

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
	WithReasoningArtifacts()(client)

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
	if len(body.Include) != 1 || body.Include[0] != "reasoning.encrypted_content" {
		t.Fatalf("Include = %#v, want reasoning.encrypted_content", body.Include)
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

func TestClientReplaysOpenAIProviderArtifactsOnly(t *testing.T) {
	body := (&Client{Model: "test"}).requestBody(model.Request{
		Messages: []model.Message{{
			Role: model.RoleAssistant,
			Content: []model.ContentBlock{
				{
					Type: model.ContentProviderArtifact,
					ProviderArtifact: &model.ProviderArtifact{
						Provider: "openai",
						Type:     "reasoning",
						ID:       "rs_1",
						Data:     json.RawMessage(`{"type":"reasoning","id":"rs_1","encrypted_content":"opaque","status":"completed","content":[{"type":"reasoning_text","text":"raw chain"}]}`),
					},
				},
				{Type: model.ContentText, Text: "after reasoning"},
				{
					Type: model.ContentProviderArtifact,
					ProviderArtifact: &model.ProviderArtifact{
						Provider: "anthropic",
						Type:     "thinking",
						Data:     json.RawMessage(`{"type":"thinking","signature":"sig"}`),
					},
				},
				{
					Type: model.ContentToolUse,
					ToolUse: &model.ToolUse{
						ID:    "call_1",
						Name:  "read_file",
						Input: json.RawMessage(`{"path":"README.md"}`),
					},
				},
			},
		}},
	})

	if len(body.Input) != 3 {
		t.Fatalf("len(input) = %d, want 3", len(body.Input))
	}
	if body.Input[0]["type"] != "reasoning" || body.Input[0]["encrypted_content"] != "opaque" {
		t.Fatalf("input item = %#v, want OpenAI reasoning artifact", body.Input[0])
	}
	if _, ok := body.Input[0]["id"]; ok {
		t.Fatalf("reasoning replay serialized id: %#v", body.Input[0])
	}
	if _, ok := body.Input[0]["content"]; ok {
		t.Fatalf("reasoning replay serialized raw content: %#v", body.Input[0])
	}
	if _, ok := body.Input[0]["status"]; ok {
		t.Fatalf("reasoning replay serialized non-allowlisted field: %#v", body.Input[0])
	}
	if body.Input[1]["role"] != "assistant" || body.Input[1]["content"] != "after reasoning" {
		t.Fatalf("assistant text item = %#v", body.Input[1])
	}
	if body.Input[2]["type"] != "function_call" || body.Input[2]["call_id"] != "call_1" {
		t.Fatalf("function call item = %#v", body.Input[2])
	}
}

func TestClientDoesNotSerializeMessageMetadata(t *testing.T) {
	body := (&Client{Model: "test"}).requestBody(model.Request{
		Messages: []model.Message{
			{
				Role:     model.RoleUser,
				Content:  []model.ContentBlock{{Type: model.ContentText, Text: "hello"}},
				Metadata: map[string]any{"context_summary": true},
			},
		},
	})
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	if strings.Contains(string(data), "context_summary") || strings.Contains(string(data), "metadata") {
		t.Fatalf("metadata leaked into provider payload: %s", data)
	}
}
