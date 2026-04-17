package commandtools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultSessionMaxTimeout        = 24 * time.Hour
	defaultSessionStopGrace         = 2 * time.Second
	defaultSessionMaxBufferedBytes  = 256 * 1024
	defaultSessionMaxBufferedChunks = 256
	defaultSessionReadChunkBytes    = 4 * 1024
	// Adapter-generated diagnostics are emitted on stderr so callers can treat
	// them consistently with other non-stdout command output, even for PTY
	// sessions whose process output uses the synthetic "pty" stream label.
	commandToolsErrorStream = "stderr"
	// After a write produces output, allow a short extra window for cmd.Wait to
	// observe a nearly-immediate process exit so write_command_input can return a
	// stable exited session instead of racing the final state handoff.
	postWriteSettleWindow = 100 * time.Millisecond
)

type sessionWaitResult struct {
	exitCode int
	err      error
}

type commandProcess interface {
	PID() int
	Interrupt() error
	Kill() error
	Wait() sessionWaitResult
	Close() error
}

type terminalHandle interface {
	io.ReadWriteCloser
	Resize(cols, rows int) error
}

type terminalDrainCloser interface {
	CloseForDrain() error
}

type sessionRuntime struct {
	process  commandProcess
	stdin    io.WriteCloser
	readers  []osSessionReader
	terminal terminalHandle
}

func (r sessionRuntime) inputWriter() io.WriteCloser {
	if r.stdin != nil {
		return r.stdin
	}
	return r.terminal
}

type execCommandProcess struct {
	cmd *exec.Cmd
}

func newExecCommandProcess(cmd *exec.Cmd) commandProcess {
	if cmd == nil {
		return nil
	}
	return &execCommandProcess{cmd: cmd}
}

func (p *execCommandProcess) PID() int {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

func (p *execCommandProcess) Interrupt() error {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return os.ErrProcessDone
	}
	return p.cmd.Process.Signal(os.Interrupt)
}

func (p *execCommandProcess) Kill() error {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return os.ErrProcessDone
	}
	return p.cmd.Process.Kill()
}

func (p *execCommandProcess) Wait() sessionWaitResult {
	if p == nil || p.cmd == nil {
		return sessionWaitResult{exitCode: -1, err: os.ErrProcessDone}
	}
	err := p.cmd.Wait()
	code := exitCode(err)
	if p.cmd.ProcessState != nil {
		code = p.cmd.ProcessState.ExitCode()
	}
	return sessionWaitResult{exitCode: code, err: err}
}

func (p *execCommandProcess) Close() error { return nil }

// OSSessionManager runs managed command sessions on the local operating
// system. Like OSRunner, it is a reference adapter for hosts that explicitly
// choose local process execution. Root confinement applies to cwd resolution,
// but it is not an OS sandbox: started processes can access anything allowed to
// the host process unless the host wraps OSSessionManager with its own
// sandboxing, allowlists, or operating-system policy.
type OSSessionManager struct {
	mu                sync.RWMutex
	runner            OSRunner
	defaultTimeout    time.Duration
	maxTimeout        time.Duration
	maxStdinBytes     int
	maxBufferedBytes  int
	maxBufferedChunks int
	stopGrace         time.Duration
	nextID            int
	sessions          map[string]*osSessionState
}

// OSSessionManagerOption configures an OSSessionManager.
type OSSessionManagerOption func(*OSSessionManager)

// WithOSSessionManagerInheritEnv controls whether managed command sessions
// inherit os.Environ(). Inheritance is disabled by default because environment
// variables frequently contain credentials and other host secrets.
func WithOSSessionManagerInheritEnv(enabled bool) OSSessionManagerOption {
	return func(m *OSSessionManager) { m.runner.inheritEnv = enabled }
}

// WithOSSessionManagerEnv sets base environment entries used before request
// overrides.
func WithOSSessionManagerEnv(env []string) OSSessionManagerOption {
	return func(m *OSSessionManager) { m.runner.baseEnv = append([]string(nil), env...) }
}

// WithOSSessionManagerDefaultTimeout sets the timeout used when a start
// request omits one.
func WithOSSessionManagerDefaultTimeout(timeout time.Duration) OSSessionManagerOption {
	return func(m *OSSessionManager) { m.defaultTimeout = timeout }
}

