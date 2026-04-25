// Package commandtools provides host-owned command execution tools.
//
// The core SDK stays system-neutral: command execution is exposed only when a
// host installs a Runner-backed tool. NewTool gives models a shell-string
// command surface for coding-agent ergonomics; NewExecTool keeps an exact argv
// schema available for hosts that need it. The default OSRunner executes argv
// directly, so approval, audit, and timeout policy can still reason about the
// exact process being launched.
package commandtools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/identity"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/approvaltools"
)

const (
	// ToolName is the default command execution tool name.
	ToolName = "run_command"

	defaultTimeout        = 2 * time.Minute
	defaultMaxTimeout     = 10 * time.Minute
	defaultMaxOutputBytes = 64 * 1024
	defaultMaxStdinBytes  = 64 * 1024
)

// MetadataCommandOperation identifies command tool results.
const (
	MetadataCommandOperation       = model.MetadataCommandOperation
	MetadataCommandArgv            = model.MetadataCommandArgv
	MetadataCommandString          = model.MetadataCommandString
	MetadataCommandCWD             = model.MetadataCommandCWD
	MetadataCommandExitCode        = model.MetadataCommandExitCode
	MetadataCommandTimedOut        = model.MetadataCommandTimedOut
	MetadataCommandDurationMS      = model.MetadataCommandDurationMS
	MetadataCommandStdoutBytes     = model.MetadataCommandStdoutBytes
	MetadataCommandStderrBytes     = model.MetadataCommandStderrBytes
	MetadataCommandOutputTruncated = model.MetadataCommandOutputTruncated
)

// Request describes one command invocation requested by the model.
type Request struct {
	SessionID       string
	ParentSessionID string
	Identity        identity.Identity
	Command         string
	Argv            []string
	CWD             string
	Env             map[string]string
	Stdin           string
	Timeout         time.Duration
	Purpose         string
	Metadata        map[string]any
}

// Result is returned by a Runner. Non-zero exits and timeouts are surfaced to
// the model as tool error results, not caller errors, so the agent can repair
// and retry.
type Result struct {
	Argv            []string
	Command         string
	CWD             string
	ExitCode        int
	TimedOut        bool
	Duration        time.Duration
	Stdout          string
	Stderr          string
	StdoutBytes     int
	StderrBytes     int
	OutputTruncated bool
	Metadata        map[string]any
}

// Runner is implemented by hosts that can execute commands in an
// application-owned environment. Implementations are responsible for their own
// sandboxing, working-directory policy, environment filtering, and process
// lifecycle. Return a Result for process-level failures; return an error for
// runner infrastructure failures.
type Runner interface {
	RunCommand(context.Context, Request) (Result, error)
}

// RunnerFunc adapts a function into a Runner.
type RunnerFunc func(context.Context, Request) (Result, error)

func (f RunnerFunc) RunCommand(ctx context.Context, req Request) (Result, error) {
	if f == nil {
		return Result{}, fmt.Errorf("commandtools: runner is required")
	}
	return f(ctx, req)
}

// Config configures NewTool.
type Config struct {
	Runner          Runner
	Name            string
	Description     string
	SearchHint      string
	MayMutate       bool
	ConcurrencySafe bool
	DefaultTimeout  time.Duration
	MaxTimeout      time.Duration
	MaxStdinBytes   int
	MaxOutputBytes  int
	MaxResultBytes  int
	// Shell is the argv prefix used by NewTool for shell-string commands, for
	// example []string{"bash", "-lc"}. The command string is appended as the
	// final argv element. If Shell is empty, NewTool uses the platform default
	// shell.
	Shell []string
}

// NewTool returns a model-friendly shell command tool backed by a host-owned
// Runner. The tool accepts a single command string and executes it through a
// shell argv prefix, preserving exact executed argv and the original command
// string in result metadata.
func NewTool(config Config) tool.Tool {
	return newTool(config, false)
}

// NewExecTool returns an exact argv command tool backed by a host-owned Runner.
// It is useful for hosts that want process-level execution semantics without an
// implicit shell. Coding agents should generally prefer NewTool.
func NewExecTool(config Config) tool.Tool {
	return newTool(config, true)
}

