package mcpbridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// Client is a minimal MCP JSON-RPC client.
type Client struct {
	conn *jsonrpcConn
}

// NewStdioClient starts a stdio MCP server process and initializes it.
func NewStdioClient(ctx context.Context, cfg ServerConfig) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, cfg.Command, cfg.Args...)
	if cfg.CWD != "" {
		cmd.Dir = cfg.CWD
	}
	cmd.Env = os.Environ()
	for key, value := range cfg.Env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp %s stdin: %w", cfg.Name, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("mcp %s stdout: %w", cfg.Name, err)
	}
	if cmd.Stderr == nil {
		cmd.Stderr = io.Discard
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("start mcp server %s: %w", cfg.Name, err)
	}
	conn := newJSONRPCConn(stdout, stdin, func() error {
		_ = stdin.Close()
		err := cmd.Process.Kill()
		_ = cmd.Wait()
		if errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return err
	})
	client := &Client{conn: conn}
	initCtx, cancel := contextWithOptionalTimeout(ctx, cfg.startupTimeout())
	defer cancel()
	if err := client.Initialize(initCtx, cfg); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}

// NewClientForTransport returns a client over an already-open newline-delimited
// JSON-RPC transport. It is primarily useful for tests and custom host
// transports.
func NewClientForTransport(reader io.Reader, writer io.WriteCloser) *Client {
	return &Client{conn: newJSONRPCConn(reader, writer, writer.Close)}
}

// Close closes the underlying transport.
func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// Initialize performs the MCP initialize handshake.
func (c *Client) Initialize(ctx context.Context, cfg ServerConfig) error {
	var result initializeResult
	if err := c.call(ctx, "initialize", initializeParams{
		ProtocolVersion: cfg.protocolVersion(),
		Capabilities:    map[string]any{},
		ClientInfo: implementationInfo{
			Name:    "memax-go-agent-sdk",
			Version: "dev",
		},
	}, &result); err != nil {
		return fmt.Errorf("initialize mcp server %s: %w", cfg.Name, err)
	}
	if err := c.notify(ctx, "notifications/initialized", map[string]any{}); err != nil {
		return fmt.Errorf("send initialized notification to mcp server %s: %w", cfg.Name, err)
	}
	return nil
}

func contextWithOptionalTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}

func (c *Client) call(ctx context.Context, method string, params any, result any) error {
	if c == nil || c.conn == nil {
		return fmt.Errorf("mcp client is closed")
	}
	return c.conn.Call(ctx, method, params, result)
}

func (c *Client) notify(ctx context.Context, method string, params any) error {
	if c == nil || c.conn == nil {
		return fmt.Errorf("mcp client is closed")
	}
	return c.conn.Notify(ctx, method, params)
}

type jsonrpcConn struct {
	reader    io.Reader
	writer    io.WriteCloser
	closeFunc func() error
	nextID    atomic.Int64

	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[int64]chan jsonrpcResponse
	closed  bool
	err     error
	done    chan struct{}
}

func newJSONRPCConn(reader io.Reader, writer io.WriteCloser, closeFunc func() error) *jsonrpcConn {
	c := &jsonrpcConn{
		reader:    reader,
		writer:    writer,
		closeFunc: closeFunc,
		pending:   map[int64]chan jsonrpcResponse{},
		done:      make(chan struct{}),
	}
	go c.readLoop()
	return c
}

func (c *jsonrpcConn) Call(ctx context.Context, method string, params any, result any) error {
	id := c.nextID.Add(1)
	ch := make(chan jsonrpcResponse, 1)
	c.mu.Lock()
	if c.closed {
		err := c.err
		c.mu.Unlock()
		if err == nil {
			err = io.ErrClosedPipe
		}
		return err
	}
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.write(jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}); err != nil {
		c.removePending(id)
		return err
	}

	select {
	case <-ctx.Done():
		c.removePending(id)
		return ctx.Err()
	case resp := <-ch:
		if resp.Error != nil {
			return resp.Error
		}
		if result == nil || len(resp.Result) == 0 {
			return nil
		}
		if err := json.Unmarshal(resp.Result, result); err != nil {
			return fmt.Errorf("decode mcp %s response: %w", method, err)
		}
		return nil
	}
}

func (c *jsonrpcConn) Notify(ctx context.Context, method string, params any) error {
	done := make(chan error, 1)
	go func() {
		done <- c.write(jsonrpcNotification{
			JSONRPC: "2.0",
			Method:  method,
			Params:  params,
		})
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	}
}

func (c *jsonrpcConn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.failPendingLocked(io.ErrClosedPipe)
	close(c.done)
	c.mu.Unlock()
	if c.closeFunc != nil {
		return c.closeFunc()
	}
	return c.writer.Close()
}

func (c *jsonrpcConn) write(msg any) error {
	encoded, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.writer.Write(append(encoded, '\n')); err != nil {
		return err
	}
	return nil
}

func (c *jsonrpcConn) readLoop() {
	scanner := bufio.NewScanner(c.reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		var resp jsonrpcResponse
		if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
			continue
		}
		if resp.ID == 0 {
			continue
		}
		c.mu.Lock()
		ch := c.pending[resp.ID]
		delete(c.pending, resp.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- resp
		}
	}
	err := scanner.Err()
	if err == nil {
		err = io.EOF
	}
	c.mu.Lock()
	if !c.closed {
		c.closed = true
		c.err = err
		c.failPendingLocked(err)
		close(c.done)
	}
	c.mu.Unlock()
}

func (c *jsonrpcConn) removePending(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *jsonrpcConn) failPendingLocked(err error) {
	for id, ch := range c.pending {
		delete(c.pending, id)
		ch <- jsonrpcResponse{ID: id, Error: &rpcError{Code: -32000, Message: err.Error()}}
	}
}

type jsonrpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type jsonrpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int64           `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return fmt.Sprintf("mcp rpc error %d", e.Code)
	}
	return fmt.Sprintf("mcp rpc error %d: %s", e.Code, e.Message)
}

type initializeParams struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    map[string]any     `json:"capabilities"`
	ClientInfo      implementationInfo `json:"clientInfo"`
}

type initializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    map[string]any     `json:"capabilities,omitempty"`
	ServerInfo      implementationInfo `json:"serverInfo,omitempty"`
}

type implementationInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}