// WithOSSessionManagerMaxTimeout sets the maximum allowed managed session
// timeout.
func WithOSSessionManagerMaxTimeout(timeout time.Duration) OSSessionManagerOption {
	return func(m *OSSessionManager) { m.maxTimeout = timeout }
}

// WithOSSessionManagerMaxStdinBytes sets the maximum allowed stdin size for a
// started command session. Negative values disable the limit.
func WithOSSessionManagerMaxStdinBytes(bytes int) OSSessionManagerOption {
	return func(m *OSSessionManager) { m.maxStdinBytes = bytes }
}

// WithOSSessionManagerMaxBufferedBytes sets the retained output byte budget per
// managed session.
func WithOSSessionManagerMaxBufferedBytes(bytes int) OSSessionManagerOption {
	return func(m *OSSessionManager) { m.maxBufferedBytes = bytes }
}

// WithOSSessionManagerMaxBufferedChunks sets the retained output chunk budget
// per managed session.
func WithOSSessionManagerMaxBufferedChunks(chunks int) OSSessionManagerOption {
	return func(m *OSSessionManager) { m.maxBufferedChunks = chunks }
}

// WithOSSessionManagerStopGrace sets the grace period between a best-effort
// interrupt and a forced kill when StopCommand or timeout handling terminates a
// running process. Graceful interrupt delivery is platform dependent; on
// Windows many processes fall back to forced termination immediately.
func WithOSSessionManagerStopGrace(grace time.Duration) OSSessionManagerOption {
	return func(m *OSSessionManager) { m.stopGrace = grace }
}

// NewOSSessionManager returns a local managed-session adapter rooted at root.
// If root is empty, cwd values are resolved relative to the current process
// directory.
func NewOSSessionManager(root string, opts ...OSSessionManagerOption) (*OSSessionManager, error) {
	m := &OSSessionManager{
		defaultTimeout:    defaultSessionTimeout,
		maxTimeout:        defaultSessionMaxTimeout,
		maxStdinBytes:     defaultMaxStdinBytes,
		maxBufferedBytes:  defaultSessionMaxBufferedBytes,
		maxBufferedChunks: defaultSessionMaxBufferedChunks,
		stopGrace:         defaultSessionStopGrace,
		sessions:          map[string]*osSessionState{},
	}
	if root != "" {
		abs, err := filepath.Abs(root)
		if err != nil {
			return nil, fmt.Errorf("commandtools: resolve root: %w", err)
		}
		m.runner.root = abs
	}
	for _, opt := range opts {
		if opt != nil {
			opt(m)
		}
	}
	if m.defaultTimeout <= 0 {
		m.defaultTimeout = defaultSessionTimeout
	}
	if m.maxTimeout == 0 {
		m.maxTimeout = defaultSessionMaxTimeout
	}
	if m.maxStdinBytes == 0 {
		m.maxStdinBytes = defaultMaxStdinBytes
	}
	if m.maxBufferedBytes <= 0 {
		m.maxBufferedBytes = defaultSessionMaxBufferedBytes
	}
	if m.maxBufferedChunks <= 0 {
		m.maxBufferedChunks = defaultSessionMaxBufferedChunks
	}
	if m.stopGrace <= 0 {
		m.stopGrace = defaultSessionStopGrace
	}
	return m, nil
}