func newTool(config Config, useExecInput bool) tool.Tool {
	name := strings.TrimSpace(config.Name)
	if name == "" {
		name = ToolName
	}
	description := strings.TrimSpace(config.Description)
	if description == "" {
		if useExecInput {
			description = "Run a host-owned command by exact argv, capture stdout/stderr, and return exit status."
		} else {
			description = "Run a host-owned shell command string, capture stdout/stderr, and return exit status."
		}
	}
	searchHint := strings.TrimSpace(config.SearchHint)
	if searchHint == "" {
		searchHint = "run command test lint build format execute"
	}
	maxResultBytes := config.MaxResultBytes
	if maxResultBytes <= 0 {
		maxResultBytes = defaultMaxOutputBytes
	}
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:            name,
			Description:     description,
			SearchHint:      searchHint,
			ReadOnly:        !config.MayMutate,
			Destructive:     config.MayMutate,
			ConcurrencySafe: config.ConcurrencySafe,
			MaxResultBytes:  maxResultBytes,
			InputSchema:     inputSchema(useExecInput),
		},
		Normalizer: commandToolInputNormalizer(useExecInput),
		Handler: func(ctx context.Context, call tool.Call) (model.ToolResult, error) {
			if config.Runner == nil {
				return model.ToolResult{}, fmt.Errorf("commandtools: runner is required")
			}
			req, err := requestFromToolUse(call.Use, config, useExecInput)
			if err != nil {
				return model.ToolResult{}, err
			}
			if err := validateStdin(req.Stdin, config.MaxStdinBytes); err != nil {
				return model.ToolResult{}, err
			}
			req.SessionID = call.Runtime.SessionID
			req.ParentSessionID = call.Runtime.ParentSessionID
			req.Identity = call.Runtime.Identity
			result, err := config.Runner.RunCommand(ctx, req)
			if err != nil {
				return model.ToolResult{}, err
			}
			result = enforceOutputLimit(result, config.MaxOutputBytes)
			return resultToToolResult(result, req), nil
		},
	}
}

func commandToolInputNormalizer(useExecInput bool) tool.InputNormalizer {
	if useExecInput {
		return normalizeNumericInput("timeout_ms")
	}
	return normalizeShellCommandInput("timeout_ms")
}

func requestFromToolUse(use model.ToolUse, config Config, useExecInput bool) (Request, error) {
	if useExecInput {
		input, err := tool.DecodeInput[execInput](use)
		if err != nil {
			return Request{}, err
		}
		return requestFromExecInput(input, config)
	}
	input, err := tool.DecodeInput[shellInput](use)
	if err != nil {
		return Request{}, err
	}
	return requestFromShellInput(input, config)
}

func validateStdin(stdin string, maxBytes int) error {
	if maxBytes == 0 {
		maxBytes = defaultMaxStdinBytes
	}
	if maxBytes < 0 {
		return nil
	}
	if len(stdin) > maxBytes {
		return fmt.Errorf("commandtools: stdin is %d bytes, exceeds maximum %d", len(stdin), maxBytes)
	}
	return nil
}

// ApprovalSummaryFromRunInput returns a host-facing approval summary for a
// run_command tool input. It is intentionally input-only: it does not inspect
// the target workspace or classify shell syntax.
func ApprovalSummaryFromRunInput(inputBytes []byte) (approvaltools.Summary, error) {
	var input struct {
		Command json.RawMessage `json:"command"`
		Purpose string          `json:"purpose"`
	}
	if len(inputBytes) > 0 {
		if err := json.Unmarshal(inputBytes, &input); err != nil {
			return approvaltools.Summary{}, fmt.Errorf("commandtools: decode run input: %w", err)
		}
	}
	command, err := commandDisplayFromRaw(input.Command)
	if err != nil {
		return approvaltools.Summary{}, err
	}
	if command == "" {
		return approvaltools.Summary{}, nil
	}
	title := "Run command: " + command
	description := strings.TrimSpace(input.Purpose)
	if description == "" {
		description = "Execute host-owned command " + command
	}
	risk := "May read or mutate host-owned state depending on the runner and command."
	return approvaltools.Summary{
		Title:       title,
		Description: description,
		Risk:        risk,
		Changes:     1,
	}, nil
}

