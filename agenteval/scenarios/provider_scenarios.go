package scenarios

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/agenteval"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/providers/anthropic"
	"github.com/MemaxLabs/memax-go-agent-sdk/providers/openai"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

const maxCapturedProviderRequestBytes = 1 << 20

// OpenAIProviderTextAndUsage returns a single-use scenario that exercises the
// OpenAI adapter through Query with deterministic SSE text and usage events.
func OpenAIProviderTextAndUsage() agenteval.Case {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "missing authorization", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","delta":"hello from openai"}

data: {"type":"response.completed","response":{"usage":{"input_tokens":3,"output_tokens":5,"total_tokens":8}}}

`))
	}))

	return agenteval.Case{
		Name:   "openai_provider_text_and_usage",
		Prompt: "Say hello.",
		Options: memaxagent.Options{
			Model: openai.New("test-key", "test-openai", openai.WithEndpoint(server.URL)),
		},
		Assertions: []agenteval.Assertion{
			agenteval.FinalEquals("hello from openai"),
			agenteval.EventKindEmitted(memaxagent.EventUsage),
			usageEquals("openai", "test-openai", 3, 5, 8),
		},
		Cleanup: server.Close,
	}
}

// AnthropicProviderTextAndUsage returns a single-use scenario that exercises
// the Anthropic adapter through Query with deterministic SSE text and usage
// deltas.
func AnthropicProviderTextAndUsage() agenteval.Case {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("x-api-key") != "test-key" {
			http.Error(w, "missing api key", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":4}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello from anthropic"}}

event: message_delta
data: {"type":"message_delta","usage":{"output_tokens":7}}

event: message_stop
data: {"type":"message_stop"}

`))
	}))

	return agenteval.Case{
		Name:   "anthropic_provider_text_and_usage",
		Prompt: "Say hello.",
		Options: memaxagent.Options{
			Model: anthropic.New("test-key", "test-anthropic", anthropic.WithEndpoint(server.URL)),
		},
		Assertions: []agenteval.Assertion{
			agenteval.FinalEquals("hello from anthropic"),
			agenteval.EventKindEmitted(memaxagent.EventUsage),
			usageEquals("anthropic", "test-anthropic", 4, 7, 11),
		},
		Cleanup: server.Close,
	}
}

// OpenAIProviderToolUseRoundTrip returns a single-use scenario where the
// OpenAI adapter emits a function call, the SDK executes a real tool, and the
// follow-up provider request includes the tool result.
func OpenAIProviderToolUseRoundTrip() agenteval.Case {
	server := newOpenAIToolServer()
	return agenteval.Case{
		Name:   "openai_provider_tool_use_round_trip",
		Prompt: "Echo through a provider tool call.",
		Options: memaxagent.Options{
			Model: openai.New("test-key", "test-openai", openai.WithEndpoint(server.URL())),
			Tools: tool.NewRegistry(providerEchoTool()),
		},
		Assertions: []agenteval.Assertion{
			agenteval.ToolUsed("echo"),
			agenteval.NoToolErrors(),
			agenteval.FinalEquals("openai saw tool result"),
			server.RequestCountAssertion(2),
			server.BodyContainsAssertion("follow-up included tool result", "echo: from provider"),
		},
		Cleanup: server.Close,
	}
}

// AnthropicProviderToolUseRoundTrip returns a single-use scenario where the
// Anthropic adapter emits a tool_use block, the SDK executes a real tool, and
// the follow-up provider request includes the tool result.
func AnthropicProviderToolUseRoundTrip() agenteval.Case {
	server := newAnthropicToolServer()
	return agenteval.Case{
		Name:   "anthropic_provider_tool_use_round_trip",
		Prompt: "Echo through a provider tool call.",
		Options: memaxagent.Options{
			Model: anthropic.New("test-key", "test-anthropic", anthropic.WithEndpoint(server.URL())),
			Tools: tool.NewRegistry(providerEchoTool()),
		},
		Assertions: []agenteval.Assertion{
			agenteval.ToolUsed("echo"),
			agenteval.NoToolErrors(),
			agenteval.FinalEquals("anthropic saw tool result"),
			server.RequestCountAssertion(2),
			server.BodyContainsAssertion("follow-up included tool result", "echo: from provider"),
		},
		Cleanup: server.Close,
	}
}

