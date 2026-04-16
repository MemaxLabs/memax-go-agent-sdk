package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

const defaultEndpoint = "https://api.openai.com/v1/responses"

type Client struct {
	APIKey string
	Model  string
	// BaseURL is the provider API base URL. When set, requests are sent to
	// BaseURL + "/responses". Endpoint takes precedence over BaseURL.
	BaseURL string
	// Endpoint is the full Responses API endpoint. It is primarily useful for
	// tests, proxies, and gateways that do not follow the default path layout.
	Endpoint        string
	HTTPClient      *http.Client
	Store           bool
	MaxOutputTokens int
	Temperature     *float64
	TopP            *float64
}

// New creates a Responses API model client.
func New(apiKey string, modelName string) *Client {
	return &Client{APIKey: apiKey, Model: modelName}
}

// NewFromEnv creates a client using OPENAI_API_KEY and OPENAI_BASE_URL. If
// modelName is empty, it uses OPENAI_MODEL.
func NewFromEnv(modelName string) *Client {
	if modelName == "" {
		modelName = os.Getenv("OPENAI_MODEL")
	}
	client := New(os.Getenv("OPENAI_API_KEY"), modelName)
	client.BaseURL = os.Getenv("OPENAI_BASE_URL")
	return client
}

// Stream sends a streaming Responses API request and adapts text deltas and
// function calls to the SDK model stream contract.
func (c *Client) Stream(ctx context.Context, req model.Request) (model.Stream, error) {
	if c.APIKey == "" {
		return nil, fmt.Errorf("openai: API key is required")
	}
	if c.Model == "" {
		return nil, fmt.Errorf("openai: model is required")
	}

	body, err := json.Marshal(c.requestBody(req))
	if err != nil {
		return nil, fmt.Errorf("openai: encode request: %w", err)
	}
	endpoint := c.endpoint()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai: create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai: send request: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		return nil, decodeError(resp)
	}
	return newStream(resp.Body, c.Model), nil
}

func (c *Client) endpoint() string {
	if c.Endpoint != "" {
		return c.Endpoint
	}
	if c.BaseURL != "" {
		return strings.TrimRight(c.BaseURL, "/") + "/responses"
	}
	return defaultEndpoint
}

func (c *Client) requestBody(req model.Request) responsesRequest {
	return responsesRequest{
		Model:           c.Model,
		Instructions:    joinInstructions(req.SystemPrompt, req.AppendSystemPrompt),
		Input:           mapMessages(req.Messages),
		Tools:           mapTools(req.Tools),
		Stream:          true,
		Store:           c.Store,
		MaxOutputTokens: optionalInt(c.MaxOutputTokens),
		Temperature:     c.Temperature,
		TopP:            c.TopP,
	}
}

func optionalInt(v int) *int {
	if v <= 0 {
		return nil
	}
	return &v
}

func joinInstructions(parts ...string) string {
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

type responsesRequest struct {
	Model           string          `json:"model"`
	Instructions    string          `json:"instructions,omitempty"`
	Input           []responsesItem `json:"input"`
	Tools           []responsesTool `json:"tools,omitempty"`
	Stream          bool            `json:"stream"`
	Store           bool            `json:"store"`
	MaxOutputTokens *int            `json:"max_output_tokens,omitempty"`
	Temperature     *float64        `json:"temperature,omitempty"`
	TopP            *float64        `json:"top_p,omitempty"`
}

type responsesItem map[string]any

type responsesTool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

func mapTools(specs []model.ToolSpec) []responsesTool {
	if len(specs) == 0 {
		return nil
	}
	tools := make([]responsesTool, 0, len(specs))
	for _, spec := range specs {
		tools = append(tools, responsesTool{
			Type:        "function",
			Name:        spec.Name,
			Description: spec.Description,
			Parameters:  spec.InputSchema,
		})
	}
	return tools
}

func mapMessages(messages []model.Message) []responsesItem {
	items := make([]responsesItem, 0, len(messages))
	for _, msg := range messages {
		switch msg.Role {
		case model.RoleUser:
			items = append(items, responsesItem{
				"role":    "user",
				"content": msg.PlainText(),
			})
		case model.RoleAssistant:
			text := msg.PlainText()
			if text != "" {
				items = append(items, responsesItem{
					"role":    "assistant",
					"content": text,
				})
			}
			for _, block := range msg.Content {
				if block.ToolUse == nil {
					continue
				}
				items = append(items, responsesItem{
					"type":      "function_call",
					"call_id":   block.ToolUse.ID,
					"name":      block.ToolUse.Name,
					"arguments": string(block.ToolUse.Input),
				})
			}
		case model.RoleTool:
			if msg.ToolResult == nil {
				continue
			}
			items = append(items, responsesItem{
				"type":    "function_call_output",
				"call_id": msg.ToolResult.ToolUseID,
				"output":  msg.ToolResult.Content,
			})
		}
	}
	return items
}