func commandDisplayFromRaw(raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", nil
	}
	switch raw[0] {
	case '"':
		var command string
		if err := json.Unmarshal(raw, &command); err != nil {
			return "", fmt.Errorf("commandtools: decode command: %w", err)
		}
		return strings.TrimSpace(command), nil
	case '[':
		var argv []string
		if err := json.Unmarshal(raw, &argv); err != nil {
			return "", fmt.Errorf("commandtools: decode command argv: %w", err)
		}
		return shellQuoteJoin(normalizeArgv(argv)), nil
	default:
		return "", fmt.Errorf("commandtools: command must be a string or argv array")
	}
}

type shellInput struct {
	Command   string            `json:"command"`
	CWD       string            `json:"cwd"`
	Env       map[string]string `json:"env"`
	Stdin     string            `json:"stdin"`
	TimeoutMS int               `json:"timeout_ms"`
	Purpose   string            `json:"purpose"`
	Metadata  map[string]any    `json:"metadata"`
}

type execInput struct {
	Command   []string          `json:"command"`
	CWD       string            `json:"cwd"`
	Env       map[string]string `json:"env"`
	Stdin     string            `json:"stdin"`
	TimeoutMS int               `json:"timeout_ms"`
	Purpose   string            `json:"purpose"`
	Metadata  map[string]any    `json:"metadata"`
}

func requestFromShellInput(input shellInput, config Config) (Request, error) {
	command := strings.TrimSpace(input.Command)
	if command == "" {
		return Request{}, fmt.Errorf("commandtools: command must not be empty")
	}
	argv := shellArgv(command, config.Shell)
	req, err := requestFromCommonInput(commonInput{
		CWD:       input.CWD,
		Env:       input.Env,
		Stdin:     input.Stdin,
		TimeoutMS: input.TimeoutMS,
		Purpose:   input.Purpose,
		Metadata:  input.Metadata,
	}, config)
	if err != nil {
		return Request{}, err
	}
	req.Command = command
	req.Argv = argv
	return req, nil
}

func requestFromExecInput(input execInput, config Config) (Request, error) {
	argv := normalizeArgv(input.Command)
	if len(argv) == 0 {
		return Request{}, fmt.Errorf("commandtools: command must contain at least one argv element")
	}
	req, err := requestFromCommonInput(commonInput{
		CWD:       input.CWD,
		Env:       input.Env,
		Stdin:     input.Stdin,
		TimeoutMS: input.TimeoutMS,
		Purpose:   input.Purpose,
		Metadata:  input.Metadata,
	}, config)
	if err != nil {
		return Request{}, err
	}
	req.Argv = argv
	return req, nil
}

type commonInput struct {
	CWD       string
	Env       map[string]string
	Stdin     string
	TimeoutMS int
	Purpose   string
	Metadata  map[string]any
}

func requestFromCommonInput(input commonInput, config Config) (Request, error) {
	timeout := config.DefaultTimeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	if input.TimeoutMS > 0 {
		timeout = time.Duration(input.TimeoutMS) * time.Millisecond
	}
	maxTimeout := config.MaxTimeout
	if maxTimeout <= 0 {
		maxTimeout = defaultMaxTimeout
	}
	if timeout > maxTimeout {
		return Request{}, fmt.Errorf("commandtools: timeout %s exceeds maximum %s", timeout, maxTimeout)
	}
	return Request{
		CWD:      strings.TrimSpace(input.CWD),
		Env:      cloneStringMap(input.Env),
		Stdin:    input.Stdin,
		Timeout:  timeout,
		Purpose:  strings.TrimSpace(input.Purpose),
		Metadata: model.CloneMetadata(input.Metadata),
	}, nil
}

