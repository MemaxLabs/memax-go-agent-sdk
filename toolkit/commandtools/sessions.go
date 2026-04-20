package commandtools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/hook"
	"github.com/MemaxLabs/memax-go-agent-sdk/identity"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/approvaltools"
)

const (
	// StartToolName is the default tool name for starting a managed command
	// session that can be inspected or stopped in later turns.
	StartToolName = "start_command"
	// WriteInputToolName is the default tool name for writing stdin to a managed
	// command session and optionally waiting briefly for fresh output.
	WriteInputToolName = "write_command_input"
	// ResizeToolName is the default tool name for resizing the terminal of a
	// PTY-backed managed command session.
	ResizeToolName = "resize_command_terminal"
	// ReadOutputToolName is the default tool name for reading buffered output
	// from a managed command session.
	ReadOutputToolName = "read_command_output"
	// StopToolName is the default tool name for stopping a managed command
	// session.
	StopToolName = "stop_command"
	// ListToolName is the default tool name for listing managed command sessions
	// visible to the current agent session.
	ListToolName = "list_commands"

	defaultReadChunks     = 32
	defaultReadBytes      = 16 * 1024
	defaultWriteYield     = 250 * time.Millisecond
	defaultSessionTimeout = time.Hour
	defaultTTYCols        = 80
	defaultTTYRows        = 24
	maxTTYDimension       = 32767
)

var (
	// ErrCommandSessionUnknown is returned when a managed command session ID is
	// not known to the session manager.
	ErrCommandSessionUnknown = errors.New("commandtools: unknown command session")
	// ErrCommandSessionNotVisible is returned when a managed command session
	// exists but belongs to a different agent session boundary.
	ErrCommandSessionNotVisible = errors.New("commandtools: command session is not visible in this agent session")
	// ErrCommandSessionAlreadyExists is returned when a caller requests a
	// managed command session ID that is already active in the manager.
	ErrCommandSessionAlreadyExists = errors.New("commandtools: command session already exists")
	// ErrCommandSessionNotRunning is returned when an operation requires a
	// running managed command session but the session has already terminated.
	ErrCommandSessionNotRunning = errors.New("commandtools: command session is not running")
	// ErrCommandSessionNotPTY is returned when a terminal-only operation is
	// requested for a command session that was not started with TTY enabled.
	ErrCommandSessionNotPTY = errors.New("commandtools: command session is not PTY-backed")
	// ErrCommandSessionStdinClosed is returned when stdin is unavailable for a
	// managed command session that otherwise accepts input operations.
	ErrCommandSessionStdinClosed = errors.New("commandtools: command session stdin is closed")
	// ErrCommandSessionTerminalClosed is returned when a PTY-backed command
	// session no longer has an open terminal handle.
	ErrCommandSessionTerminalClosed = errors.New("commandtools: command session terminal is closed")
	// ErrCommandSessionPTYUnsupported is returned when the current manager or
	// platform cannot start PTY-backed command sessions.
	ErrCommandSessionPTYUnsupported = errors.New("commandtools: PTY sessions are not supported")
)

type classifiedCommandSessionError struct {
	kind error
	msg  string
}

func (e classifiedCommandSessionError) Error() string {
	return e.msg
}

func (e classifiedCommandSessionError) Unwrap() error {
	return e.kind
}

func commandSessionError(kind error, format string, args ...any) error {
	return classifiedCommandSessionError{
		kind: kind,
		msg:  fmt.Sprintf(format, args...),
	}
}

// Metadata for managed command sessions.
const (
	MetadataCommandSessionID          = model.MetadataCommandSessionID
	MetadataCommandStatus             = model.MetadataCommandStatus
	MetadataCommandPID                = model.MetadataCommandPID
	MetadataCommandTTY                = model.MetadataCommandTTY
	MetadataCommandSignalsProcessTree = model.MetadataCommandSignalsProcessTree
	MetadataCommandCols               = model.MetadataCommandCols
	MetadataCommandRows               = model.MetadataCommandRows
	MetadataCommandStartedAt          = model.MetadataCommandStartedAt
	MetadataCommandFinishedAt         = model.MetadataCommandFinishedAt
	MetadataCommandInputBytes         = model.MetadataCommandInputBytes
	MetadataCommandNextSeq            = model.MetadataCommandNextSeq
	MetadataCommandOutputChunks       = model.MetadataCommandOutputChunks
	MetadataCommandDroppedChunks      = model.MetadataCommandDroppedChunks
	MetadataCommandDroppedBytes       = model.MetadataCommandDroppedBytes
)

// SessionStatus describes a managed command lifecycle state.
type SessionStatus string

const (
	SessionRunning SessionStatus = "running"
	SessionExited  SessionStatus = "exited"
	SessionStopped SessionStatus = "stopped"
)

// CommandSession describes one managed command session.
type CommandSession struct {
	ID              string
	SessionID       string
	ParentSessionID string
	Identity        identity.Identity
	Argv            []string
	CWD             string
	Purpose         string
	Status          SessionStatus
	PID             int
	TTY             bool
	Cols            int
	Rows            int
	// SignalsProcessTree reports whether StopCommand and timeout handling target
	// the launched process tree/group instead of only the top-level process.
	SignalsProcessTree bool
	StartedAt          time.Time
	FinishedAt         *time.Time
	ExitCode           *int
	TimedOut           bool
	NextSeq            int
	DroppedChunks      int
	DroppedBytes       int
}

// OutputChunk is one ordered piece of buffered command output. Seq is
// monotonically increasing within a command session and remains stable across
// reads so callers can pass ReadRequest.AfterSeq to resume from the last seen
// chunk.
type OutputChunk struct {
	Seq    int
	Stream string
	Text   string
	Time   time.Time
}

// StartRequest starts a managed command session.
type StartRequest struct {
	ID              string
	SessionID       string
	ParentSessionID string
	Identity        identity.Identity
	Argv            []string
	CWD             string
	Env             map[string]string
	Stdin           string
	TTY             bool
	Cols            int
	Rows            int
	Timeout         time.Duration
	Purpose         string
	Metadata        map[string]any
}

// ReadRequest reads buffered output from a managed command session.
type ReadRequest struct {
	ID              string
	SessionID       string
	ParentSessionID string
	Identity        identity.Identity
	AfterSeq        int
	MaxChunks       int
	MaxBytes        int
}

// ReadResult contains newly available output for a managed command session.
type ReadResult struct {
	Session CommandSession
	Chunks  []OutputChunk
	NextSeq int
}

