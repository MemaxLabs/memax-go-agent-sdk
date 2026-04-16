package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

const defaultEndpoint = "https://api.openai.com/v1/responses"

// Option configures a Client. Nil options are ignored.
type Option func(*Client)

type Client struct {
	APIKey string
	Model  string
	// BaseURL is the provider API-version base URL. When selected, requests are
	// sent to BaseURL + "/responses". For OpenAI-compatible gateways, this is
	// usually the root URL plus "/v1".
	BaseURL string
	// Endpoint is the full Responses API endpoint. It is primarily useful for
	// tests, proxies, and gateways that do not follow the default path layout.
	Endpoint        string
	HTTPClient      *http.Client
	Timeout         time.Duration
	Store           bool
	MaxOutputTokens int
	Temperature     *float64
	TopP            *float64
}

// New creates a Responses API model client.
func New(apiKey string, modelName string, opts ...Option) *Client {
	client := &Client{APIKey: apiKey, Model: modelName}
	applyOptions(client, opts)
	return client
}

// NewFromEnv creates a client using OPENAI_API_KEY and OPENAI_BASE_URL. If
// modelName is empty, it uses OPENAI_MODEL. Options passed to NewFromEnv take
// precedence over environment-derived endpoint settings.
func NewFromEnv(modelName string, opts ...Option) *Client {
	if modelName == "" {
		modelName = os.Getenv("OPENAI_MODEL")
	}
	envOpts := make([]Option, 0, len(opts)+1)
	if baseURL := os.Getenv("OPENAI_BASE_URL"); baseURL != "" {
		envOpts = append(envOpts, WithBaseURL(baseURL))
	}
	envOpts = append(envOpts, opts...)
	return New(os.Getenv("OPENAI_API_KEY"), modelName, envOpts...)
}

// WithBaseURL sets the provider API-version base URL. Requests are sent to
// BaseURL + "/responses". For OpenAI-compatible gateways, this usually includes
// "/v1", for example "https://gateway.example.com/v1".
func WithBaseURL(baseURL string) Option {
	return func(c *Client) {
		c.BaseURL = baseURL
	}
}

// WithEndpoint sets the full Responses API endpoint. It is useful for tests,
// proxies, and gateways with custom paths.
func WithEndpoint(endpoint string) Option {
	return func(c *Client) {
		c.Endpoint = endpoint
	}
}

// WithHTTPClient sets the HTTP client used for provider requests.
func WithHTTPClient(client *http.Client) Option {
	return func(c *Client) {
		c.HTTPClient = client
	}
}

// WithTimeout sets a per-request timeout. It composes with WithHTTPClient.
func WithTimeout(timeout time.Duration) Option {
	return func(c *Client) {
		c.Timeout = timeout
	}
}

// WithStore controls the OpenAI Responses API store flag.
func WithStore(store bool) Option {
	return func(c *Client) {
		c.Store = store
	}
}

// WithMaxOutputTokens limits the model output token budget for the request.
func WithMaxOutputTokens(tokens int) Option {
	return func(c *Client) {
		c.MaxOutputTokens = tokens
	}
}

// WithTemperature sets the sampling temperature.
func WithTemperature(temperature float64) Option {
	return func(c *Client) {
		c.Temperature = &temperature
	}
}

// WithTopP sets nucleus sampling.
func WithTopP(topP float64) Option {
	return func(c *Client) {
		c.TopP = &topP
	}
}

func applyOptions(client *Client, opts []Option) {
	for _, opt := range opts {
		if opt != nil {
			opt(client)
		}
	}
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
	reqCtx := ctx
	var cancel context.CancelFunc
	if c.Timeout > 0 {
		reqCtx, cancel = context.WithTimeout(ctx, c.Timeout)
	}
	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		if cancel != nil {
			cancel()
		}
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
		if cancel != nil {
			cancel()
		}
		return nil, fmt.Errorf("openai: send request: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		if cancel != nil {
			defer cancel()
		}
		return nil, decodeError(resp)
	}
	responseBody := resp.Body
	if cancel != nil {
		responseBody = cancelReadCloser{ReadCloser: resp.Body, cancel: cancel}
	}
	return newStream(responseBody, c.Model), nil
}

type cancelReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c cancelReadCloser) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
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