func resultToToolResult(result Result, req Request) model.ToolResult {
	argv := result.Argv
	if len(argv) == 0 {
		argv = req.Argv
	}
	command := strings.TrimSpace(result.Command)
	if command == "" {
		command = strings.TrimSpace(req.Command)
	}
	cwd := strings.TrimSpace(result.CWD)
	if cwd == "" {
		cwd = req.CWD
	}
	stdoutBytes := result.StdoutBytes
	if stdoutBytes == 0 && result.Stdout != "" {
		stdoutBytes = len(result.Stdout)
	}
	stderrBytes := result.StderrBytes
	if stderrBytes == 0 && result.Stderr != "" {
		stderrBytes = len(result.Stderr)
	}
	metadata := model.CloneMetadata(result.Metadata)
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata[MetadataCommandOperation] = "run"
	metadata[MetadataCommandArgv] = append([]string(nil), argv...)
	if command != "" {
		metadata[MetadataCommandString] = command
	}
	metadata[MetadataCommandCWD] = cwd
	metadata[MetadataCommandExitCode] = result.ExitCode
	metadata[MetadataCommandTimedOut] = result.TimedOut
	metadata[MetadataCommandDurationMS] = int(result.Duration / time.Millisecond)
	metadata[MetadataCommandStdoutBytes] = stdoutBytes
	metadata[MetadataCommandStderrBytes] = stderrBytes
	metadata[MetadataCommandOutputTruncated] = result.OutputTruncated
	return model.ToolResult{
		Content:  formatResult(result, argv, command),
		IsError:  result.TimedOut || result.ExitCode != 0,
		Metadata: metadata,
	}
}

func formatResult(result Result, argv []string, command string) string {
	status := "succeeded"
	if result.TimedOut {
		status = "timed out"
	} else if result.ExitCode != 0 {
		status = "failed"
	}
	display := strings.TrimSpace(command)
	if display == "" {
		display = strings.Join(argv, " ")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "command %s: %s\nexit_code: %d", status, display, result.ExitCode)
	if result.TimedOut {
		b.WriteString("\ntimed_out: true")
	}
	if result.Stdout != "" {
		b.WriteString("\nstdout:\n")
		b.WriteString(result.Stdout)
	}
	if result.Stderr != "" {
		b.WriteString("\nstderr:\n")
		b.WriteString(result.Stderr)
	}
	if result.OutputTruncated {
		b.WriteString("\noutput truncated")
	}
	return b.String()
}

func enforceOutputLimit(result Result, maxBytes int) Result {
	if maxBytes <= 0 {
		maxBytes = defaultMaxOutputBytes
	}
	var truncated bool
	result.Stdout, truncated = limitStringBytes(result.Stdout, maxBytes)
	result.OutputTruncated = result.OutputTruncated || truncated
	result.Stderr, truncated = limitStringBytes(result.Stderr, maxBytes)
	result.OutputTruncated = result.OutputTruncated || truncated
	return result
}

func limitStringBytes(input string, maxBytes int) (string, bool) {
	if maxBytes < 0 || len(input) <= maxBytes {
		return input, false
	}
	if maxBytes == 0 {
		return "", input != ""
	}
	cut := 0
	for i := range input {
		if i > maxBytes {
			return input[:cut], true
		}
		cut = i
	}
	if len(input) > maxBytes {
		return input[:cut], true
	}
	return input[:maxBytes], true
}

func inputSchema(useExecInput bool) map[string]any {
	commandSchema := map[string]any{
		"type":        "string",
		"description": "Shell command string to execute. Pipes, redirects, globs, and shell operators are interpreted by the configured shell.",
		"minLength":   1,
	}
	if useExecInput {
		commandSchema = map[string]any{
			"type":        "array",
			"description": "Command argv vector. The first element is the executable. No shell is implied.",
			"minItems":    1,
			"items":       map[string]any{"type": "string"},
		}
	}
	return map[string]any{
		"type":                 "object",
		"required":             []any{"command"},
		"additionalProperties": false,
		"properties": map[string]any{
			"command": commandSchema,
			"cwd": map[string]any{
				"type":        "string",
				"description": "Optional runner-relative working directory.",
			},
			"env": map[string]any{
				"type":        "object",
				"description": "Optional environment overrides allowed by the runner.",
				"additionalProperties": map[string]any{
					"type": "string",
				},
			},
			"stdin": map[string]any{
				"type":        "string",
				"description": "Optional stdin content.",
			},
			"timeout_ms": map[string]any{
				"type":        "integer",
				"description": "Optional command timeout in milliseconds.",
				"minimum":     1,
			},
			"purpose": map[string]any{
				"type":        "string",
				"description": "Short explanation of why this command is needed.",
			},
			"metadata": map[string]any{
				"type":        "object",
				"description": "Optional host-defined metadata.",
			},
		},
	}
}

