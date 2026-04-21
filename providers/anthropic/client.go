package anthropic

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

const (
	defaultEndpoint = "https://api.anthropic.com/v1/messages"
	apiVersion      = "2023-06-01"
)

// Option configures a Client. Nil options are ignored.
type Option func(*Client)

// Client adapts the Anthropic Messages API to the SDK model.Client contract.
type Client struct {
	APIKey string
	Model  string
	// BaseURL is the Anthropic API service root. When set, requests are sent to
	// BaseURL + "/v1/messages".
	BaseURL string
	// Endpoint is the full Messages API endpoint. It is primarily useful for
	// tests, proxies, and gateways that do not follow the default path layout.
	Endpoint    string
	HTTPClient  *http.Client
	Timeout     time.Duration
	MaxTokens   int
	Temperature *float64
	TopP        *float64
	Effort      Effort
	Thinking    *ThinkingConfig
}

// Effort controls Anthropic output_config.effort for models that support it.
type Effort string

const (
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
	EffortXHigh  Effort = "xhigh"
	EffortMax    Effort = "max"
)

// ThinkingType controls Anthropic's thinking mode.
type ThinkingType string

const (
	ThinkingAdaptive ThinkingType = "adaptive"
	ThinkingEnabled  ThinkingType = "enabled"
	ThinkingDisabled ThinkingType = "disabled"
)

// ThinkingDisplay controls whether Anthropic returns thinking content blocks
// when a model supports summarized or omitted thinking display.
type ThinkingDisplay string

const (
	ThinkingDisplaySummarized ThinkingDisplay = "summarized"
	ThinkingDisplayOmitted    ThinkingDisplay = "omitted"
)

// ThinkingConfig maps to Anthropic's thinking request object. Newer Claude
// models prefer Type "adaptive" with output_config.effort; older models may
// use Type "enabled" with BudgetTokens.
type ThinkingConfig struct {
	Type         ThinkingType    `json:"type,omitempty"`
	BudgetTokens int             `json:"budget_tokens,omitempty"`
	Display      ThinkingDisplay `json:"display,omitempty"`
}

// OutputConfig maps to Anthropic's output_config request object.
type OutputConfig struct {
	Effort Effort `json:"effort,omitempty"`
}

// New creates a Messages API model client.
func New(apiKey string, modelName string, opts ...Option) *Client {
	client := &Client{APIKey: apiKey, Model: modelName}
	applyOptions(client, opts)
	return client
}

// NewFromEnv creates a client using ANTHROPIC_API_KEY and ANTHROPIC_BASE_URL.
// If modelName is empty, it uses ANTHROPIC_MODEL. Options passed to NewFromEnv
// take precedence over environment-derived endpoint settings.
func NewFromEnv(modelName string, opts ...Option) *Client {
	if modelName == "" {
		modelName = os.Getenv("ANTHROPIC_MODEL")
	}
	envOpts := make([]Option, 0, len(opts)+1)
	if baseURL := os.Getenv("ANTHROPIC_BASE_URL"); baseURL != "" {
		envOpts = append(envOpts, WithBaseURL(baseURL))
	}
	envOpts = append(envOpts, opts...)
	return New(os.Getenv("ANTHROPIC_API_KEY"), modelName, envOpts...)
}

// WithBaseURL sets the Anthropic API service root. Requests are sent to
// BaseURL + "/v1/messages". This matches Anthropic SDK conventions where the
// default base URL is "https://api.anthropic.com".
func WithBaseURL(baseURL string) Option {
	return func(c *Client) {
		c.BaseURL = baseURL
	}
}

// WithEndpoint sets the full Messages API endpoint. It is useful for tests,
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