func (m *OSSessionManager) StartCommand(ctx context.Context, req StartRequest) (CommandSession, error) {
	if err := ctx.Err(); err != nil {
		return CommandSession{}, err
	}
	if m == nil {
		return CommandSession{}, fmt.Errorf("commandtools: nil OSSessionManager")
	}
	argv := normalizeArgv(req.Argv)
	if len(argv) == 0 {
		return CommandSession{}, fmt.Errorf("commandtools: command must contain at least one argv element")
	}
	if err := validateStdin(req.Stdin, m.maxStdinBytes); err != nil {
		return CommandSession{}, err
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = m.defaultTimeout
	}
	if m.maxTimeout > 0 && timeout > m.maxTimeout {
		return CommandSession{}, fmt.Errorf("commandtools: timeout %s exceeds maximum %s", timeout, m.maxTimeout)
	}
	cols, rows, err := normalizeStartTTYDimensions(req.TTY, req.Cols, req.Rows)
	if err != nil {
		return CommandSession{}, err
	}
	req.Cols = cols
	req.Rows = rows
	cwd, err := m.runner.resolveCWD(req.CWD)
	if err != nil {
		return CommandSession{}, err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = cwd
	cmd.Env = m.runner.env(req.Env)
	runtime, err := startSessionIO(cmd, req)
	if err != nil {
		return CommandSession{}, err
	}
	if runtime.process == nil {
		return CommandSession{}, fmt.Errorf("commandtools: start command: missing process handle")
	}

	now := time.Now().UTC()
	state := &osSessionState{
		manager:  m,
		process:  runtime.process,
		stdin:    runtime.stdin,
		terminal: runtime.terminal,
		session: CommandSession{
			SessionID:       req.SessionID,
			ParentSessionID: req.ParentSessionID,
			Identity:        req.Identity,
			Argv:            append([]string(nil), argv...),
			CWD:             cwd,
			Purpose:         strings.TrimSpace(req.Purpose),
			Status:          SessionRunning,
			PID:             runtime.process.PID(),
			TTY:             req.TTY,
			Cols:            req.Cols,
			Rows:            req.Rows,
			StartedAt:       now,
			NextSeq:         1,
		},
		done:    make(chan struct{}),
		updates: make(chan struct{}, 1),
	}
	if timeout > 0 {
		state.timer = time.AfterFunc(timeout, state.timeout)
	}

	m.mu.Lock()
	id := strings.TrimSpace(req.ID)
	if id != "" {
		if _, exists := m.sessions[id]; exists {
			m.mu.Unlock()
			cleanupFailedSessionRuntime(runtime)
			return CommandSession{}, fmt.Errorf("commandtools: command session %s already exists", id)
		}
	} else {
		for {
			m.nextID++
			id = fmt.Sprintf("cmd-%d", m.nextID)
			if _, exists := m.sessions[id]; !exists {
				break
			}
		}
	}
	state.session.ID = id
	m.sessions[id] = state
	m.mu.Unlock()

	if req.Stdin != "" {
		input := runtime.inputWriter()
		if input == nil {
			m.mu.Lock()
			delete(m.sessions, state.session.ID)
			m.mu.Unlock()
			cleanupFailedSessionRuntime(runtime)
			return CommandSession{}, fmt.Errorf("commandtools: command session %s stdin is closed", state.session.ID)
		}
		if _, err := io.WriteString(input, req.Stdin); err != nil {
			m.mu.Lock()
			delete(m.sessions, state.session.ID)
			m.mu.Unlock()
			cleanupFailedSessionRuntime(runtime)
			return CommandSession{}, fmt.Errorf("commandtools: write initial stdin: %w", err)
		}
	}

	doneChans := make([]chan struct{}, len(runtime.readers))
	for i, reader := range runtime.readers {
		doneChans[i] = make(chan struct{})
		go state.captureStream(reader.stream, reader.reader, reader.closer, doneChans[i])
	}
	go state.waitAndFinish(doneChans)

	return state.snapshot(), nil
}

func (m *OSSessionManager) ReadCommandOutput(ctx context.Context, req ReadRequest) (ReadResult, error) {
	if err := ctx.Err(); err != nil {
		return ReadResult{}, err
	}
	if m == nil {
		return ReadResult{}, fmt.Errorf("commandtools: nil OSSessionManager")
	}
	state, err := m.lookupSession(req.SessionID, req.ID)
	if err != nil {
		return ReadResult{}, err
	}
	return state.read(req.AfterSeq, req.MaxChunks, req.MaxBytes), nil
}

func (m *OSSessionManager) WriteCommandInput(ctx context.Context, req WriteRequest) (WriteResult, error) {
	if err := ctx.Err(); err != nil {
		return WriteResult{}, err
	}
	if m == nil {
		return WriteResult{}, fmt.Errorf("commandtools: nil OSSessionManager")
	}
	if req.Yield < 0 {
		return WriteResult{}, fmt.Errorf("commandtools: yield must be non-negative")
	}
	if err := validateStdin(req.Input, m.maxStdinBytes); err != nil {
		return WriteResult{}, err
	}
	state, err := m.lookupSession(req.SessionID, req.ID)
	if err != nil {
		return WriteResult{}, err
	}
	return state.writeInput(ctx, req)
}

func (m *OSSessionManager) ResizeCommandTerminal(ctx context.Context, req ResizeRequest) (CommandSession, error) {
	if err := ctx.Err(); err != nil {
		return CommandSession{}, err
	}
	if m == nil {
		return CommandSession{}, fmt.Errorf("commandtools: nil OSSessionManager")
	}
	if err := validateTTYDimensions(req.Cols, req.Rows); err != nil {
		return CommandSession{}, err
	}
	state, err := m.lookupSession(req.SessionID, req.ID)
	if err != nil {
		return CommandSession{}, err
	}
	return state.resizeTerminal(ctx, req)
}

func (m *OSSessionManager) StopCommand(ctx context.Context, req StopRequest) (CommandSession, error) {
	if err := ctx.Err(); err != nil {
		return CommandSession{}, err
	}
	if m == nil {
		return CommandSession{}, fmt.Errorf("commandtools: nil OSSessionManager")
	}
	state, err := m.lookupSession(req.SessionID, req.ID)
	if err != nil {
		return CommandSession{}, err
	}
	return state.stop(ctx, req.Force)
}

func (m *OSSessionManager) ListCommands(ctx context.Context, req ListRequest) ([]CommandSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, fmt.Errorf("commandtools: nil OSSessionManager")
	}
	m.mu.RLock()
	states := make([]*osSessionState, 0, len(m.sessions))
	for _, state := range m.sessions {
		states = append(states, state)
	}
	m.mu.RUnlock()
	out := make([]CommandSession, 0, len(states))
	for _, state := range states {
		session := state.snapshot()
		if req.SessionID != "" && session.SessionID != req.SessionID {
			continue
		}
		if !req.IncludeCompleted && session.Status != SessionRunning {
			continue
		}
		out = append(out, session)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].StartedAt.Before(out[j].StartedAt)
	})
	if req.Limit > 0 && len(out) > req.Limit {
		out = out[:req.Limit]
	}
	return out, nil
}