// ScriptedRunner is a deterministic concurrency-safe Runner for tests and
// evals. Each call consumes one result.
type ScriptedRunner struct {
	mu       sync.Mutex
	results  []Result
	requests []Request
}

// NewScriptedRunner returns a runner that yields results in order.
func NewScriptedRunner(results ...Result) *ScriptedRunner {
	return &ScriptedRunner{results: cloneResults(results)}
}

func (r *ScriptedRunner) RunCommand(ctx context.Context, req Request) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if r == nil {
		return Result{}, fmt.Errorf("commandtools: nil ScriptedRunner")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, cloneRequest(req))
	if len(r.results) == 0 {
		return Result{}, fmt.Errorf("commandtools: scripted runner exhausted")
	}
	result := cloneResult(r.results[0])
	r.results = r.results[1:]
	if len(result.Argv) == 0 {
		result.Argv = append([]string(nil), req.Argv...)
	}
	if result.Command == "" {
		result.Command = req.Command
	}
	if result.CWD == "" {
		result.CWD = req.CWD
	}
	return result, nil
}

// Requests returns captured command requests.
func (r *ScriptedRunner) Requests() []Request {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Request, len(r.requests))
	for i, req := range r.requests {
		out[i] = cloneRequest(req)
	}
	return out
}

// OSRunner executes argv directly on the local operating system. It is a
// reference adapter for hosts that explicitly choose local command execution;
// it is not used by core SDK packages. Root confinement applies to cwd
// resolution, but it is not an OS sandbox: commands can access anything allowed
// to the host process unless the host wraps OSRunner with its own sandbox,
// allowlist, or operating-system policy.
type OSRunner struct {
	root           string
	inheritEnv     bool
	baseEnv        []string
	maxOutputBytes int
}

// OSRunnerOption configures an OSRunner.
type OSRunnerOption func(*OSRunner)

// WithOSRunnerInheritEnv controls whether commands inherit os.Environ().
// Inheritance is disabled by default because environment variables frequently
// contain credentials and other host secrets.
func WithOSRunnerInheritEnv(enabled bool) OSRunnerOption {
	return func(r *OSRunner) { r.inheritEnv = enabled }
}

// WithOSRunnerEnv sets base environment entries used before request overrides.
func WithOSRunnerEnv(env []string) OSRunnerOption {
	return func(r *OSRunner) { r.baseEnv = append([]string(nil), env...) }
}

// WithOSRunnerMaxOutputBytes sets the retained stdout/stderr byte budget.
func WithOSRunnerMaxOutputBytes(bytes int) OSRunnerOption {
	return func(r *OSRunner) { r.maxOutputBytes = bytes }
}

// NewOSRunner returns a local process runner rooted at root. If root is empty,
// cwd values are resolved relative to the current process directory.
func NewOSRunner(root string, opts ...OSRunnerOption) (*OSRunner, error) {
	r := &OSRunner{
		maxOutputBytes: defaultMaxOutputBytes,
	}
	if root != "" {
		abs, err := filepath.Abs(root)
		if err != nil {
			return nil, fmt.Errorf("commandtools: resolve root: %w", err)
		}
		r.root = abs
	}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}
	if r.maxOutputBytes <= 0 {
		r.maxOutputBytes = defaultMaxOutputBytes
	}
	return r, nil
}

