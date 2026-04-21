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
	Reasoning       *ReasoningConfig
	Text            *TextConfig
	ServiceTier     ServiceTier
	Include         []string
}

// ReasoningEffort controls OpenAI reasoning token spend for models that
// support the Responses API reasoning parameter.
type ReasoningEffort string

const (
	// ReasoningEffortNone explicitly sends "none" for models that accept it.
	// Omit WithReasoningEffort to leave the provider default unchanged.
	ReasoningEffortNone    ReasoningEffort = "none"
	ReasoningEffortMinimal ReasoningEffort = "minimal"
	ReasoningEffortLow     ReasoningEffort = "low"
	ReasoningEffortMedium  ReasoningEffort = "medium"
	ReasoningEffortHigh    ReasoningEffort = "high"
	ReasoningEffortXHigh   ReasoningEffort = "xhigh"
)

// ReasoningConfig maps to the OpenAI Responses API reasoning object.
type ReasoningConfig struct {
	Effort ReasoningEffort `json:"effort,omitempty"`
}

// TextVerbosity controls OpenAI text verbosity for models that support it.
type TextVerbosity string

const (
	TextVerbosityLow    TextVerbosity = "low"
	TextVerbosityMedium TextVerbosity = "medium"
	TextVerbosityHigh   TextVerbosity = "high"
)

// TextConfig maps to the OpenAI Responses API text object.
type TextConfig struct {
	Verbosity TextVerbosity `json:"verbosity,omitempty"`
}

// ServiceTier controls OpenAI service_tier for deployments that support it.
type ServiceTier string

const (
	ServiceTierAuto     ServiceTier = "auto"
	ServiceTierDefault  ServiceTier = "default"
	ServiceTierFlex     ServiceTier = "flex"
	ServiceTierPriority ServiceTier = "priority"
	ServiceTierScale    ServiceTier = "scale"
)

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

// WithReasoning sets request-side OpenAI Responses API reasoning controls. It
// is omitted by default. OpenAI model families differ in accepted effort values
// and in compatibility with temperature/top_p, so hosts should choose values
// that match the configured model.
//
// This option controls model behavior only. Pair it with
// WithReasoningArtifacts when the host wants opaque encrypted reasoning state
// preserved across stateless turns.
func WithReasoning(reasoning ReasoningConfig) Option {
	return func(c *Client) {
		c.Reasoning = &reasoning
	}
}

// WithReasoningEffort sets reasoning.effort for OpenAI reasoning-capable
// models. Use ReasoningEffortNone only when you want to explicitly send
// "none"; omit this option to use the provider default.
func WithReasoningEffort(effort ReasoningEffort) Option {
	return WithReasoning(ReasoningConfig{Effort: effort})
}

// WithText sets the OpenAI Responses API text object. It is omitted by default.
func WithText(text TextConfig) Option {
	return func(c *Client) {
		c.Text = &text
	}
}

// WithTextVerbosity sets text.verbosity for OpenAI GPT-5-family models.
func WithTextVerbosity(verbosity TextVerbosity) Option {
	return WithText(TextConfig{Verbosity: verbosity})
}

// WithServiceTier sets OpenAI's service_tier request value.
func WithServiceTier(tier ServiceTier) Option {
	return func(c *Client) {
		c.ServiceTier = tier
	}
}

// WithReasoningArtifacts requests encrypted OpenAI reasoning items so the SDK
// can persist and replay them in stateless multi-turn conversations. Stored
// reasoning artifacts remain opaque provider transcript state and are not
// exposed as normal assistant text. It is most useful with WithStore(false),
// where the host, not OpenAI's response store, owns transcript continuity.
func WithReasoningArtifacts() Option {
	return func(c *Client) {
		c.Include = appendInclude(c.Include, "reasoning.encrypted_content")
	}
}

func includesReasoningArtifacts(include []string) bool {
	for _, value := range include {
		if value == "reasoning.encrypted_content" {
			return true
		}
	}
	return false
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
	return newStream(responseBody, c.Model, includesReasoningArtifacts(c.Include)), nil
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
		Reasoning:       c.Reasoning,
		Text:            c.Text,
		ServiceTier:     c.ServiceTier,
		Include:         append([]string(nil), c.Include...),
	}
}

func appendInclude(include []string, value string) []string {
	for _, existing := range include {
		if existing == value {
			return include
		}
	}
	return append(include, value)
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
	Model           string           `json:"model"`
	Instructions    string           `json:"instructions,omitempty"`
	Input           []responsesItem  `json:"input"`
	Tools           []responsesTool  `json:"tools,omitempty"`
	Stream          bool             `json:"stream"`
	Store           bool             `json:"store"`
	MaxOutputTokens *int             `json:"max_output_tokens,omitempty"`
	Temperature     *float64         `json:"temperature,omitempty"`
	TopP            *float64         `json:"top_p,omitempty"`
	Reasoning       *ReasoningConfig `json:"reasoning,omitempty"`
	Text            *TextConfig      `json:"text,omitempty"`
	ServiceTier     ServiceTier      `json:"service_tier,omitempty"`
	Include         []string         `json:"include,omitempty"`
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
			items = appendAssistantItems(items, msg.Content)
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

func appendAssistantItems(items []responsesItem, blocks []model.ContentBlock) []responsesItem {
	var text strings.Builder
	flushText := func() {
		if text.Len() == 0 {
			return
		}
		items = append(items, responsesItem{
			"role":    "assistant",
			"content": text.String(),
		})
		text.Reset()
	}
	for _, block := range blocks {
		switch block.Type {
		case model.ContentText:
			text.WriteString(block.Text)
		case model.ContentProviderArtifact:
			if item, ok := providerArtifactItem(block, "openai"); ok {
				flushText()
				items = append(items, item)
			}
		case model.ContentToolUse:
			if block.ToolUse == nil {
				continue
			}
			flushText()
			items = append(items, responsesItem{
				"type":      "function_call",
				"call_id":   block.ToolUse.ID,
				"name":      block.ToolUse.Name,
				"arguments": string(block.ToolUse.Input),
			})
		}
	}
	flushText()
	return items
}

func providerArtifactItem(block model.ContentBlock, provider string) (responsesItem, bool) {
	artifact := block.ProviderArtifact
	if block.Type != model.ContentProviderArtifact || artifact == nil || artifact.Provider != provider || len(artifact.Data) == 0 {
		return nil, false
	}
	var item responsesItem
	if err := json.Unmarshal(artifact.Data, &item); err != nil || item["type"] == nil {
		return nil, false
	}
	if item["type"] == "reasoning" {
		sanitized := responsesItem{"type": "reasoning"}
		if summary, ok := item["summary"]; ok {
			sanitized["summary"] = summary
		}
		if encrypted, ok := item["encrypted_content"]; ok {
			sanitized["encrypted_content"] = encrypted
		}
		return sanitized, true
	}
	return item, true
}