func (m *OSSessionManager) CleanupSession(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m == nil || sessionID == "" {
		return nil
	}
	m.mu.RLock()
	var states []*osSessionState
	for _, state := range m.sessions {
		session := state.snapshot()
		if session.SessionID == sessionID {
			states = append(states, state)
		}
	}
	m.mu.RUnlock()
	var joined error
	for _, state := range states {
		stopCtx, cancel := context.WithTimeout(context.Background(), m.stopGrace*2)
		_, err := state.stop(stopCtx, true)
		cancel()
		if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			joined = errors.Join(joined, err)
		}
		m.mu.Lock()
		delete(m.sessions, state.snapshot().ID)
		m.mu.Unlock()
	}
	return joined
}

func (m *OSSessionManager) lookupSession(sessionID, id string) (*osSessionState, error) {
	m.mu.RLock()
	state, ok := m.sessions[id]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("commandtools: unknown command session %s", id)
	}
	session := state.snapshot()
	if sessionID != "" && session.SessionID != "" && session.SessionID != sessionID {
		return nil, fmt.Errorf("commandtools: command session %s is not visible in this agent session", id)
	}
	return state, nil
}

func cleanupFailedSessionRuntime(runtime sessionRuntime) {
	if runtime.process != nil {
		_ = runtime.process.Kill()
		runtime.process.Wait()
		_ = runtime.process.Close()
	}
	if runtime.stdin != nil {
		_ = runtime.stdin.Close()
	}
	if runtime.terminal != nil {
		_ = runtime.terminal.Close()
	}
}

