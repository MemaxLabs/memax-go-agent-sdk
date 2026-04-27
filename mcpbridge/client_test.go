package mcpbridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
	if result.Metadata["mcp_content_items"] != 1 {
		t.Fatalf("content item metadata = %#v", result.Metadata)
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

func TestDiscoverToolsAppliesDefaultResultLimit(t *testing.T) {
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
	tools := set.Tools()
	if len(tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(tools))
	}
	if got := tools[0].Spec().MaxResultBytes; got != defaultMaxResultBytes {
		t.Fatalf("MaxResultBytes = %d, want %d", got, defaultMaxResultBytes)
	}
}

func TestMCPToolCallTimeoutIsModelVisible(t *testing.T) {
	ctx := context.Background()
	cfg := testServerConfig("docs", false)
	cfg.ToolTimeout = 10 * time.Millisecond
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
		Input: json.RawMessage(`{"query":"slow"}`),
	}})
	result := <-results
	if !result.IsError {
		t.Fatalf("timeout should be model-visible error: %#v", result)
	}
}

func TestJSONRPCConnCloseRunsCloseFuncAfterReadLoopEOF(t *testing.T) {
	reader, writer := io.Pipe()
	var closeCalls atomic.Int32
	conn := newJSONRPCConn(reader, nopWriteCloser{}, func() error {
		closeCalls.Add(1)
		return nil
	})
	_ = writer.Close()
	select {
	case <-conn.done:
	case <-time.After(time.Second):
		t.Fatal("read loop did not observe EOF")
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if got := closeCalls.Load(); got != 1 {
		t.Fatalf("closeFunc calls = %d, want 1", got)
	}
}

func TestJSONRPCConnReadLoopEOFRunsCloseFuncWithoutExplicitClose(t *testing.T) {
	reader, writer := io.Pipe()
	var closeCalls atomic.Int32
	conn := newJSONRPCConn(reader, nopWriteCloser{}, func() error {
		closeCalls.Add(1)
		return nil
	})
	_ = writer.Close()
	select {
	case <-conn.done:
	case <-time.After(time.Second):
		t.Fatal("read loop did not observe EOF")
	}
	deadline := time.After(time.Second)
	for closeCalls.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("closeFunc was not called after read loop EOF")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if got := closeCalls.Load(); got != 1 {
		t.Fatalf("closeFunc calls = %d, want 1", got)
	}
}

func TestJSONRPCConnOversizedMessageDoesNotCloseFutureCalls(t *testing.T) {
	clientReader, serverWriter := io.Pipe()
	serverReader, clientWriter := io.Pipe()
	client := &Client{conn: newJSONRPCConnWithMaxRead(clientReader, clientWriter, clientWriter.Close, 96)}
	defer client.Close()

	serverDone := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(serverReader)
		if !scanner.Scan() {
			serverDone <- fmt.Errorf("read first request: %w", scanner.Err())
			return
		}
		if _, err := serverWriter.Write(append(bytes.Repeat([]byte("x"), 160), '\n')); err != nil {
			serverDone <- err
			return
		}
		if !scanner.Scan() {
			serverDone <- fmt.Errorf("read second request: %w", scanner.Err())
			return
		}
		var req struct {
			ID int64 `json:"id"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			serverDone <- err
			return
		}
		serverDone <- json.NewEncoder(serverWriter).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  map[string]any{"ok": true},
		})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var first map[string]any
	err := client.call(ctx, "oversized", nil, &first)
	if err == nil || !strings.Contains(err.Error(), "response id unknown") {
		t.Fatalf("first call error = %v, want oversized message error", err)
	}
	var second struct {
		OK bool `json:"ok"`
	}
	if err := client.call(ctx, "second", nil, &second); err != nil {
		t.Fatalf("second call after oversized message error = %v", err)
	}
	if !second.OK {
		t.Fatalf("second call result = %#v, want ok", second)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server goroutine error = %v", err)
	}
}

func TestReadLineLimitedAllowsLimitSizedLineWithNewline(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("abcd\n"))
	line, oversized, err := readLineLimited(reader, 4)
	if err != nil {
		t.Fatalf("readLineLimited() error = %v", err)
	}
	if oversized {
		t.Fatal("readLineLimited() oversized = true, want false")
	}
	if string(line) != "abcd" {
		t.Fatalf("line = %q, want abcd", line)
	}
}

func TestStdioClientIncludesStderrTailOnInitializeFailure(t *testing.T) {
	ctx := context.Background()
	cfg := testServerConfig("docs", false)
	cfg.StartupTimeout = time.Second
	cfg.Env["MEMAX_MCPBRIDGE_TEST_SERVER_INIT_FAIL"] = "1"
	_, err := NewStdioClient(ctx, cfg)
	if err == nil {
		t.Fatal("NewStdioClient() error = nil, want initialize failure")
	}
	if !strings.Contains(err.Error(), "test mcp init failed") {
		t.Fatalf("NewStdioClient() error = %v, want stderr tail", err)
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
	if os.Getenv("MEMAX_MCPBRIDGE_TEST_SERVER_INIT_FAIL") == "1" {
		_, _ = fmt.Fprintln(os.Stderr, "test mcp init failed")
		os.Exit(2)
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
			if query == "slow" {
				time.Sleep(200 * time.Millisecond)
			}
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

type nopWriteCloser struct{}

func (nopWriteCloser) Write(p []byte) (int, error) {
	return len(p), nil
}

func (nopWriteCloser) Close() error {
	return nil
}

func writeRPCResult(encoder *json.Encoder, id int64, result any) {
	_ = encoder.Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	})
}