// WriteRequest writes stdin to a managed command session and can optionally
// wait briefly for fresh output produced after the write.
type WriteRequest struct {
	ID              string
	SessionID       string
	ParentSessionID string
	Identity        identity.Identity
	Input           string
	Yield           time.Duration
	MaxChunks       int
	MaxBytes        int
}

// WriteResult contains any new output observed after a write request.
type WriteResult struct {
	Session    CommandSession
	Chunks     []OutputChunk
	NextSeq    int
	InputBytes int
}

// ResizeRequest resizes a PTY-backed managed command session.
type ResizeRequest struct {
	ID              string
	SessionID       string
	ParentSessionID string
	Identity        identity.Identity
	Cols            int
	Rows            int
}

// StopRequest stops a managed command session.
type StopRequest struct {
	ID              string
	SessionID       string
	ParentSessionID string
	Identity        identity.Identity
	Force           bool
}

// ListRequest lists command sessions visible to the current agent session.
type ListRequest struct {
	SessionID        string
	ParentSessionID  string
	Identity         identity.Identity
	IncludeCompleted bool
	Limit            int
}

// Starter starts managed command sessions.
type Starter interface {
	StartCommand(context.Context, StartRequest) (CommandSession, error)
}

// Reader reads buffered command output.
type Reader interface {
	ReadCommandOutput(context.Context, ReadRequest) (ReadResult, error)
}

// Writer writes stdin to a running managed command session.
type Writer interface {
	WriteCommandInput(context.Context, WriteRequest) (WriteResult, error)
}

// Resizer changes terminal geometry for a PTY-backed managed command session.
type Resizer interface {
	ResizeCommandTerminal(context.Context, ResizeRequest) (CommandSession, error)
}

// Stopper stops managed command sessions.
type Stopper interface {
	StopCommand(context.Context, StopRequest) (CommandSession, error)
}

// Lister lists managed command sessions.
type Lister interface {
	ListCommands(context.Context, ListRequest) ([]CommandSession, error)
}

// Cleaner removes or stops any managed command sessions owned by an agent
// session when that session ends.
type Cleaner interface {
	CleanupSession(context.Context, string) error
}

// SessionManager is the convenience baseline interface for managed command
// session tools. Hosts can still expose narrower capabilities by using the
// individual New*Tool constructors. NewSessionTools also installs
// write_command_input and resize_command_terminal when the provided manager
// implements the corresponding optional interfaces.
type SessionManager interface {
	Starter
	Reader
	Stopper
	Lister
}

// NewSessionTools returns the standard managed command-session tool set over
// manager. Use the individual constructors when a host wants to expose only a
// subset of lifecycle capabilities.
func NewSessionTools(manager SessionManager) ([]tool.Tool, error) {
	if manager == nil {
		return nil, fmt.Errorf("commandtools: session manager is required")
	}
	tools := []tool.Tool{
		NewStartTool(manager),
		NewReadOutputTool(manager),
		NewStopTool(manager),
		NewListTool(manager),
	}
	if writer, ok := any(manager).(Writer); ok {
		tools = append(tools, NewWriteInputTool(writer))
	}
	if resizer, ok := any(manager).(Resizer); ok {
		tools = append(tools, NewResizeTool(resizer))
	}
	return tools, nil
}

// SessionCleanupOptions returns hook options that call cleaner when the agent
// session ends. Hosts should install this when managed command sessions must
// not outlive the parent agent session.
func SessionCleanupOptions(cleaner Cleaner) []hook.Option {
	if cleaner == nil {
		return nil
	}
	return []hook.Option{
		hook.WithSessionEnded(func(ctx context.Context, input hook.SessionEndedInput) error {
			return cleaner.CleanupSession(ctx, input.SessionID)
		}),
	}
}

// NewStartTool returns a tool that starts a managed command session.
func NewStartTool(starter Starter) tool.Tool {
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:           StartToolName,
			Description:    "Start a host-owned command session that can be inspected or stopped in later turns.",
			SearchHint:     "start background command dev server watcher process session",
			Destructive:    true,
			MaxResultBytes: 8 * 1024,
			InputSchema:    startInputSchema(),
		},
		Handler: func(ctx context.Context, call tool.Call) (model.ToolResult, error) {
			input, err := tool.DecodeInput[startInput](call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			req, err := startRequestFromInput(input)
			if err != nil {
				return model.ToolResult{}, err
			}
			req.SessionID = call.Runtime.SessionID
			req.ParentSessionID = call.Runtime.ParentSessionID
			req.Identity = call.Runtime.Identity
			session, err := starter.StartCommand(ctx, req)
			if err != nil {
				return model.ToolResult{}, err
			}
			return startResult(session), nil
		},
	}
}

// NewReadOutputTool returns a tool that reads buffered output from a managed
// command session.
func NewReadOutputTool(reader Reader) tool.Tool {
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:            ReadOutputToolName,
			Description:     "Read newly available buffered output from a managed command session.",
			SearchHint:      "read command output logs session process watcher server",
			ReadOnly:        true,
			ConcurrencySafe: true,
			MaxResultBytes:  32 * 1024,
			InputSchema:     readInputSchema(),
		},
		Handler: func(ctx context.Context, call tool.Call) (model.ToolResult, error) {
			input, err := tool.DecodeInput[readInput](call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			req := ReadRequest{
				ID:              strings.TrimSpace(input.ID),
				SessionID:       call.Runtime.SessionID,
				ParentSessionID: call.Runtime.ParentSessionID,
				Identity:        call.Runtime.Identity,
				AfterSeq:        input.AfterSeq,
				MaxChunks:       input.Limit,
				MaxBytes:        input.MaxBytes,
			}
			if req.ID == "" {
				return model.ToolResult{}, fmt.Errorf("commandtools: id is required")
			}
			result, err := reader.ReadCommandOutput(ctx, req)
			if err != nil {
				return model.ToolResult{}, err
			}
			return readResult(result), nil
		},
	}
}