// WithMaxTokens sets Anthropic's max_tokens request value.
func WithMaxTokens(tokens int) Option {
	return func(c *Client) {
		c.MaxTokens = tokens
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

// WithEffort sets Anthropic output_config.effort. It is omitted by default,
// which lets Anthropic apply the model default.
func WithEffort(effort Effort) Option {
	return func(c *Client) {
		c.Effort = effort
	}
}

// WithThinking sets Anthropic's thinking object. It is omitted by default.
// If Type is empty but BudgetTokens is set, the adapter treats the config as
// manual extended thinking and sends Type "enabled".
func WithThinking(thinking ThinkingConfig) Option {
	return func(c *Client) {
		c.Thinking = &thinking
	}
}

// WithAdaptiveThinking enables Anthropic adaptive thinking. Pair this with
// WithEffort to control thinking depth on models that support adaptive
// thinking.
func WithAdaptiveThinking() Option {
	return WithThinking(ThinkingConfig{Type: ThinkingAdaptive})
}

// WithExtendedThinkingBudget enables manual extended thinking with a token
// budget. Anthropic recommends adaptive thinking plus effort on newer models.
func WithExtendedThinkingBudget(tokens int) Option {
	return WithThinking(ThinkingConfig{Type: ThinkingEnabled, BudgetTokens: tokens})
}

func applyOptions(client *Client, opts []Option) {
	for _, opt := range opts {
		if opt != nil {
			opt(client)
		}
	}
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
		if cancel != nil {
			cancel()
		}
		return nil, fmt.Errorf("anthropic: send request: %w", err)
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
		return strings.TrimRight(c.BaseURL, "/") + "/v1/messages"
	}
	return defaultEndpoint
}

func (c *Client) requestBody(req model.Request) messagesRequest {
	return messagesRequest{
		Model:        c.Model,
		System:       joinSystem(req.SystemPrompt, req.AppendSystemPrompt),
		Messages:     mapMessages(req.Messages),
		Tools:        mapTools(req.Tools),
		MaxTokens:    maxTokens(c.MaxTokens),
		Stream:       true,
		Temperature:  c.Temperature,
		TopP:         c.TopP,
		OutputConfig: outputConfig(c.Effort),
		Thinking:     thinkingConfig(c.Thinking),
	}
}

func outputConfig(effort Effort) *OutputConfig {
	if effort == "" {
		return nil
	}
	return &OutputConfig{Effort: effort}
}

func thinkingConfig(thinking *ThinkingConfig) *ThinkingConfig {
	if thinking == nil {
		return nil
	}
	if thinking.Type == "" && thinking.BudgetTokens == 0 && thinking.Display == "" {
		return nil
	}
	if thinking.Type == "" && thinking.BudgetTokens > 0 {
		normalized := *thinking
		normalized.Type = ThinkingEnabled
		return &normalized
	}
	return thinking
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
	Model        string          `json:"model"`
	System       string          `json:"system,omitempty"`
	Messages     []message       `json:"messages"`
	Tools        []toolSpec      `json:"tools,omitempty"`
	MaxTokens    int             `json:"max_tokens"`
	Stream       bool            `json:"stream"`
	Temperature  *float64        `json:"temperature,omitempty"`
	TopP         *float64        `json:"top_p,omitempty"`
	OutputConfig *OutputConfig   `json:"output_config,omitempty"`
	Thinking     *ThinkingConfig `json:"thinking,omitempty"`
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
		case model.ContentProviderArtifact:
			if artifactBlock, ok := providerArtifactBlock(block, "anthropic"); ok {
				blocks = append(blocks, artifactBlock)
			}
		}
	}
	return blocks
}

func providerArtifactBlock(block model.ContentBlock, provider string) (contentBlock, bool) {
	artifact := block.ProviderArtifact
	if block.Type != model.ContentProviderArtifact || artifact == nil || artifact.Provider != provider || len(artifact.Data) == 0 {
		return nil, false
	}
	var out contentBlock
	if err := json.Unmarshal(artifact.Data, &out); err != nil || out["type"] == nil {
		return nil, false
	}
	return out, true
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