func startSessionIO(cmd *exec.Cmd, req StartRequest) (sessionRuntime, error) {
	if req.TTY {
		terminal, process, err := startPTYCommand(cmd, req.Cols, req.Rows)
		if err != nil {
			return sessionRuntime{}, err
		}
		return sessionRuntime{
			process:  process,
			terminal: terminal,
			readers: []osSessionReader{{
				stream: "pty",
				reader: terminal,
			}},
		}, nil
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return sessionRuntime{}, fmt.Errorf("commandtools: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return sessionRuntime{}, fmt.Errorf("commandtools: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		return sessionRuntime{}, fmt.Errorf("commandtools: stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return sessionRuntime{}, fmt.Errorf("commandtools: start command: %w", err)
	}
	return sessionRuntime{
		process: newExecCommandProcess(cmd),
		stdin:   stdin,
		readers: []osSessionReader{
			{stream: "stdout", reader: stdout, closer: stdout},
			{stream: "stderr", reader: stderr, closer: stderr},
		},
	}, nil
}

type osSessionState struct {
	manager  *OSSessionManager
	mu       sync.RWMutex
	process  commandProcess
	stdin    io.WriteCloser
	terminal terminalHandle
	// draining becomes true once a PTY-backed session has closed its terminal to
	// let blocked readers observe EOF after process exit. It stays true for the
	// rest of the session so post-drain writes and resizes consistently report
	// "not running" rather than transiently surfacing closed-descriptor errors.
	draining      bool
	session       CommandSession
	output        []OutputChunk
	bufferBytes   int
	stopRequested bool
	timedOut      bool
	done          chan struct{}
	updates       chan struct{}
	timer         *time.Timer
	finishOnce    sync.Once
	// stdinMu protects stdin/terminal and must be acquired before any work that
	// touches the live PTY descriptor.
	stdinMu sync.Mutex
}

type osSessionReader struct {
	stream string
	reader io.Reader
	closer io.Closer
}

func (s *osSessionState) snapshot() CommandSession {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneSession(s.session)
}

func (s *osSessionState) read(afterSeq, maxChunks, maxBytes int) ReadResult {
	if maxChunks <= 0 {
		maxChunks = defaultReadChunks
	}
	if maxBytes <= 0 {
		maxBytes = defaultReadBytes
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	session := cloneSession(s.session)
	var chunks []OutputChunk
	bytes := 0
	for _, chunk := range s.output {
		if chunk.Seq <= afterSeq {
			continue
		}
		if len(chunks) >= maxChunks {
			break
		}
		if bytes > 0 && bytes+len(chunk.Text) > maxBytes {
			break
		}
		chunks = append(chunks, cloneOutputChunk(chunk))
		bytes += len(chunk.Text)
	}
	return ReadResult{
		Session: session,
		Chunks:  chunks,
		NextSeq: session.NextSeq,
	}
}

func (s *osSessionState) writeInput(ctx context.Context, req WriteRequest) (WriteResult, error) {
	session := s.snapshot()
	if session.Status != SessionRunning {
		return WriteResult{
			Session:    session,
			NextSeq:    session.NextSeq,
			InputBytes: len(req.Input),
		}, nil
	}
	afterSeq := maxInt(0, session.NextSeq-1)
	if req.Input != "" {
		s.stdinMu.Lock()
		input := s.stdin
		if input == nil {
			input = s.terminal
		}
		if input == nil {
			s.stdinMu.Unlock()
			if s.isDraining() {
				return WriteResult{}, fmt.Errorf("commandtools: command session %s is not running", session.ID)
			}
			return WriteResult{}, fmt.Errorf("commandtools: command session %s stdin is closed", session.ID)
		}
		_, err := io.WriteString(input, req.Input)
		s.stdinMu.Unlock()
		if err != nil {
			return WriteResult{}, fmt.Errorf("commandtools: write input to command session %s: %w", session.ID, err)
		}
	}
	if err := s.waitForUpdate(ctx, afterSeq, req.Yield); err != nil {
		return WriteResult{}, err
	}
	result := s.read(afterSeq, req.MaxChunks, req.MaxBytes)
	return WriteResult{
		Session:    result.Session,
		Chunks:     result.Chunks,
		NextSeq:    result.NextSeq,
		InputBytes: len(req.Input),
	}, nil
}

func (s *osSessionState) resizeTerminal(ctx context.Context, req ResizeRequest) (CommandSession, error) {
	s.stdinMu.Lock()
	defer s.stdinMu.Unlock()
	session := s.snapshot()
	if !session.TTY {
		return CommandSession{}, fmt.Errorf("commandtools: command session %s is not PTY-backed", session.ID)
	}
	if session.Status != SessionRunning {
		return CommandSession{}, fmt.Errorf("commandtools: command session %s is not running", session.ID)
	}
	terminal := s.terminal
	if terminal == nil {
		if s.isDraining() {
			return CommandSession{}, fmt.Errorf("commandtools: command session %s is not running", session.ID)
		}
		return CommandSession{}, fmt.Errorf("commandtools: command session %s terminal is closed", session.ID)
	}
	if err := terminal.Resize(req.Cols, req.Rows); err != nil {
		return CommandSession{}, fmt.Errorf("commandtools: resize command session %s: %w", session.ID, err)
	}
	s.mu.Lock()
	s.session.Cols = req.Cols
	s.session.Rows = req.Rows
	s.mu.Unlock()
	s.signalUpdate()
	if err := ctx.Err(); err != nil {
		return CommandSession{}, err
	}
	return s.snapshot(), nil
}

func (s *osSessionState) waitForUpdate(ctx context.Context, afterSeq int, yield time.Duration) error {
	if yield <= 0 {
		return nil
	}
	deadline := time.NewTimer(yield + postWriteSettleWindow)
	defer deadline.Stop()
	var settleTimer *time.Timer
	var settle <-chan time.Time
	defer func() {
		if settleTimer != nil {
			settleTimer.Stop()
		}
	}()
	advanced := false
	resetSettle := func() {
		if settleTimer == nil {
			settleTimer = time.NewTimer(postWriteSettleWindow)
			settle = settleTimer.C
			return
		}
		if !settleTimer.Stop() {
			select {
			case <-settleTimer.C:
			default:
			}
		}
		settleTimer.Reset(postWriteSettleWindow)
	}
	for {
		if s.hasAdvanced(afterSeq) {
			advanced = true
			resetSettle()
		}
		if advanced && !s.isRunning() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.done:
			return nil
		case <-s.updates:
			if s.hasAdvanced(afterSeq) {
				advanced = true
				resetSettle()
			}
		case <-settle:
			if advanced {
				return nil
			}
		case <-deadline.C:
			return nil
		}
	}
}

func (s *osSessionState) hasAdvanced(afterSeq int) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.session.NextSeq-1 > afterSeq || s.session.Status != SessionRunning
}

func (s *osSessionState) isRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.session.Status == SessionRunning
}