// NewWriteInputTool returns a tool that writes stdin to a managed command
// session and can wait briefly for output produced after the write began.
func NewWriteInputTool(writer Writer) tool.Tool {
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:           WriteInputToolName,
			Description:    "Write input to a managed command session and optionally wait briefly for new output.",
			SearchHint:     "write send input stdin interact command session process watcher repl terminal",
			Destructive:    true,
			MaxResultBytes: 32 * 1024,
			InputSchema: map[string]any{
				"type":                 "object",
				"required":             []any{"id"},
				"additionalProperties": false,
				"properties": map[string]any{
					"id": map[string]any{
						"type":        "string",
						"description": "Command session ID returned by start_command.",
						"minLength":   1,
					},
					"input": map[string]any{
						"type":        "string",
						"description": "Text to write to stdin. It may be empty when only waiting for fresh output.",
					},
					"append_newline": map[string]any{
						"type":        "boolean",
						"description": "Append a trailing newline after input. Useful for line-oriented REPLs and watch commands.",
					},
					"yield_ms": map[string]any{
						"type":        "integer",
						"description": "Optional time to wait for fresh output after writing. Defaults to a short wait when omitted; set to 0 to disable waiting.",
						"minimum":     0,
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Optional maximum number of chunks to return.",
						"minimum":     1,
					},
					"max_bytes": map[string]any{
						"type":        "integer",
						"description": "Optional approximate retained byte budget for returned chunk text.",
						"minimum":     1,
					},
				},
			},
		},
		Handler: func(ctx context.Context, call tool.Call) (model.ToolResult, error) {
			input, err := tool.DecodeInput[writeInput](call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			req := WriteRequest{
				ID:              strings.TrimSpace(input.ID),
				SessionID:       call.Runtime.SessionID,
				ParentSessionID: call.Runtime.ParentSessionID,
				Identity:        call.Runtime.Identity,
				Input:           input.Input,
				MaxChunks:       input.Limit,
				MaxBytes:        input.MaxBytes,
			}
			if req.ID == "" {
				return model.ToolResult{}, fmt.Errorf("commandtools: id is required")
			}
			if input.AppendNewline {
				req.Input += "\n"
			}
			if input.YieldMS == nil {
				req.Yield = defaultWriteYield
			} else {
				req.Yield = time.Duration(*input.YieldMS) * time.Millisecond
			}
			result, err := writer.WriteCommandInput(ctx, req)
			if err != nil {
				return model.ToolResult{}, err
			}
			return writeResult(result), nil
		},
	}
}

// NewResizeTool returns a tool that resizes the terminal geometry of a
// PTY-backed managed command session.
func NewResizeTool(resizer Resizer) tool.Tool {
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:           ResizeToolName,
			Description:    "Resize the terminal geometry of a PTY-backed managed command session.",
			SearchHint:     "resize terminal pty session cols rows width height",
			Destructive:    false,
			MaxResultBytes: 8 * 1024,
			InputSchema: map[string]any{
				"type":                 "object",
				"required":             []any{"id", "cols", "rows"},
				"additionalProperties": false,
				"properties": map[string]any{
					"id": map[string]any{
						"type":        "string",
						"description": "Command session ID returned by start_command.",
						"minLength":   1,
					},
					"cols": map[string]any{
						"type":        "integer",
						"description": "Terminal width in character cells.",
						"minimum":     1,
						"maximum":     maxTTYDimension,
					},
					"rows": map[string]any{
						"type":        "integer",
						"description": "Terminal height in character cells.",
						"minimum":     1,
						"maximum":     maxTTYDimension,
					},
				},
			},
		},
		Handler: func(ctx context.Context, call tool.Call) (model.ToolResult, error) {
			input, err := tool.DecodeInput[resizeInput](call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			req := ResizeRequest{
				ID:              strings.TrimSpace(input.ID),
				SessionID:       call.Runtime.SessionID,
				ParentSessionID: call.Runtime.ParentSessionID,
				Identity:        call.Runtime.Identity,
				Cols:            input.Cols,
				Rows:            input.Rows,
			}
			if req.ID == "" {
				return model.ToolResult{}, fmt.Errorf("commandtools: id is required")
			}
			if err := validateTTYDimensions(req.Cols, req.Rows); err != nil {
				return model.ToolResult{}, err
			}
			session, err := resizer.ResizeCommandTerminal(ctx, req)
			if err != nil {
				return model.ToolResult{}, err
			}
			return resizeResult(session), nil
		},
	}
}

// NewStopTool returns a tool that stops a managed command session.
func NewStopTool(stopper Stopper) tool.Tool {
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:           StopToolName,
			Description:    "Stop a managed command session.",
			SearchHint:     "stop terminate command session process server watcher",
			Destructive:    true,
			MaxResultBytes: 8 * 1024,
			InputSchema: map[string]any{
				"type":                 "object",
				"required":             []any{"id"},
				"additionalProperties": false,
				"properties": map[string]any{
					"id": map[string]any{
						"type":        "string",
						"description": "Command session ID returned by start_command.",
						"minLength":   1,
					},
					"force": map[string]any{
						"type":        "boolean",
						"description": "Force termination instead of a graceful stop when supported.",
					},
				},
			},
		},
		Handler: func(ctx context.Context, call tool.Call) (model.ToolResult, error) {
			input, err := tool.DecodeInput[stopInput](call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			req := StopRequest{
				ID:              strings.TrimSpace(input.ID),
				SessionID:       call.Runtime.SessionID,
				ParentSessionID: call.Runtime.ParentSessionID,
				Identity:        call.Runtime.Identity,
				Force:           input.Force,
			}
			if req.ID == "" {
				return model.ToolResult{}, fmt.Errorf("commandtools: id is required")
			}
			session, err := stopper.StopCommand(ctx, req)
			if err != nil {
				return model.ToolResult{}, err
			}
			return stopResult(session), nil
		},
	}
}

// NewListTool returns a tool that lists managed command sessions visible to the
// current agent session.
func NewListTool(lister Lister) tool.Tool {
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:            ListToolName,
			Description:     "List managed command sessions for the current agent session.",
			SearchHint:      "list command sessions processes servers watchers",
			ReadOnly:        true,
			ConcurrencySafe: true,
			MaxResultBytes:  16 * 1024,
			InputSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"include_completed": map[string]any{
						"type":        "boolean",
						"description": "Include exited or stopped command sessions.",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Optional maximum number of sessions to return.",
						"minimum":     1,
					},
				},
			},
		},
		Handler: func(ctx context.Context, call tool.Call) (model.ToolResult, error) {
			input, err := tool.DecodeInput[listInput](call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			sessions, err := lister.ListCommands(ctx, ListRequest{
				SessionID:        call.Runtime.SessionID,
				ParentSessionID:  call.Runtime.ParentSessionID,
				Identity:         call.Runtime.Identity,
				IncludeCompleted: input.IncludeCompleted,
				Limit:            input.Limit,
			})
			if err != nil {
				return model.ToolResult{}, err
			}
			return listResult(sessions), nil
		},
	}
}

