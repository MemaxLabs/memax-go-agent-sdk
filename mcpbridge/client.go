package mcpbridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultMaxRPCMessageBytes = 64 * 1024 * 1024
	defaultStderrTailBytes    = 16 * 1024
	defaultCloseGrace         = 250 * time.Millisecond
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
	stderrTail := newTailBuffer(defaultStderrTailBytes)
	if cfg.Stderr != nil {
		cmd.Stderr = io.MultiWriter(cfg.Stderr, stderrTail)
	} else if cmd.Stderr == nil {
		cmd.Stderr = stderrTail
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("start mcp server %s: %w", cfg.Name, err)
	}
	conn := newJSONRPCConn(stdout, stdin, func() error {
		_ = stdin.Close()
		waitCh := make(chan error, 1)
		go func() {
			waitCh <- cmd.Wait()
		}()
		select {
		case err := <-waitCh:
			return normalizeProcessExit(err)
		case <-time.After(defaultCloseGrace):
		}
		killErr := cmd.Process.Kill()
		_ = <-waitCh
		if killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
			return killErr
		}
		return nil
	})
	client := &Client{conn: conn}
	initCtx, cancel := contextWithOptionalTimeout(ctx, cfg.startupTimeout())
	if err := client.Initialize(initCtx, cfg); err != nil {
		cancel()
		_ = client.Close()
		if tail := stderrTail.String(); tail != "" {
			return nil, fmt.Errorf("%w; stderr: %s", err, tail)
		}
		return nil, err
	}
	cancel()
	return client, nil
}

func normalizeProcessExit(err error) error {
	if err == nil || errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if exitErr.ProcessState != nil && exitErr.ProcessState.Exited() {
			return nil
		}
	}
	return err
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
	maxRead   int

	closeOnce sync.Once
	closeErr  error
	writeMu   sync.Mutex
	mu        sync.Mutex
	pending   map[int64]chan jsonrpcResponse
	closed    bool
	err       error
	done      chan struct{}
}

func newJSONRPCConn(reader io.Reader, writer io.WriteCloser, closeFunc func() error) *jsonrpcConn {
	return newJSONRPCConnWithMaxRead(reader, writer, closeFunc, defaultMaxRPCMessageBytes)
}

func newJSONRPCConnWithMaxRead(reader io.Reader, writer io.WriteCloser, closeFunc func() error, maxRead int) *jsonrpcConn {
	c := &jsonrpcConn{
		reader:    reader,
		writer:    writer,
		closeFunc: closeFunc,
		maxRead:   maxRead,
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
	if !c.closed {
		c.closed = true
		c.failPendingLocked(io.ErrClosedPipe)
		close(c.done)
	}
	c.mu.Unlock()
	return c.runClose()
}

func (c *jsonrpcConn) runClose() error {
	c.closeOnce.Do(func() {
		if c.closeFunc != nil {
			c.closeErr = c.closeFunc()
			return
		}
		c.closeErr = c.writer.Close()
	})
	return c.closeErr
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
	reader := bufio.NewReader(c.reader)
	for {
		line, oversized, readErr := readLineLimited(reader, c.maxRead)
		if oversized {
			c.failPending(fmt.Errorf("mcp json-rpc message exceeded %d bytes", c.maxRead))
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				c.closeFromReadLoop(readErr)
				return
			}
			if errors.Is(readErr, io.EOF) {
				c.closeFromReadLoop(io.EOF)
				return
			}
			continue
		}
		if readErr != nil && !(errors.Is(readErr, io.EOF) && len(line) > 0) {
			c.closeFromReadLoop(readErr)
			return
		}
		var resp jsonrpcResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			continue
		}
		if resp.ID == 0 {
			if errors.Is(readErr, io.EOF) {
				c.closeFromReadLoop(io.EOF)
				return
			}
			continue
		}
		c.mu.Lock()
		ch := c.pending[resp.ID]
		delete(c.pending, resp.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- resp
		}
		if errors.Is(readErr, io.EOF) {
			c.closeFromReadLoop(io.EOF)
			return
		}
	}
}

func (c *jsonrpcConn) failPending(err error) {
	c.mu.Lock()
	c.failPendingLocked(err)
	c.mu.Unlock()
}

func (c *jsonrpcConn) closeFromReadLoop(err error) {
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

func readLineLimited(reader *bufio.Reader, limit int) ([]byte, bool, error) {
	if limit <= 0 {
		line, err := reader.ReadBytes('\n')
		return bytes.TrimSuffix(line, []byte{'\n'}), false, err
	}
	var out []byte
	for {
		part, err := reader.ReadSlice('\n')
		if len(out)+len(part) > limit {
			for err == bufio.ErrBufferFull {
				part, err = reader.ReadSlice('\n')
			}
			return nil, true, err
		}
		out = append(out, part...)
		if err == bufio.ErrBufferFull {
			continue
		}
		return bytes.TrimSuffix(out, []byte{'\n'}), false, err
	}
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

type tailBuffer struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func newTailBuffer(limit int) *tailBuffer {
	return &tailBuffer{limit: limit}
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if b.limit > 0 && len(b.buf) > b.limit {
		b.buf = append([]byte(nil), b.buf[len(b.buf)-b.limit:]...)
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(string(b.buf))
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