func (s *osSessionState) isDraining() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.draining
}

func (s *osSessionState) stop(ctx context.Context, force bool) (CommandSession, error) {
	session := s.snapshot()
	if session.Status != SessionRunning {
		return session, nil
	}
	s.mu.Lock()
	if s.timer != nil {
		s.timer.Stop()
	}
	s.stopRequested = true
	process := s.process
	done := s.done
	s.mu.Unlock()
	if process == nil {
		select {
		case <-done:
			return s.snapshot(), nil
		case <-ctx.Done():
			return CommandSession{}, ctx.Err()
		}
	}
	if force {
		if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return CommandSession{}, fmt.Errorf("commandtools: kill command session %s: %w", session.ID, err)
		}
	} else {
		// Wait in two phases: first for graceful interrupt completion, then again
		// after a forced kill so cmd.Wait and the capture goroutines can drain.
		err := process.Interrupt()
		if err != nil && !errors.Is(err, os.ErrProcessDone) {
			if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				return CommandSession{}, fmt.Errorf("commandtools: stop command session %s: %w", session.ID, err)
			}
		} else {
			timer := time.NewTimer(s.manager.stopGrace)
			defer timer.Stop()
			select {
			case <-done:
				return s.snapshot(), nil
			case <-ctx.Done():
				return CommandSession{}, ctx.Err()
			case <-timer.C:
			}
			if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				return CommandSession{}, fmt.Errorf("commandtools: force-stop command session %s: %w", session.ID, err)
			}
		}
	}
	select {
	case <-done:
		return s.snapshot(), nil
	case <-ctx.Done():
		return CommandSession{}, ctx.Err()
	}
}