type startInput struct {
	ID        string            `json:"id"`
	Command   []string          `json:"command"`
	CWD       string            `json:"cwd"`
	Env       map[string]string `json:"env"`
	Stdin     string            `json:"stdin"`
	TTY       bool              `json:"tty"`
	Cols      int               `json:"cols"`
	Rows      int               `json:"rows"`
	TimeoutMS int               `json:"timeout_ms"`
	Purpose   string            `json:"purpose"`
	Metadata  map[string]any    `json:"metadata"`
}

type readInput struct {
	ID       string `json:"id"`
	AfterSeq int    `json:"after_seq"`
	Limit    int    `json:"limit"`
	MaxBytes int    `json:"max_bytes"`
}

type writeInput struct {
	ID            string `json:"id"`
	Input         string `json:"input"`
	AppendNewline bool   `json:"append_newline"`
	YieldMS       *int   `json:"yield_ms"`
	Limit         int    `json:"limit"`
	MaxBytes      int    `json:"max_bytes"`
}

type resizeInput struct {
	ID   string `json:"id"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

type stopInput struct {
	ID    string `json:"id"`
	Force bool   `json:"force"`
}

type listInput struct {
	IncludeCompleted bool `json:"include_completed"`
	Limit            int  `json:"limit"`
}

func startInputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"required":             []any{"command"},
		"additionalProperties": false,
		"properties": map[string]any{
			"id": map[string]any{
				"type":        "string",
				"description": "Optional stable command session ID. If omitted, the host generates one.",
			},
			"command": map[string]any{
				"type":        "array",
				"description": "Command argv vector. The first element is the executable. No shell is implied.",
				"minItems":    1,
				"items":       map[string]any{"type": "string"},
			},
			"cwd": map[string]any{
				"type":        "string",
				"description": "Optional runner-relative working directory.",
			},
			"env": map[string]any{
				"type":        "object",
				"description": "Optional environment overrides allowed by the manager.",
				"additionalProperties": map[string]any{
					"type": "string",
				},
			},
			"stdin": map[string]any{
				"type":        "string",
				"description": "Optional initial stdin content.",
			},
			"tty": map[string]any{
				"type":        "boolean",
				"description": "Allocate a PTY-backed terminal session. Use for shells, REPLs, prompts, or tools that behave differently when attached to a terminal.",
			},
			"cols": map[string]any{
				"type":        "integer",
				"description": "Optional PTY terminal width in character cells. Defaults to 80 when tty is true.",
				"minimum":     1,
				"maximum":     maxTTYDimension,
			},
			"rows": map[string]any{
				"type":        "integer",
				"description": "Optional PTY terminal height in character cells. Defaults to 24 when tty is true.",
				"minimum":     1,
				"maximum":     maxTTYDimension,
			},
			"timeout_ms": map[string]any{
				"type":        "integer",
				"description": "Optional command timeout in milliseconds.",
				"minimum":     1,
			},
			"purpose": map[string]any{
				"type":        "string",
				"description": "Short explanation of why this command session is needed.",
			},
			"metadata": map[string]any{
				"type":        "object",
				"description": "Optional host-defined metadata.",
			},
		},
	}
}

func readInputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"required":             []any{"id"},
		"additionalProperties": false,
		"properties": map[string]any{
			"id": map[string]any{
				"type":        "string",
				"description": "Command session ID returned by start_command.",
				"minLength":   1,
			},
			"after_seq": map[string]any{
				"type":        "integer",
				"description": "Return chunks whose sequence number is greater than this value.",
				"minimum":     0,
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Optional maximum number of chunks to return.",
				"minimum":     1,
			},
			"max_bytes": map[string]any{
				"type":        "integer",
				"description": "Optional approximate retained byte budget for returned chunk text.",
				"minimum":     1,
			},
		},
	}
}

// ApprovalSummaryFromStartInput returns a host-facing approval summary for a
// start_command tool input. It is intentionally input-only: it does not inspect
// manager state or classify process side effects.
func ApprovalSummaryFromStartInput(inputBytes []byte) (approvaltools.Summary, error) {
	var input startInput
	if len(inputBytes) > 0 {
		if err := json.Unmarshal(inputBytes, &input); err != nil {
			return approvaltools.Summary{}, fmt.Errorf("commandtools: decode start input: %w", err)
		}
	}
	argv := normalizeArgv(input.Command)
	if len(argv) == 0 {
		return approvaltools.Summary{}, nil
	}
	title := "Start command session: " + strings.Join(argv, " ")
	description := strings.TrimSpace(input.Purpose)
	if description == "" {
		description = "Start managed command session " + strings.Join(argv, " ")
	}
	return approvaltools.Summary{
		Title:       title,
		Description: description,
		Risk:        startApprovalRisk(input.TTY),
		Changes:     1,
	}, nil
}

func startApprovalRisk(tty bool) string {
	if tty {
		return "May keep reading or mutating host-owned state until the terminal session is stopped or times out. PTY sessions can change command behavior compared with plain pipes."
	}
	return "May keep reading or mutating host-owned state until the session is stopped or times out."
}

func startRequestFromInput(input startInput) (StartRequest, error) {
	argv := normalizeArgv(input.Command)
	if len(argv) == 0 {
		return StartRequest{}, fmt.Errorf("commandtools: command must contain at least one argv element")
	}
	cols, rows, err := normalizeStartTTYDimensions(input.TTY, input.Cols, input.Rows)
	if err != nil {
		return StartRequest{}, err
	}
	timeout := defaultSessionTimeout
	if input.TimeoutMS > 0 {
		timeout = time.Duration(input.TimeoutMS) * time.Millisecond
	}
	return StartRequest{
		ID:       strings.TrimSpace(input.ID),
		Argv:     argv,
		CWD:      strings.TrimSpace(input.CWD),
		Env:      cloneStringMap(input.Env),
		Stdin:    input.Stdin,
		TTY:      input.TTY,
		Cols:     cols,
		Rows:     rows,
		Timeout:  timeout,
		Purpose:  strings.TrimSpace(input.Purpose),
		Metadata: model.CloneMetadata(input.Metadata),
	}, nil
}

func startResult(session CommandSession) model.ToolResult {
	metadata := sessionMetadata(session)
	metadata[MetadataCommandOperation] = "start"
	return model.ToolResult{
		Content:  formatSessionStart(session),
		Metadata: metadata,
	}
}

func readResult(result ReadResult) model.ToolResult {
	metadata := sessionMetadata(result.Session)
	metadata[MetadataCommandOperation] = "read"
	metadata[MetadataCommandNextSeq] = result.NextSeq
	metadata[MetadataCommandOutputChunks] = len(result.Chunks)
	return model.ToolResult{
		Content:  formatSessionRead(result),
		Metadata: metadata,
	}
}

func writeResult(result WriteResult) model.ToolResult {
	metadata := sessionMetadata(result.Session)
	metadata[MetadataCommandOperation] = "write"
	metadata[MetadataCommandInputBytes] = result.InputBytes
	metadata[MetadataCommandNextSeq] = result.NextSeq
	metadata[MetadataCommandOutputChunks] = len(result.Chunks)
	return model.ToolResult{
		Content:  formatSessionWrite(result),
		Metadata: metadata,
	}
}

func resizeResult(session CommandSession) model.ToolResult {
	metadata := sessionMetadata(session)
	metadata[MetadataCommandOperation] = "resize"
	return model.ToolResult{
		Content:  formatSessionResize(session),
		Metadata: metadata,
	}
}

func stopResult(session CommandSession) model.ToolResult {
	metadata := sessionMetadata(session)
	metadata[MetadataCommandOperation] = "stop"
	return model.ToolResult{
		Content:  formatSessionStop(session),
		Metadata: metadata,
	}
}

func listResult(sessions []CommandSession) model.ToolResult {
	if len(sessions) == 0 {
		return model.ToolResult{
			Content:  "no command sessions",
			Metadata: map[string]any{"count": 0},
		}
	}
	var b strings.Builder
	for i, session := range sessions {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%s\t%s\t%s", session.ID, session.Status, strings.Join(session.Argv, " "))
		if session.TTY {
			b.WriteString("\ttty=true")
			if session.Cols > 0 && session.Rows > 0 {
				fmt.Fprintf(&b, "\tsize=%dx%d", session.Cols, session.Rows)
			}
		}
		if session.CWD != "" {
			fmt.Fprintf(&b, "\tcwd=%s", session.CWD)
		}
		if session.ExitCode != nil {
			fmt.Fprintf(&b, "\texit_code=%d", *session.ExitCode)
		}
	}
	return model.ToolResult{
		Content: b.String(),
		Metadata: map[string]any{
			"count": len(sessions),
		},
	}
}

func sessionMetadata(session CommandSession) map[string]any {
	metadata := map[string]any{
		MetadataCommandSessionID:          session.ID,
		MetadataCommandArgv:               append([]string(nil), session.Argv...),
		MetadataCommandCWD:                session.CWD,
		MetadataCommandStatus:             string(session.Status),
		MetadataCommandPID:                session.PID,
		MetadataCommandTTY:                session.TTY,
		MetadataCommandSignalsProcessTree: session.SignalsProcessTree,
		MetadataCommandStartedAt:          session.StartedAt.UTC().Format(time.RFC3339Nano),
		MetadataCommandTimedOut:           session.TimedOut,
		MetadataCommandNextSeq:            session.NextSeq,
	}
	if session.TTY {
		metadata[MetadataCommandCols] = session.Cols
		metadata[MetadataCommandRows] = session.Rows
	}
	if session.FinishedAt != nil {
		metadata[MetadataCommandFinishedAt] = session.FinishedAt.UTC().Format(time.RFC3339Nano)
	}
	if session.ExitCode != nil {
		metadata[MetadataCommandExitCode] = *session.ExitCode
	}
	if session.DroppedChunks > 0 {
		metadata[MetadataCommandDroppedChunks] = session.DroppedChunks
	}
	if session.DroppedBytes > 0 {
		metadata[MetadataCommandDroppedBytes] = session.DroppedBytes
	}
	return metadata
}

func normalizeStartTTYDimensions(tty bool, cols, rows int) (int, int, error) {
	if !tty {
		if cols > 0 || rows > 0 {
			return 0, 0, fmt.Errorf("commandtools: cols and rows require tty=true")
		}
		return 0, 0, nil
	}
	if (cols > 0) != (rows > 0) {
		return 0, 0, fmt.Errorf("commandtools: cols and rows must be set together")
	}
	if cols == 0 && rows == 0 {
		return defaultTTYCols, defaultTTYRows, nil
	}
	if err := validateTTYDimensions(cols, rows); err != nil {
		return 0, 0, err
	}
	return cols, rows, nil
}

func validateTTYDimensions(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("commandtools: cols and rows must be positive")
	}
	if cols > maxTTYDimension || rows > maxTTYDimension {
		return fmt.Errorf("commandtools: cols and rows must be <= %d", maxTTYDimension)
	}
	return nil
}

func formatSessionStart(session CommandSession) string {
	var b strings.Builder
	fmt.Fprintf(&b, "started command session %s: %s", session.ID, strings.Join(session.Argv, " "))
	fmt.Fprintf(&b, "\nstatus: %s", session.Status)
	if session.TTY {
		b.WriteString("\ntty: true")
		if session.Cols > 0 && session.Rows > 0 {
			fmt.Fprintf(&b, "\nsize: %dx%d", session.Cols, session.Rows)
		}
	}
	if session.CWD != "" {
		fmt.Fprintf(&b, "\ncwd: %s", session.CWD)
	}
	if session.PID != 0 {
		fmt.Fprintf(&b, "\npid: %d", session.PID)
	}
	if session.Purpose != "" {
		fmt.Fprintf(&b, "\npurpose: %s", session.Purpose)
	}
	return b.String()
}

func formatSessionRead(result ReadResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "command output for %s: %s", result.Session.ID, strings.Join(result.Session.Argv, " "))
	fmt.Fprintf(&b, "\nstatus: %s", result.Session.Status)
	if result.Session.TTY {
		b.WriteString("\ntty: true")
		if result.Session.Cols > 0 && result.Session.Rows > 0 {
			fmt.Fprintf(&b, "\nsize: %dx%d", result.Session.Cols, result.Session.Rows)
		}
	}
	fmt.Fprintf(&b, "\nnext_seq: %d", result.NextSeq)
	if result.Session.DroppedChunks > 0 {
		fmt.Fprintf(&b, "\ndropped_chunks: %d", result.Session.DroppedChunks)
	}
	if len(result.Chunks) == 0 {
		b.WriteString("\nno new output")
		return b.String()
	}
	for _, chunk := range result.Chunks {
		fmt.Fprintf(&b, "\n[%s #%d]\n%s", chunk.Stream, chunk.Seq, chunk.Text)
	}
	return b.String()
}

func formatSessionWrite(result WriteResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "wrote input to command session %s: %s", result.Session.ID, strings.Join(result.Session.Argv, " "))
	fmt.Fprintf(&b, "\nstatus: %s", result.Session.Status)
	if result.Session.TTY {
		b.WriteString("\ntty: true")
		if result.Session.Cols > 0 && result.Session.Rows > 0 {
			fmt.Fprintf(&b, "\nsize: %dx%d", result.Session.Cols, result.Session.Rows)
		}
	}
	fmt.Fprintf(&b, "\ninput_bytes: %d", result.InputBytes)
	fmt.Fprintf(&b, "\nnext_seq: %d", result.NextSeq)
	if result.Session.DroppedChunks > 0 {
		fmt.Fprintf(&b, "\ndropped_chunks: %d", result.Session.DroppedChunks)
	}
	if len(result.Chunks) == 0 {
		b.WriteString("\nno new output")
		return b.String()
	}
	for _, chunk := range result.Chunks {
		fmt.Fprintf(&b, "\n[%s #%d]\n%s", chunk.Stream, chunk.Seq, chunk.Text)
	}
	return b.String()
}

func formatSessionResize(session CommandSession) string {
	var b strings.Builder
	fmt.Fprintf(&b, "resized command session %s: %s", session.ID, strings.Join(session.Argv, " "))
	fmt.Fprintf(&b, "\nstatus: %s", session.Status)
	if session.TTY {
		b.WriteString("\ntty: true")
	}
	if session.Cols > 0 && session.Rows > 0 {
		fmt.Fprintf(&b, "\nsize: %dx%d", session.Cols, session.Rows)
	}
	return b.String()
}

func formatSessionStop(session CommandSession) string {
	var b strings.Builder
	fmt.Fprintf(&b, "stopped command session %s: %s", session.ID, strings.Join(session.Argv, " "))
	fmt.Fprintf(&b, "\nstatus: %s", session.Status)
	if session.TTY {
		b.WriteString("\ntty: true")
		if session.Cols > 0 && session.Rows > 0 {
			fmt.Fprintf(&b, "\nsize: %dx%d", session.Cols, session.Rows)
		}
	}
	if session.ExitCode != nil {
		fmt.Fprintf(&b, "\nexit_code: %d", *session.ExitCode)
	}
	if session.TimedOut {
		b.WriteString("\ntimed_out: true")
	}
	return b.String()
}

// ScriptedOutputPage is one deterministic read page returned by a
// ScriptedSessionManager.
type ScriptedOutputPage struct {
	Chunks   []OutputChunk
	Running  bool
	ExitCode *int
	TimedOut bool
}

// ScriptedWritePage is one deterministic write interaction returned by a
// ScriptedSessionManager.
type ScriptedWritePage struct {
	Page  ScriptedOutputPage
	Error string
}

// ScriptedCommand describes one managed command session for deterministic
// tests and evals.
type ScriptedCommand struct {
	ID           string
	PID          int
	TTY          bool
	Cols         int
	Rows         int
	Pages        []ScriptedOutputPage
	WritePages   []ScriptedWritePage
	StopExitCode *int
	StopTimedOut bool
}

// ScriptedSessionManager is a deterministic concurrency-safe managed command
// implementation for tests and evals.
type ScriptedSessionManager struct {
	mu             sync.Mutex
	commands       []ScriptedCommand
	sessions       map[string]*scriptedSessionState
	startRequests  []StartRequest
	writeRequests  []WriteRequest
	resizeRequests []ResizeRequest
	readRequests   []ReadRequest
	stopRequests   []StopRequest
}

type scriptedSessionState struct {
	session  CommandSession
	pages    []ScriptedOutputPage
	page     int
	writes   []ScriptedWritePage
	write    int
	stopExit *int
	stopTime bool
}

// NewScriptedSessionManager returns a manager that yields scripted sessions in
// order.
func NewScriptedSessionManager(commands ...ScriptedCommand) *ScriptedSessionManager {
	return &ScriptedSessionManager{
		commands: cloneScriptedCommands(commands),
		sessions: map[string]*scriptedSessionState{},
	}
}

func (m *ScriptedSessionManager) StartCommand(ctx context.Context, req StartRequest) (CommandSession, error) {
	if err := ctx.Err(); err != nil {
		return CommandSession{}, err
	}
	if m == nil {
		return CommandSession{}, fmt.Errorf("commandtools: nil ScriptedSessionManager")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.startRequests = append(m.startRequests, cloneStartRequest(req))
	if len(m.commands) == 0 {
		return CommandSession{}, fmt.Errorf("commandtools: scripted session manager exhausted")
	}
	cmd := m.commands[0]
	id := strings.TrimSpace(req.ID)
	if id == "" {
		id = strings.TrimSpace(cmd.ID)
	}
	if id == "" {
		id = fmt.Sprintf("cmd-%d", len(m.sessions)+1)
	}
	if _, exists := m.sessions[id]; exists {
		return CommandSession{}, commandSessionError(ErrCommandSessionAlreadyExists, "commandtools: command session %s already exists", id)
	}
	m.commands = m.commands[1:]
	tty := cmd.TTY || req.TTY
	cols := req.Cols
	rows := req.Rows
	if cols == 0 && rows == 0 {
		cols = cmd.Cols
		rows = cmd.Rows
	}
	cols, rows, err := normalizeStartTTYDimensions(tty, cols, rows)
	if err != nil {
		return CommandSession{}, err
	}
	now := time.Now().UTC()
	state := &scriptedSessionState{
		session: CommandSession{
			ID:              id,
			SessionID:       req.SessionID,
			ParentSessionID: req.ParentSessionID,
			Identity:        req.Identity,
			Argv:            append([]string(nil), req.Argv...),
			CWD:             req.CWD,
			Purpose:         req.Purpose,
			Status:          SessionRunning,
			PID:             cmd.PID,
			TTY:             tty,
			Cols:            cols,
			Rows:            rows,
			StartedAt:       now,
		},
		pages:    cloneOutputPages(cmd.Pages),
		writes:   cloneWritePages(cmd.WritePages),
		stopExit: cloneIntPtr(cmd.StopExitCode),
		stopTime: cmd.StopTimedOut,
	}
	m.sessions[id] = state
	return cloneSession(state.session), nil
}

func (m *ScriptedSessionManager) ReadCommandOutput(ctx context.Context, req ReadRequest) (ReadResult, error) {
	if err := ctx.Err(); err != nil {
		return ReadResult{}, err
	}
	if m == nil {
		return ReadResult{}, fmt.Errorf("commandtools: nil ScriptedSessionManager")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.readRequests = append(m.readRequests, cloneReadRequest(req))
	state, err := m.lookupSession(req.SessionID, req.ID)
	if err != nil {
		return ReadResult{}, err
	}
	page := ScriptedOutputPage{Running: state.session.Status == SessionRunning}
	// Plain polling preserves one scripted page per read, including empty running
	// pages. Resumed reads skip pages whose chunks are already covered by AfterSeq
	// until new output appears or a terminal page updates session lifecycle state.
	if req.AfterSeq <= 0 {
		if state.page < len(state.pages) {
			page = cloneOutputPage(state.pages[state.page])
			state.page++
		}
	} else {
		for state.page < len(state.pages) {
			candidate := cloneOutputPage(state.pages[state.page])
			filtered := filterOutputPageAfterSeq(candidate, req.AfterSeq)
			state.page++
			page = filtered
			if len(filtered.Chunks) > 0 || !candidate.Running || state.page >= len(state.pages) {
				break
			}
		}
	}
	limitChunks := req.MaxChunks
	if limitChunks <= 0 {
		limitChunks = defaultReadChunks
	}
	maxBytes := req.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultReadBytes
	}
	chunks := limitOutputChunks(page.Chunks, limitChunks, maxBytes)
	updateSessionFromPage(&state.session, page, chunks)
	return ReadResult{
		Session: cloneSession(state.session),
		Chunks:  cloneOutputChunks(chunks),
		NextSeq: state.session.NextSeq,
	}, nil
}

func (m *ScriptedSessionManager) WriteCommandInput(ctx context.Context, req WriteRequest) (WriteResult, error) {
	if err := ctx.Err(); err != nil {
		return WriteResult{}, err
	}
	if m == nil {
		return WriteResult{}, fmt.Errorf("commandtools: nil ScriptedSessionManager")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writeRequests = append(m.writeRequests, cloneWriteRequest(req))
	state, err := m.lookupSession(req.SessionID, req.ID)
	if err != nil {
		return WriteResult{}, err
	}
	if state.session.Status != SessionRunning {
		return WriteResult{
			Session:    cloneSession(state.session),
			NextSeq:    state.session.NextSeq,
			InputBytes: len(req.Input),
		}, nil
	}
	page := ScriptedWritePage{}
	if state.write < len(state.writes) {
		page = cloneWritePage(state.writes[state.write])
		state.write++
	}
	if page.Error != "" {
		return WriteResult{}, fmt.Errorf("commandtools: %s", page.Error)
	}
	limitChunks := req.MaxChunks
	if limitChunks <= 0 {
		limitChunks = defaultReadChunks
	}
	maxBytes := req.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultReadBytes
	}
	chunks := limitOutputChunks(page.Page.Chunks, limitChunks, maxBytes)
	updateSessionFromPage(&state.session, page.Page, chunks)
	return WriteResult{
		Session:    cloneSession(state.session),
		Chunks:     cloneOutputChunks(chunks),
		NextSeq:    state.session.NextSeq,
		InputBytes: len(req.Input),
	}, nil
}

func (m *ScriptedSessionManager) ResizeCommandTerminal(ctx context.Context, req ResizeRequest) (CommandSession, error) {
	if err := ctx.Err(); err != nil {
		return CommandSession{}, err
	}
	if m == nil {
		return CommandSession{}, fmt.Errorf("commandtools: nil ScriptedSessionManager")
	}
	if err := validateTTYDimensions(req.Cols, req.Rows); err != nil {
		return CommandSession{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resizeRequests = append(m.resizeRequests, cloneResizeRequest(req))
	state, err := m.lookupSession(req.SessionID, req.ID)
	if err != nil {
		return CommandSession{}, err
	}
	if !state.session.TTY {
		return CommandSession{}, commandSessionError(ErrCommandSessionNotPTY, "commandtools: command session %s is not PTY-backed", state.session.ID)
	}
	if state.session.Status != SessionRunning {
		return CommandSession{}, commandSessionError(ErrCommandSessionNotRunning, "commandtools: command session %s is not running", state.session.ID)
	}
	state.session.Cols = req.Cols
	state.session.Rows = req.Rows
	return cloneSession(state.session), nil
}

func (m *ScriptedSessionManager) StopCommand(ctx context.Context, req StopRequest) (CommandSession, error) {
	if err := ctx.Err(); err != nil {
		return CommandSession{}, err
	}
	if m == nil {
		return CommandSession{}, fmt.Errorf("commandtools: nil ScriptedSessionManager")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopRequests = append(m.stopRequests, cloneStopRequest(req))
	state, err := m.lookupSession(req.SessionID, req.ID)
	if err != nil {
		return CommandSession{}, err
	}
	if state.session.Status == SessionRunning {
		now := time.Now().UTC()
		state.session.Status = SessionStopped
		state.session.FinishedAt = &now
		state.session.ExitCode = cloneIntPtr(state.stopExit)
		state.session.TimedOut = state.stopTime
	}
	return cloneSession(state.session), nil
}

func (m *ScriptedSessionManager) ListCommands(ctx context.Context, req ListRequest) ([]CommandSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, fmt.Errorf("commandtools: nil ScriptedSessionManager")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []CommandSession
	for _, state := range m.sessions {
		if req.SessionID != "" && state.session.SessionID != req.SessionID {
			continue
		}
		if !req.IncludeCompleted && state.session.Status != SessionRunning {
			continue
		}
		out = append(out, cloneSession(state.session))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	if req.Limit > 0 && len(out) > req.Limit {
		out = out[:req.Limit]
	}
	return out, nil
}

func (m *ScriptedSessionManager) CleanupSession(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m == nil || sessionID == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, state := range m.sessions {
		if state.session.SessionID == sessionID {
			delete(m.sessions, id)
		}
	}
	return nil
}

// StartRequests returns captured start requests.
func (m *ScriptedSessionManager) StartRequests() []StartRequest {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]StartRequest, len(m.startRequests))
	for i, req := range m.startRequests {
		out[i] = cloneStartRequest(req)
	}
	return out
}

// ReadRequests returns captured read requests.
func (m *ScriptedSessionManager) ReadRequests() []ReadRequest {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ReadRequest, len(m.readRequests))
	for i, req := range m.readRequests {
		out[i] = cloneReadRequest(req)
	}
	return out
}

// WriteRequests returns captured write requests.
func (m *ScriptedSessionManager) WriteRequests() []WriteRequest {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]WriteRequest, len(m.writeRequests))
	for i, req := range m.writeRequests {
		out[i] = cloneWriteRequest(req)
	}
	return out
}

// ResizeRequests returns captured resize requests.
func (m *ScriptedSessionManager) ResizeRequests() []ResizeRequest {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ResizeRequest, len(m.resizeRequests))
	for i, req := range m.resizeRequests {
		out[i] = cloneResizeRequest(req)
	}
	return out
}

// StopRequests returns captured stop requests.
func (m *ScriptedSessionManager) StopRequests() []StopRequest {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]StopRequest, len(m.stopRequests))
	for i, req := range m.stopRequests {
		out[i] = cloneStopRequest(req)
	}
	return out
}

func (m *ScriptedSessionManager) lookupSession(sessionID, id string) (*scriptedSessionState, error) {
	state, ok := m.sessions[id]
	if !ok {
		return nil, commandSessionError(ErrCommandSessionUnknown, "commandtools: unknown command session %s", id)
	}
	if sessionID != "" && state.session.SessionID != "" && state.session.SessionID != sessionID {
		return nil, commandSessionError(ErrCommandSessionNotVisible, "commandtools: command session %s is not visible in this agent session", id)
	}
	return state, nil
}

func updateSessionFromPage(session *CommandSession, page ScriptedOutputPage, chunks []OutputChunk) {
	if session == nil {
		return
	}
	for _, chunk := range chunks {
		if chunk.Seq >= session.NextSeq {
			session.NextSeq = chunk.Seq + 1
		}
	}
	if !page.Running && session.Status == SessionRunning {
		now := time.Now().UTC()
		session.Status = SessionExited
		session.FinishedAt = &now
		session.ExitCode = cloneIntPtr(page.ExitCode)
		session.TimedOut = page.TimedOut
	}
}

func filterOutputPageAfterSeq(page ScriptedOutputPage, afterSeq int) ScriptedOutputPage {
	if afterSeq <= 0 || len(page.Chunks) == 0 {
		return page
	}
	filtered := page
	filtered.Chunks = make([]OutputChunk, 0, len(page.Chunks))
	for _, chunk := range page.Chunks {
		if chunk.Seq > afterSeq {
			filtered.Chunks = append(filtered.Chunks, cloneOutputChunk(chunk))
		}
	}
	if len(filtered.Chunks) == 0 {
		filtered.Chunks = nil
	}
	return filtered
}

func limitOutputChunks(chunks []OutputChunk, maxChunks, maxBytes int) []OutputChunk {
	if len(chunks) == 0 {
		return nil
	}
	if maxChunks <= 0 {
		maxChunks = defaultReadChunks
	}
	if maxBytes <= 0 {
		maxBytes = defaultReadBytes
	}
	var out []OutputChunk
	bytes := 0
	for _, chunk := range chunks {
		if len(out) >= maxChunks {
			break
		}
		if bytes > 0 && bytes+len(chunk.Text) > maxBytes {
			break
		}
		out = append(out, cloneOutputChunk(chunk))
		bytes += len(chunk.Text)
	}
	return out
}

func cloneScriptedCommands(commands []ScriptedCommand) []ScriptedCommand {
	if len(commands) == 0 {
		return nil
	}
	out := make([]ScriptedCommand, len(commands))
	for i, command := range commands {
		out[i] = ScriptedCommand{
			ID:           command.ID,
			PID:          command.PID,
			TTY:          command.TTY,
			Cols:         command.Cols,
			Rows:         command.Rows,
			Pages:        cloneOutputPages(command.Pages),
			WritePages:   cloneWritePages(command.WritePages),
			StopExitCode: cloneIntPtr(command.StopExitCode),
			StopTimedOut: command.StopTimedOut,
		}
	}
	return out
}

func cloneWritePages(pages []ScriptedWritePage) []ScriptedWritePage {
	if len(pages) == 0 {
		return nil
	}
	out := make([]ScriptedWritePage, len(pages))
	for i, page := range pages {
		out[i] = cloneWritePage(page)
	}
	return out
}

func cloneWritePage(page ScriptedWritePage) ScriptedWritePage {
	return ScriptedWritePage{
		Page:  cloneOutputPage(page.Page),
		Error: page.Error,
	}
}

func cloneOutputPages(pages []ScriptedOutputPage) []ScriptedOutputPage {
	if len(pages) == 0 {
		return nil
	}
	out := make([]ScriptedOutputPage, len(pages))
	for i, page := range pages {
		out[i] = cloneOutputPage(page)
	}
	return out
}

func cloneOutputPage(page ScriptedOutputPage) ScriptedOutputPage {
	return ScriptedOutputPage{
		Chunks:   cloneOutputChunks(page.Chunks),
		Running:  page.Running,
		ExitCode: cloneIntPtr(page.ExitCode),
		TimedOut: page.TimedOut,
	}
}

func cloneOutputChunks(chunks []OutputChunk) []OutputChunk {
	if len(chunks) == 0 {
		return nil
	}
	out := make([]OutputChunk, len(chunks))
	for i, chunk := range chunks {
		out[i] = cloneOutputChunk(chunk)
	}
	return out
}

func cloneOutputChunk(chunk OutputChunk) OutputChunk {
	return OutputChunk{
		Seq:    chunk.Seq,
		Stream: chunk.Stream,
		Text:   chunk.Text,
		Time:   chunk.Time,
	}
}

func cloneSession(session CommandSession) CommandSession {
	if len(session.Argv) > 0 {
		session.Argv = append([]string(nil), session.Argv...)
	}
	session.ExitCode = cloneIntPtr(session.ExitCode)
	if session.FinishedAt != nil {
		finished := *session.FinishedAt
		session.FinishedAt = &finished
	}
	return session
}

func cloneIntPtr(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneStartRequest(req StartRequest) StartRequest {
	req.Argv = append([]string(nil), req.Argv...)
	req.Env = cloneStringMap(req.Env)
	req.Metadata = model.CloneMetadata(req.Metadata)
	return req
}

func cloneReadRequest(req ReadRequest) ReadRequest { return req }

func cloneWriteRequest(req WriteRequest) WriteRequest { return req }

func cloneResizeRequest(req ResizeRequest) ResizeRequest { return req }

func cloneStopRequest(req StopRequest) StopRequest { return req }
