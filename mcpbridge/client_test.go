package mcpbridge

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

func TestStdioClientDiscoversAndExecutesToolsThroughSDKExecutor(t *testing.T) {
	ctx := context.Background()
	cfg := testServerConfig("docs", false)
	client, err := NewStdioClient(ctx, cfg)
	if err != nil {
		t.Fatalf("NewStdioClient() error = %v", err)
	}
	defer client.Close()

	set, err := DiscoverTools(ctx, client, cfg)
	if err != nil {
		t.Fatalf("DiscoverTools() error = %v", err)
	}
	registry := tool.NewRegistry()
	if err := set.Register(registry); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	specs := registry.Specs()
	if len(specs) != 1 {
		t.Fatalf("registry specs = %d, want 1", len(specs))
	}
	spec := specs[0]
	if spec.Name != "mcp__docs__search" {
		t.Fatalf("tool name = %q", spec.Name)
	}
	if spec.ConcurrencySafe {
		t.Fatalf("tool unexpectedly marked concurrency safe")
	}

	executor := tool.Executor{Registry: registry}
	results := executor.Run(ctx, []model.ToolUse{{
		ID:    "call-1",
		Name:  "mcp__docs__search",
		Input: json.RawMessage(`{"query":"quota"}`),
	}})
	result := <-results
	if result.ToolUseID != "call-1" || result.Name != "mcp__docs__search" {
		t.Fatalf("result identity = (%q, %q)", result.ToolUseID, result.Name)
	}
	if result.IsError {
		t.Fatalf("result error = %s", result.Content)
	}
	if result.Content != "result for quota" {
		t.Fatalf("result content = %q", result.Content)
	}
	if result.Metadata["mcp_server"] != "docs" || result.Metadata["mcp_tool"] != "search" {
		t.Fatalf("metadata = %#v", result.Metadata)
	}
}

func TestDiscoverToolsHonorsParallelConfig(t *testing.T) {
	ctx := context.Background()
	cfg := testServerConfig("docs", true)
	client, err := NewStdioClient(ctx, cfg)
	if err != nil {
		t.Fatalf("NewStdioClient() error = %v", err)
	}
	defer client.Close()

	set, err := DiscoverTools(ctx, client, cfg)
	if err != nil {
		t.Fatalf("DiscoverTools() error = %v", err)
	}
	tools := set.Tools()
	if len(tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(tools))
	}
	if !tools[0].CanRunConcurrently(model.ToolUse{Name: "mcp__docs__search"}) {
		t.Fatalf("tool should be concurrency safe")
	}
}

func TestStdioClientPropagatesMCPToolErrors(t *testing.T) {
	ctx := context.Background()
	cfg := testServerConfig("docs", false)
	client, err := NewStdioClient(ctx, cfg)
	if err != nil {
		t.Fatalf("NewStdioClient() error = %v", err)
	}
	defer client.Close()

	set, err := DiscoverTools(ctx, client, cfg)
	if err != nil {
		t.Fatalf("DiscoverTools() error = %v", err)
	}
	registry := tool.NewRegistry()
	if err := set.Register(registry); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	executor := tool.Executor{Registry: registry}
	results := executor.Run(ctx, []model.ToolUse{{
		ID:    "call-1",
		Name:  "mcp__docs__search",
		Input: json.RawMessage(`{"query":"fail"}`),
	}})
	result := <-results
	if !result.IsError {
		t.Fatalf("result should be model-visible error: %#v", result)
	}
	if result.Content != "remote failure" {
		t.Fatalf("result content = %q", result.Content)
	}
}

func testServerConfig(name string, parallel bool) ServerConfig {
	return ServerConfig{
		Name:                      name,
		Command:                   os.Args[0],
		Args:                      []string{"-test.run=TestMCPBridgeStdioServerHelper", "--"},
		Env:                       map[string]string{"MEMAX_MCPBRIDGE_TEST_SERVER": "1"},
		SupportsParallelToolCalls: parallel,
	}
}

func TestMCPBridgeStdioServerHelper(t *testing.T) {
	if os.Getenv("MEMAX_MCPBRIDGE_TEST_SERVER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var req struct {
			ID     int64           `json:"id,omitempty"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params,omitempty"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}
		if req.ID == 0 {
			continue
		}
		switch req.Method {
		case "initialize":
			writeRPCResult(encoder, req.ID, map[string]any{
				"protocolVersion": defaultProtocolVersion,
				"capabilities":    map[string]any{},
				"serverInfo": map[string]any{
					"name":    "test-mcp",
					"version": "dev",
				},
			})
		case "tools/list":
			writeRPCResult(encoder, req.ID, map[string]any{
				"tools": []map[string]any{{
					"name":        "search",
					"description": "Search test docs.",
					"inputSchema": map[string]any{
						"type":     "object",
						"required": []string{"query"},
						"properties": map[string]any{
							"query": map[string]any{"type": "string"},
						},
					},
				}},
			})
		case "tools/call":
			var params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &params)
			query, _ := params.Arguments["query"].(string)
			if query == "fail" {
				writeRPCResult(encoder, req.ID, map[string]any{
					"isError": true,
					"content": []map[string]any{{
						"type": "text",
						"text": "remote failure",
					}},
				})
				continue
			}
			writeRPCResult(encoder, req.ID, map[string]any{
				"content": []map[string]any{{
					"type": "text",
					"text": fmt.Sprintf("result for %s", query),
				}},
			})
		default:
			_ = encoder.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"error": map[string]any{
					"code":    -32601,
					"message": "method not found",
				},
			})
		}
	}
	os.Exit(0)
}

func writeRPCResult(encoder *json.Encoder, id int64, result any) {
	_ = encoder.Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	})
}
