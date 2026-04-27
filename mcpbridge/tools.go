package mcpbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

// ToolSet is a discovered collection of MCP tools from one server.
type ToolSet struct {
	serverName string
	client     *Client
	tools      []tool.Tool
}

// DiscoverTools lists tools from an initialized MCP server and adapts them into
// the SDK tool interface.
func DiscoverTools(ctx context.Context, client *Client, cfg ServerConfig) (ToolSet, error) {
	if client == nil {
		return ToolSet{}, fmt.Errorf("mcp client is required")
	}
	var result listToolsResult
	listCtx, cancel := contextWithOptionalTimeout(ctx, cfg.startupTimeout())
	defer cancel()
	if err := client.call(listCtx, "tools/list", map[string]any{}, &result); err != nil {
		return ToolSet{}, fmt.Errorf("list mcp tools for %s: %w", cfg.Name, err)
	}
	serverName := normalizeName(cfg.Name)
	out := make([]tool.Tool, 0, len(result.Tools))
	seen := map[string]struct{}{}
	for _, remote := range result.Tools {
		remoteName := strings.TrimSpace(remote.Name)
		localName := modelFacingToolName(serverName, remoteName)
		if localName == "" {
			return ToolSet{}, fmt.Errorf("mcp server %s returned tool with invalid name %q", cfg.Name, remote.Name)
		}
		if _, ok := seen[localName]; ok {
			return ToolSet{}, fmt.Errorf("mcp server %s returned duplicate local tool name %q", cfg.Name, localName)
		}
		seen[localName] = struct{}{}
		out = append(out, &remoteTool{
			serverName:  serverName,
			remoteName:  remoteName,
			localName:   localName,
			description: strings.TrimSpace(remote.Description),
			inputSchema: cloneSchema(remote.InputSchema),
			readOnly:    boolAnnotation(remote.Annotations.ReadOnlyHint),
			destructive: boolAnnotation(remote.Annotations.DestructiveHint),
			concurrent:  cfg.SupportsParallelToolCalls,
			toolTimeout: cfg.toolTimeout(),
			maxResult:   cfg.maxResultBytes(),
			client:      client,
		})
	}
	return ToolSet{serverName: serverName, client: client, tools: out}, nil
}

// Tools returns a snapshot of discovered SDK tools.
func (s ToolSet) Tools() []tool.Tool {
	return append([]tool.Tool(nil), s.tools...)
}

// Register adds every discovered MCP tool to registry.
func (s ToolSet) Register(registry *tool.Registry) error {
	if registry == nil {
		return fmt.Errorf("registry is required")
	}
	for _, t := range s.tools {
		if err := registry.Register(t); err != nil {
			return err
		}
	}
	return nil
}

// Close closes the backing MCP client.
func (s ToolSet) Close() error {
	return s.client.Close()
}

type remoteTool struct {
	serverName  string
	remoteName  string
	localName   string
	description string
	inputSchema map[string]any
	readOnly    bool
	destructive bool
	concurrent  bool
	toolTimeout time.Duration
	maxResult   int
	client      *Client
}

func (t *remoteTool) Spec() model.ToolSpec {
	description := t.description
	if description == "" {
		description = fmt.Sprintf("Call MCP tool %s on server %s.", t.remoteName, t.serverName)
	}
	return model.ToolSpec{
		Name:            t.localName,
		Description:     description,
		InputSchema:     cloneSchema(t.inputSchema),
		SearchHint:      "mcp " + t.serverName + " " + t.remoteName,
		ReadOnly:        t.readOnly,
		ConcurrencySafe: t.concurrent,
		Destructive:     t.destructive,
		MaxResultBytes:  t.maxResult,
	}
}

func (t *remoteTool) Execute(ctx context.Context, call tool.Call) (model.ToolResult, error) {
	var arguments map[string]any
	if len(call.Use.Input) > 0 {
		if err := json.Unmarshal(call.Use.Input, &arguments); err != nil {
			return model.ToolResult{}, fmt.Errorf("decode mcp tool arguments: %w", err)
		}
	}
	if arguments == nil {
		arguments = map[string]any{}
	}
	var result callToolResult
	callCtx, cancel := contextWithOptionalTimeout(ctx, t.toolTimeout)
	defer cancel()
	if err := t.client.call(callCtx, "tools/call", callToolParams{
		Name:      t.remoteName,
		Arguments: arguments,
	}, &result); err != nil {
		return model.ToolResult{}, fmt.Errorf("call mcp tool %s on %s: %w", t.remoteName, t.serverName, err)
	}
	return model.ToolResult{
		Content: renderMCPContent(result.Content),
		IsError: result.IsError,
		Metadata: map[string]any{
			"mcp_server":        t.serverName,
			"mcp_tool":          t.remoteName,
			"mcp_content_items": len(result.Content),
		},
	}, nil
}

func (t *remoteTool) CanRunConcurrently(_ model.ToolUse) bool {
	return t.concurrent
}

type listToolsResult struct {
	Tools []listedTool `json:"tools"`
}

type listedTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema map[string]any  `json:"inputSchema,omitempty"`
	Annotations toolAnnotations `json:"annotations,omitempty"`
}

type toolAnnotations struct {
	ReadOnlyHint    *bool `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool `json:"destructiveHint,omitempty"`
	IdempotentHint  *bool `json:"idempotentHint,omitempty"`
}

type callToolParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

type callToolResult struct {
	Content []contentItem `json:"content,omitempty"`
	IsError bool          `json:"isError,omitempty"`
}

type contentItem struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	Data     string          `json:"data,omitempty"`
	MimeType string          `json:"mimeType,omitempty"`
	URI      string          `json:"uri,omitempty"`
	Raw      json.RawMessage `json:"-"`
}

func renderMCPContent(items []contentItem) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	for i, item := range items {
		if i > 0 {
			b.WriteString("\n\n")
		}
		switch strings.TrimSpace(item.Type) {
		case "text", "":
			b.WriteString(item.Text)
		case "image":
			if item.MimeType != "" {
				fmt.Fprintf(&b, "[image %s", item.MimeType)
			} else {
				b.WriteString("[image")
			}
			if item.Data != "" {
				fmt.Fprintf(&b, " base64_bytes=%d", len(item.Data))
			}
			b.WriteString("]")
		case "resource":
			if item.URI != "" {
				fmt.Fprintf(&b, "[resource %s]", item.URI)
			} else {
				b.WriteString("[resource]")
			}
		default:
			encoded, _ := json.Marshal(item)
			b.Write(encoded)
		}
	}
	return b.String()
}

func boolAnnotation(value *bool) bool {
	return value != nil && *value
}

func cloneSchema(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	encoded, err := json.Marshal(in)
	if err != nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(encoded, &out); err != nil {
		return nil
	}
	return out
}