func (r *OSRunner) RunCommand(ctx context.Context, req Request) (Result, error) {
	if r == nil {
		return Result{}, fmt.Errorf("commandtools: nil OSRunner")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	argv := normalizeArgv(req.Argv)
	if len(argv) == 0 {
		return Result{}, fmt.Errorf("commandtools: command must contain at least one argv element")
	}
	cwd, err := r.resolveCWD(req.CWD)
	if err != nil {
		return Result{}, err
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	cmd.Dir = cwd
	cmd.Env = r.env(req.Env)
	if req.Stdin != "" {
		cmd.Stdin = strings.NewReader(req.Stdin)
	}
	stdout := newCappedBuffer(r.maxOutputBytes)
	stderr := newCappedBuffer(r.maxOutputBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	started := time.Now()
	err = cmd.Run()
	duration := time.Since(started)
	result := Result{
		Command:         strings.TrimSpace(req.Command),
		Argv:            append([]string(nil), argv...),
		CWD:             cwd,
		ExitCode:        exitCode(err),
		TimedOut:        runCtx.Err() == context.DeadlineExceeded,
		Duration:        duration,
		Stdout:          stdout.String(),
		Stderr:          stderr.String(),
		StdoutBytes:     stdout.Total(),
		StderrBytes:     stderr.Total(),
		OutputTruncated: stdout.Truncated() || stderr.Truncated(),
	}
	if err == nil || result.TimedOut {
		return result, nil
	}
	var exitErr *exec.ExitError
	if ok := errors.As(err, &exitErr); ok {
		return result, nil
	}
	result.Stderr = strings.TrimSpace(joinNonEmpty(result.Stderr, err.Error()))
	result.StderrBytes = len(result.Stderr)
	return result, nil
}

func (r *OSRunner) resolveCWD(cwd string) (string, error) {
	cwd = strings.TrimSpace(cwd)
	if strings.Contains(cwd, "\x00") {
		return "", fmt.Errorf("commandtools: cwd contains NUL byte")
	}
	if r.root == "" {
		if cwd == "" {
			return os.Getwd()
		}
		if filepath.IsAbs(cwd) {
			return filepath.Clean(cwd), nil
		}
		base, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(base, filepath.FromSlash(cwd)), nil
	}
	if cwd == "" {
		return r.root, nil
	}
	if filepath.IsAbs(cwd) {
		return "", fmt.Errorf("commandtools: cwd must be relative to runner root")
	}
	clean := filepath.Clean(filepath.FromSlash(cwd))
	if clean == "." {
		return r.root, nil
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("commandtools: cwd escapes runner root")
	}
	full := filepath.Join(r.root, clean)
	rel, err := filepath.Rel(r.root, full)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("commandtools: cwd escapes runner root")
	}
	return full, nil
}

func (r *OSRunner) env(overrides map[string]string) []string {
	env := append([]string(nil), r.baseEnv...)
	if !r.inheritEnv && env == nil {
		env = []string{}
	}
	if r.inheritEnv {
		env = append(env, os.Environ()...)
	}
	if len(overrides) == 0 {
		return env
	}
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		env = append(env, key+"="+overrides[key])
	}
	return env
}

type cappedBuffer struct {
	buf       bytes.Buffer
	limit     int
	total     int
	truncated bool
}

func newCappedBuffer(limit int) *cappedBuffer {
	return &cappedBuffer{limit: limit}
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.total += len(p)
	remaining := b.limit - b.buf.Len()
	if remaining > 0 {
		if len(p) <= remaining {
			_, _ = b.buf.Write(p)
		} else {
			_, _ = b.buf.Write(p[:remaining])
			b.truncated = true
		}
	} else if len(p) > 0 {
		b.truncated = true
	}
	return len(p), nil
}

func (b *cappedBuffer) String() string { return b.buf.String() }
func (b *cappedBuffer) Total() int     { return b.total }
func (b *cappedBuffer) Truncated() bool {
	return b.truncated
}

func normalizeArgv(argv []string) []string {
	out := make([]string, 0, len(argv))
	for _, arg := range argv {
		arg = strings.TrimSpace(arg)
		if arg == "" {
			continue
		}
		out = append(out, arg)
	}
	return out
}

func shellArgv(command string, shell []string) []string {
	prefix := normalizeArgv(shell)
	if len(prefix) == 0 {
		if runtime.GOOS == "windows" {
			prefix = []string{"cmd", "/C"}
		} else {
			prefix = []string{"sh", "-c"}
		}
	}
	return append(append([]string(nil), prefix...), command)
}

func cloneRequest(req Request) Request {
	req.Argv = append([]string(nil), req.Argv...)
	req.Env = cloneStringMap(req.Env)
	req.Metadata = model.CloneMetadata(req.Metadata)
	return req
}

func cloneResult(result Result) Result {
	result.Argv = append([]string(nil), result.Argv...)
	result.Metadata = model.CloneMetadata(result.Metadata)
	return result
}

func cloneResults(results []Result) []Result {
	if len(results) == 0 {
		return nil
	}
	out := make([]Result, len(results))
	for i, result := range results {
		out[i] = cloneResult(result)
	}
	return out
}

func cloneStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func joinNonEmpty(parts ...string) string {
	var out []string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return strings.Join(out, "\n")
}