func usageEquals(provider string, modelName string, input int, output int, total int) agenteval.Assertion {
	return agenteval.Assertion{
		Name: "usage equals",
		Check: func(result agenteval.Result) error {
			if result.Usage.Provider != provider ||
				result.Usage.Model != modelName ||
				result.Usage.InputTokens != input ||
				result.Usage.OutputTokens != output ||
				result.Usage.TotalTokens != total {
				return fmt.Errorf("usage = %#v, want provider=%q model=%q input=%d output=%d total=%d", result.Usage, provider, modelName, input, output, total)
			}
			return nil
		},
	}
}

type providerToolServer struct {
	server *httptest.Server
	mu     sync.Mutex
	bodies []string
}

func newOpenAIToolServer() *providerToolServer {
	s := &providerToolServer{}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "missing authorization", http.StatusUnauthorized)
			return
		}
		n := s.capture(r)
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			_, _ = w.Write([]byte(`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"echo","arguments":""}}

data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"message\""}

data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":":\"from provider\"}"}

data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"echo","arguments":"{\"message\":\"from provider\"}"}}

data: {"type":"response.completed"}

`))
			return
		}
		_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","delta":"openai saw tool result"}

data: {"type":"response.completed"}

`))
	}))
	return s
}

func newAnthropicToolServer() *providerToolServer {
	s := &providerToolServer{}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-key" {
			http.Error(w, "missing api key", http.StatusUnauthorized)
			return
		}
		n := s.capture(r)
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			_, _ = w.Write([]byte(`event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"echo","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"message\""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":":\"from provider\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_stop
data: {"type":"message_stop"}

`))
			return
		}
		_, _ = w.Write([]byte(`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"anthropic saw tool result"}}

event: message_stop
data: {"type":"message_stop"}

`))
	}))
	return s
}

func (s *providerToolServer) capture(r *http.Request) int {
	body, _ := io.ReadAll(io.LimitReader(r.Body, maxCapturedProviderRequestBytes))
	_ = r.Body.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bodies = append(s.bodies, string(body))
	return len(s.bodies)
}

func (s *providerToolServer) URL() string {
	return s.server.URL
}

func (s *providerToolServer) Close() {
	s.server.Close()
}

func (s *providerToolServer) RequestCountAssertion(want int) agenteval.Assertion {
	return agenteval.Assertion{
		Name: "provider request count",
		Check: func(agenteval.Result) error {
			got := len(s.Bodies())
			if got != want {
				return fmt.Errorf("provider request count = %d, want %d", got, want)
			}
			return nil
		},
	}
}

func (s *providerToolServer) BodyContainsAssertion(name string, substring string) agenteval.Assertion {
	return agenteval.Assertion{
		Name: name,
		Check: func(agenteval.Result) error {
			bodies := s.Bodies()
			if len(bodies) < 2 {
				return fmt.Errorf("provider request count = %d, want at least 2", len(bodies))
			}
			if strings.Contains(bodies[1], substring) {
				return nil
			}
			return fmt.Errorf("second provider request body did not contain %q", substring)
		},
	}
}

func (s *providerToolServer) Bodies() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.bodies))
	copy(out, s.bodies)
	return out
}

func providerEchoTool() tool.Tool {
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:            "echo",
			Description:     "Echo a message.",
			ReadOnly:        true,
			ConcurrencySafe: true,
			InputSchema: map[string]any{
				"type":                 "object",
				"required":             []any{"message"},
				"additionalProperties": false,
				"properties": map[string]any{
					"message": map[string]any{"type": "string"},
				},
			},
		},
		Handler: func(_ context.Context, call tool.Call) (model.ToolResult, error) {
			var input struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal(call.Use.Input, &input); err != nil {
				return model.ToolResult{}, err
			}
			return model.ToolResult{Content: "echo: " + input.Message}, nil
		},
	}
}