func (s *osSessionState) timeout() {
	s.mu.Lock()
	if s.session.Status != SessionRunning {
		s.mu.Unlock()
		return
	}
	s.timedOut = true
	process := s.process
	done := s.done
	s.mu.Unlock()
	if process == nil {
		return
	}
	_ = process.Interrupt()
	timer := time.NewTimer(s.manager.stopGrace)
	defer timer.Stop()
	select {
	case <-done:
		return
	case <-timer.C:
	}
	_ = process.Kill()
}

func (s *osSessionState) waitAndFinish(doneChans []chan struct{}) {
	waitResult := sessionWaitResult{exitCode: -1, err: os.ErrProcessDone}
	if s.process != nil {
		waitResult = s.process.Wait()
	}
	// ConPTY keeps the output pipe readable until the pseudo console itself is
	// closed. CloseForDrain is a PTY-specific hook that lets those readers
	// observe EOF after process exit without changing Unix PTY behavior.
	if err := s.closeTerminalForDrain(); err != nil {
		s.appendOutput(commandToolsErrorStream, fmt.Sprintf("[commandtools] close terminal for drain error: %v\n", err))
	}
	for _, done := range doneChans {
		<-done
	}
	s.finish(waitResult)
}

func (s *osSessionState) closeTerminalForDrain() error {
	s.stdinMu.Lock()
	defer s.stdinMu.Unlock()
	closer, ok := s.terminal.(terminalDrainCloser)
	if !ok || closer == nil {
		return nil
	}
	// Publish terminal=nil before finish flips the session status so blocked PTY
	// readers can observe EOF and complete the drain phase.
	err := closer.CloseForDrain()
	s.terminal = nil
	s.mu.Lock()
	s.draining = true
	s.mu.Unlock()
	return err
}

func (s *osSessionState) captureStream(stream string, reader io.Reader, closer io.Closer, done chan<- struct{}) {
	defer close(done)
	if closer != nil {
		defer closer.Close()
	}
	buf := make([]byte, defaultSessionReadChunkBytes)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			s.appendOutput(stream, string(buf[:n]))
		}
		if err != nil {
			if errors.Is(err, io.EOF) || (stream == "pty" && isPTYEOFError(err)) {
				return
			}
			s.appendOutput(commandToolsErrorStream, fmt.Sprintf("[commandtools] read %s error: %v\n", stream, err))
			return
		}
	}
}

func (s *osSessionState) appendOutput(stream, text string) {
	if text == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seq := s.session.NextSeq
	chunk := OutputChunk{
		Seq:    seq,
		Stream: stream,
		Text:   text,
		Time:   time.Now().UTC(),
	}
	s.session.NextSeq++
	s.output = append(s.output, chunk)
	s.bufferBytes += len(text)
	for len(s.output) > s.manager.maxBufferedChunks || s.bufferBytes > s.manager.maxBufferedBytes {
		dropped := s.output[0]
		s.output = s.output[1:]
		s.bufferBytes -= len(dropped.Text)
		s.session.DroppedChunks++
		s.session.DroppedBytes += len(dropped.Text)
	}
	s.signalUpdate()
}

func (s *osSessionState) finish(result sessionWaitResult) {
	s.finishOnce.Do(func() {
		s.mu.Lock()
		if s.timer != nil {
			s.timer.Stop()
		}
		now := time.Now().UTC()
		status := SessionExited
		if s.stopRequested {
			status = SessionStopped
		}
		s.session.Status = status
		s.session.FinishedAt = &now
		exit := result.exitCode
		s.session.ExitCode = &exit
		s.session.TimedOut = s.timedOut
		s.mu.Unlock()
		s.stdinMu.Lock()
		if s.stdin != nil {
			_ = s.stdin.Close()
			s.stdin = nil
		}
		if s.terminal != nil {
			_ = s.terminal.Close()
			s.terminal = nil
		}
		s.stdinMu.Unlock()
		if s.process != nil {
			_ = s.process.Close()
		}
		s.signalUpdate()
		close(s.done)
	})
}

func (s *osSessionState) signalUpdate() {
	if s == nil || s.updates == nil {
		return
	}
	select {
	case s.updates <- struct{}{}:
	default:
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
