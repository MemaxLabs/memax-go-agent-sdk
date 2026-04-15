package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

const (
	defaultEndpoint = "https://api.anthropic.com/v1/messages"
	apiVersion      = "2023-06-01"
)

// Client adapts the Anthropic Messages API to the SDK model.Client contract.
type Client struct {
	APIKey      string
	Model       string
	Endpoint    string
	HTTPClient  *http.Client
	MaxTokens   int
	Temperature *float64
	TopP        *float64
}

// New creates a Messages API model client.
func New(apiKey string, modelName string) *Client {
	return &Client{APIKey: apiKey, Model: modelName}
}

// NewFromEnv creates a client using ANTHROPIC_API_KEY.
func NewFromEnv(modelName string) *Client {
	return New(os.Getenv("ANTHROPIC_API_KEY"), modelName)
}

// Stream sends a streaming Messages API request and adapts text deltas and
// tool-use blocks to the SDK model stream contract.
func (c *Client) Stream(ctx context.Context, req model.Request) (model.Stream, error) {
	if c.APIKey == "" {
		return nil, fmt.Errorf("anthropic: API key is required")
	}
	if c.Model == "" {
		return nil, fmt.Errorf("anthropic: model is required")
	}

	body, err := json.Marshal(c.requestBody(req))
	if err != nil {
		return nil, fmt.Errorf("anthropic: encode request: %w", err)
	}
	endpoint := c.Endpoint
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("anthropic: create request: %w", err)
	}
	httpReq.Header.Set("x-api-key", c.APIKey)
	httpReq.Header.Set("anthropic-version", apiVersion)
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("accept", "text/event-stream")

	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: send request: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		return nil, decodeError(resp)
	}
	return newStream(resp.Body, c.Model), nil
}

func (c *Client) requestBody(req model.Request) messagesRequest {
	return messagesRequest{
		Model:       c.Model,
		System:      joinSystem(req.SystemPrompt, req.AppendSystemPrompt),
		Messages:    mapMessages(req.Messages),
		Tools:       mapTools(req.Tools),
		MaxTokens:   maxTokens(c.MaxTokens),
		Stream:      true,
		Temperature: c.Temperature,
		TopP:        c.TopP,
	}
}

func joinSystem(parts ...string) string {
	var out string
	for _, part := range parts {
		if part == "" {
			continue
		}
		if out != "" {
			out += "\n\n"
		}
		out += part
	}
	return out
}

func maxTokens(v int) int {
	if v > 0 {
		return v
	}
	return 4096
}

type messagesRequest struct {
	Model       string     `json:"model"`
	System      string     `json:"system,omitempty"`
	Messages    []message  `json:"messages"`
	Tools       []toolSpec `json:"tools,omitempty"`
	MaxTokens   int        `json:"max_tokens"`
	Stream      bool       `json:"stream"`
	Temperature *float64   `json:"temperature,omitempty"`
	TopP        *float64   `json:"top_p,omitempty"`
}

type message struct {
	Role    string         `json:"role"`
	Content []contentBlock `json:"content"`
}

type contentBlock map[string]any

type toolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
}

func mapTools(specs []model.ToolSpec) []toolSpec {
	if len(specs) == 0 {
		return nil
	}
	tools := make([]toolSpec, 0, len(specs))
	for _, spec := range specs {
		tools = append(tools, toolSpec{
			Name:        spec.Name,
			Description: spec.Description,
			InputSchema: spec.InputSchema,
		})
	}
	return tools
}

func mapMessages(messages []model.Message) []message {
	out := make([]message, 0, len(messages))
	for _, msg := range messages {
		switch msg.Role {
		case model.RoleUser:
			out = appendMessage(out, "user", []contentBlock{{"type": "text", "text": msg.PlainText()}})
		case model.RoleAssistant:
			blocks := mapAssistantBlocks(msg)
			if len(blocks) > 0 {
				out = appendMessage(out, "assistant", blocks)
			}
		case model.RoleTool:
			if msg.ToolResult == nil {
				continue
			}
			out = appendMessage(out, "user", []contentBlock{{
				"type":        "tool_result",
				"tool_use_id": msg.ToolResult.ToolUseID,
				"content":     msg.ToolResult.Content,
				"is_error":    msg.ToolResult.IsError,
			}})
		}
	}
	return out
}

func appendMessage(messages []message, role string, blocks []contentBlock) []message {
	if len(blocks) == 0 {
		return messages
	}
	last := len(messages) - 1
	if last >= 0 && messages[last].Role == role {
		messages[last].Content = append(messages[last].Content, blocks...)
		return messages
	}
	return append(messages, message{Role: role, Content: blocks})
}

func mapAssistantBlocks(msg model.Message) []contentBlock {
	var blocks []contentBlock
	for _, block := range msg.Content {
		switch block.Type {
		case model.ContentText:
			if block.Text != "" {
				blocks = append(blocks, contentBlock{"type": "text", "text": block.Text})
			}
		case model.ContentToolUse:
			if block.ToolUse != nil {
				blocks = append(blocks, contentBlock{
					"type":  "tool_use",
					"id":    block.ToolUse.ID,
					"name":  block.ToolUse.Name,
					"input": rawInputObject(block.ToolUse.Input),
				})
			}
		}
	}
	return blocks
}

func rawInputObject(input json.RawMessage) any {
	if len(input) == 0 {
		return map[string]any{}
	}
	var out any
	if err := json.Unmarshal(input, &out); err != nil {
		return map[string]any{}
	}
	return out
}
