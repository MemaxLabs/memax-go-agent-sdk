package commandtools

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOSSessionManagerStartReadNaturalExitAndVisibility(t *testing.T) {
	root := t.TempDir()
	appDir := filepath.Join(root, "app")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	manager, err := NewOSSessionManager(root)
	if err != nil {
		t.Fatalf("NewOSSessionManager returned error: %v", err)
	}
	started, err := manager.StartCommand(context.Background(), StartRequest{
		SessionID: "session-1",
		Argv: []string{
			os.Args[0],
			"-test.run=TestHelperProcess",
			"--",
			"session",
			"ready-then-finish",
			"500ms",
		},
		CWD: "app",
		Env: map[string]string{"GO_WANT_HELPER_PROCESS": "1"},
	})
	if err != nil {
		t.Fatalf("StartCommand returned error: %v", err)
	}
	if started.Status != SessionRunning || started.PID == 0 {
		t.Fatalf("started = %#v, want running session with pid", started)
	}
	if started.CWD != appDir {
		t.Fatalf("started.CWD = %q, want %q", started.CWD, appDir)
	}

	sessions, err := manager.ListCommands(context.Background(), ListRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatalf("ListCommands returned error: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != started.ID || sessions[0].Status != SessionRunning {
		t.Fatalf("sessions = %#v, want one visible running session", sessions)
	}
	otherSessions, err := manager.ListCommands(context.Background(), ListRequest{SessionID: "session-2"})
	if err != nil {
		t.Fatalf("ListCommands other returned error: %v", err)
	}
	if len(otherSessions) != 0 {
		t.Fatalf("otherSessions = %#v, want no cross-session visibility", otherSessions)
	}
	if _, err := manager.ReadCommandOutput(context.Background(), ReadRequest{SessionID: "session-2", ID: started.ID}); err == nil || !strings.Contains(err.Error(), "not visible") {
		t.Fatalf("ReadCommandOutput cross-session error = %v, want visibility error", err)
	}

	first := waitForOutput(t, manager, ReadRequest{SessionID: "session-1", ID: started.ID}, func(result ReadResult) bool {
		return strings.Contains(joinChunkText(result.Chunks), "ready\n")
	})

	final := waitForOutput(t, manager, ReadRequest{
		SessionID: "session-1",
		ID:        started.ID,
		AfterSeq:  max(0, first.NextSeq-1),
	}, func(result ReadResult) bool {
		return result.Session.Status == SessionExited
	})
	if final.Session.Status != SessionExited || final.Session.ExitCode == nil || *final.Session.ExitCode != 0 {
		t.Fatalf("final = %#v, want exited session with zero exit code", final)
	}
}

func TestOSSessionManagerStopAndCleanup(t *testing.T) {
	manager, err := NewOSSessionManager(t.TempDir(), WithOSSessionManagerStopGrace(100*time.Millisecond))
	if err != nil {
		t.Fatalf("NewOSSessionManager returned error: %v", err)
	}
	first, err := manager.StartCommand(context.Background(), StartRequest{
		SessionID: "session-1",
		Argv: []string{
			os.Args[0],
			"-test.run=TestHelperProcess",
			"--",
			"session",
			"linger",
		},
		Env: map[string]string{"GO_WANT_HELPER_PROCESS": "1"},
	})
	if err != nil {
		t.Fatalf("StartCommand first returned error: %v", err)
	}
	_ = waitForOutput(t, manager, ReadRequest{SessionID: "session-1", ID: first.ID}, func(result ReadResult) bool {
		return strings.Contains(joinChunkText(result.Chunks), "ready\n")
	})
	stopped, err := manager.StopCommand(context.Background(), StopRequest{SessionID: "session-1", ID: first.ID})
	if err != nil {
		t.Fatalf("StopCommand returned error: %v", err)
	}
	if stopped.Status != SessionStopped || stopped.ExitCode == nil {
		t.Fatalf("stopped = %#v, want stopped session with exit code", stopped)
	}

	second, err := manager.StartCommand(context.Background(), StartRequest{
		SessionID: "session-2",
		Argv: []string{
			os.Args[0],
			"-test.run=TestHelperProcess",
			"--",
			"session",
			"linger",
		},
		Env: map[string]string{"GO_WANT_HELPER_PROCESS": "1"},
	})
	if err != nil {
		t.Fatalf("StartCommand second returned error: %v", err)
	}
	if err := manager.CleanupSession(context.Background(), "session-2"); err != nil {
		t.Fatalf("CleanupSession returned error: %v", err)
	}
	sessions, err := manager.ListCommands(context.Background(), ListRequest{SessionID: "session-2", IncludeCompleted: true})
	if err != nil {
		t.Fatalf("ListCommands after cleanup returned error: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("sessions = %#v, want cleanup to remove session-owned commands", sessions)
	}
	if _, err := manager.ReadCommandOutput(context.Background(), ReadRequest{SessionID: "session-2", ID: second.ID}); err == nil || !strings.Contains(err.Error(), "unknown command session") {
		t.Fatalf("ReadCommandOutput after cleanup error = %v, want unknown session", err)
	}
}

func TestOSSessionManagerWriteInputEchoesAndExits(t *testing.T) {
	manager, err := NewOSSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewOSSessionManager returned error: %v", err)
	}
	started, err := manager.StartCommand(context.Background(), StartRequest{
		SessionID: "session-1",
		Argv: []string{
			os.Args[0],
			"-test.run=TestHelperProcess",
			"--",
			"session",
			"echo-stdin",
		},
		Env: map[string]string{"GO_WANT_HELPER_PROCESS": "1"},
	})
	if err != nil {
		t.Fatalf("StartCommand returned error: %v", err)
	}
	_ = waitForOutput(t, manager, ReadRequest{SessionID: "session-1", ID: started.ID}, func(result ReadResult) bool {
		return strings.Contains(joinChunkText(result.Chunks), "ready\n")
	})

	wrote, err := manager.WriteCommandInput(context.Background(), WriteRequest{
		SessionID: "session-1",
		ID:        started.ID,
		Input:     "hello\n",
		Yield:     500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("WriteCommandInput hello returned error: %v", err)
	}
	if !strings.Contains(joinChunkText(wrote.Chunks), "echo:hello\n") || wrote.InputBytes != len("hello\n") {
		t.Fatalf("wrote = %#v, want echoed output and input byte count", wrote)
	}

	exited, err := manager.WriteCommandInput(context.Background(), WriteRequest{
		SessionID: "session-1",
		ID:        started.ID,
		Input:     "exit\n",
		Yield:     500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("WriteCommandInput exit returned error: %v", err)
	}
	if !strings.Contains(joinChunkText(exited.Chunks), "bye\n") {
		t.Fatalf("exited = %#v, want exit output chunk", exited)
	}
	final := waitForOutput(t, manager, ReadRequest{
		SessionID: "session-1",
		ID:        started.ID,
		AfterSeq:  max(0, exited.NextSeq-1),
	}, func(result ReadResult) bool {
		return result.Session.Status == SessionExited
	})
	if final.Session.ExitCode == nil || *final.Session.ExitCode != 0 {
		t.Fatalf("final = %#v, want exited session after exit input", final)
	}
}

func TestOSSessionManagerBoundsInheritedStdoutDrain(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fd-inheritance fixture is Unix-specific")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh not available: %v", err)
	}
	manager, err := NewOSSessionManager(t.TempDir(), WithOSSessionManagerDrainTimeout(25*time.Millisecond))
	if err != nil {
		t.Fatalf("NewOSSessionManager returned error: %v", err)
	}
	started, err := manager.StartCommand(context.Background(), StartRequest{
		SessionID: "session-1",
		Argv:      []string{sh, "-c", "sleep 1 & printf 'ready\\n'"},
	})
	if err != nil {
		t.Fatalf("StartCommand returned error: %v", err)
	}
	result := waitForOutput(t, manager, ReadRequest{SessionID: "session-1", ID: started.ID}, func(result ReadResult) bool {
		return result.Session.Status == SessionExited
	})
	if result.Session.ExitCode == nil || *result.Session.ExitCode != 0 {
		t.Fatalf("session = %#v, want exited session with zero exit code", result.Session)
	}
	output := joinChunkText(result.Chunks)
	if !strings.Contains(output, "ready\n") {
		t.Fatalf("output = %q, want shell output", output)
	}
	if !strings.Contains(output, "output drain timed out") {
		t.Fatalf("output = %q, want forced drain diagnostic for inherited stdout", output)
	}
}

func TestOSSessionManagerTTYSessionUsesPTYStream(t *testing.T) {
	manager, err := NewOSSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewOSSessionManager returned error: %v", err)
	}
	started, err := manager.StartCommand(context.Background(), StartRequest{
		SessionID: "session-1",
		TTY:       true,
		Argv: []string{
			os.Args[0],
			"-test.run=TestHelperProcess",
			"--",
			"session",
			"tty-echo",
		},
		Env: map[string]string{"GO_WANT_HELPER_PROCESS": "1"},
	})
	if err != nil {
		if strings.Contains(err.Error(), "PTY sessions are not supported") {
			t.Skipf("pty unsupported on %s: %v", runtime.GOOS, err)
		}
		t.Fatalf("StartCommand returned error: %v", err)
	}
	if !started.TTY {
		t.Fatalf("started = %#v, want tty session", started)
	}
	ready := waitForOutput(t, manager, ReadRequest{SessionID: "session-1", ID: started.ID}, func(result ReadResult) bool {
		return strings.Contains(joinChunkText(result.Chunks), "tty-ready")
	})
	if len(ready.Chunks) == 0 || ready.Chunks[0].Stream != "pty" {
		t.Fatalf("ready = %#v, want pty stream", ready)
	}

	wrote, err := manager.WriteCommandInput(context.Background(), WriteRequest{
		SessionID: "session-1",
		ID:        started.ID,
		Input:     "hello\n",
		Yield:     500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("WriteCommandInput hello returned error: %v", err)
	}
	if !strings.Contains(joinChunkText(wrote.Chunks), "tty:hello") {
		t.Fatalf("wrote = %#v, want tty echo output", wrote)
	}

	exited, err := manager.WriteCommandInput(context.Background(), WriteRequest{
		SessionID: "session-1",
		ID:        started.ID,
		Input:     "exit\n",
		Yield:     500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("WriteCommandInput exit returned error: %v", err)
	}
	if !strings.Contains(joinChunkText(exited.Chunks), "tty-bye") {
		t.Fatalf("exited = %#v, want tty exit output", exited)
	}
}

func TestOSSessionManagerDoesNotInheritEnvByDefault(t *testing.T) {
	t.Setenv("MEMAX_COMMANDTOOLS_TEST_SECRET", "secret")
	manager, err := NewOSSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewOSSessionManager returned error: %v", err)
	}
	started, err := manager.StartCommand(context.Background(), StartRequest{
		SessionID: "session-1",
		Argv: []string{
			os.Args[0],
			"-test.run=TestHelperProcess",
			"--",
			"env",
			"MEMAX_COMMANDTOOLS_TEST_SECRET",
		},
		Env: map[string]string{"GO_WANT_HELPER_PROCESS": "1"},
	})
	if err != nil {
		t.Fatalf("StartCommand returned error: %v", err)
	}
	result := waitForOutput(t, manager, ReadRequest{SessionID: "session-1", ID: started.ID}, func(result ReadResult) bool {
		return result.Session.Status == SessionExited
	})
	if strings.Contains(joinChunkText(result.Chunks), "secret") {
		t.Fatalf("result chunks = %#v, want no inherited secret", result.Chunks)
	}
}

func TestOSSessionManagerRejectsRootEscapeCWD(t *testing.T) {
	manager, err := NewOSSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewOSSessionManager returned error: %v", err)
	}
	_, err = manager.StartCommand(context.Background(), StartRequest{
		SessionID: "session-1",
		Argv:      []string{os.Args[0], "-test.run=TestHelperProcess", "--", "session", "linger"},
		CWD:       "../outside",
		Env:       map[string]string{"GO_WANT_HELPER_PROCESS": "1"},
	})
	if err == nil || !strings.Contains(err.Error(), "escapes runner root") {
		t.Fatalf("StartCommand error = %v, want root escape", err)
	}
}

func TestOSSessionManagerRejectsInvalidTTYGeometry(t *testing.T) {
	manager, err := NewOSSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewOSSessionManager returned error: %v", err)
	}
	cases := []StartRequest{
		{
			SessionID: "session-1",
			TTY:       true,
			Cols:      120,
			Argv:      []string{os.Args[0], "-test.run=TestHelperProcess", "--", "session", "linger"},
			Env:       map[string]string{"GO_WANT_HELPER_PROCESS": "1"},
		},
		{
			SessionID: "session-1",
			TTY:       true,
			Cols:      maxTTYDimension + 1,
			Rows:      40,
			Argv:      []string{os.Args[0], "-test.run=TestHelperProcess", "--", "session", "linger"},
			Env:       map[string]string{"GO_WANT_HELPER_PROCESS": "1"},
		},
	}
	for _, req := range cases {
		if _, err := manager.StartCommand(context.Background(), req); err == nil {
			t.Fatalf("StartCommand(%#v) returned nil error, want geometry validation failure", req)
		}
	}
}

func TestOSSessionStateWaitAndFinishClosesDrainableTerminal(t *testing.T) {
	terminal := &blockingDrainTerminal{
		readStarted: make(chan struct{}),
		closed:      make(chan struct{}),
	}
	state := &osSessionState{
		process:  &stubCommandProcess{wait: sessionWaitResult{exitCode: 0}},
		session:  CommandSession{ID: "cmd-1", Status: SessionRunning},
		terminal: terminal,
		done:     make(chan struct{}),
		updates:  make(chan struct{}, 1),
	}
	readerDone := make(chan struct{})
	go state.captureStream("pty", terminal, nil, readerDone)
	<-terminal.readStarted

	go state.waitAndFinish([]osSessionReader{{stream: "pty", reader: terminal}}, []chan struct{}{readerDone})

	select {
	case <-state.done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for waitAndFinish to close drainable terminal")
	}
	if !state.isDraining() {
		t.Fatal("draining state was not preserved after waitAndFinish")
	}
	if !terminal.isClosed() {
		t.Fatal("terminal was not closed for drain")
	}
	session := state.snapshot()
	if session.Status != SessionExited {
		t.Fatalf("session = %#v, want exited session", session)
	}
	if session.ExitCode == nil || *session.ExitCode != 0 {
		t.Fatalf("session = %#v, want zero exit code", session)
	}
}

func TestOSSessionStateWaitAndFinishForcesReaderDrainAfterTimeout(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	state := &osSessionState{
		manager: &OSSessionManager{
			drainTimeout:      10 * time.Millisecond,
			maxBufferedBytes:  defaultSessionMaxBufferedBytes,
			maxBufferedChunks: defaultSessionMaxBufferedChunks,
		},
		process: &stubCommandProcess{wait: sessionWaitResult{exitCode: 0}},
		session: CommandSession{
			ID:        "cmd-1",
			Status:    SessionRunning,
			NextSeq:   1,
			StartedAt: time.Now().UTC(),
		},
		done:    make(chan struct{}),
		updates: make(chan struct{}, 1),
	}
	readerDone := make(chan struct{})
	go state.captureStream("stdout", reader, reader, readerDone)

	go state.waitAndFinish([]osSessionReader{{stream: "stdout", reader: reader, closer: reader}}, []chan struct{}{readerDone})

	select {
	case <-state.done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for waitAndFinish to force-close inherited stream reader")
	}
	if !state.isDraining() {
		t.Fatal("draining state was not set after forced reader close")
	}
	session := state.snapshot()
	if session.Status != SessionExited {
		t.Fatalf("session = %#v, want exited session after forced drain", session)
	}
	if session.ExitCode == nil || *session.ExitCode != 0 {
		t.Fatalf("session = %#v, want zero exit code", session)
	}
	if !strings.Contains(joinChunkText(state.read(0, 0, 0).Chunks), "output drain timed out") {
		t.Fatalf("output = %#v, want drain timeout diagnostic", state.read(0, 0, 0).Chunks)
	}
}

func waitForOutput(t *testing.T, manager *OSSessionManager, req ReadRequest, ok func(ReadResult) bool) ReadResult {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last ReadResult
	for time.Now().Before(deadline) {
		result, err := manager.ReadCommandOutput(context.Background(), req)
		if err != nil {
			t.Fatalf("ReadCommandOutput returned error: %v", err)
		}
		last = result
		if ok(result) {
			return result
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for output; last=%#v", last)
	return ReadResult{}
}

func joinChunkText(chunks []OutputChunk) string {
	var b strings.Builder
	for _, chunk := range chunks {
		b.WriteString(chunk.Text)
	}
	return b.String()
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

type stubCommandProcess struct {
	wait sessionWaitResult
}

func (p *stubCommandProcess) PID() int                { return 1 }
func (p *stubCommandProcess) Interrupt() error        { return os.ErrProcessDone }
func (p *stubCommandProcess) Kill() error             { return nil }
func (p *stubCommandProcess) Wait() sessionWaitResult { return p.wait }
func (p *stubCommandProcess) Close() error            { return nil }

type blockingDrainTerminal struct {
	readStarted chan struct{}
	closed      chan struct{}
	startOnce   sync.Once
	closeOnce   sync.Once
}

func (t *blockingDrainTerminal) Read(p []byte) (int, error) {
	// The drain test expects exactly one transition from "read not yet blocked"
	// to "reader is blocked waiting for close."
	t.startOnce.Do(func() {
		close(t.readStarted)
	})
	<-t.closed
	return 0, io.EOF
}

func (t *blockingDrainTerminal) Write(p []byte) (int, error) { return len(p), nil }
func (t *blockingDrainTerminal) Resize(cols, rows int) error { return nil }
func (t *blockingDrainTerminal) Close() error {
	t.closeOnce.Do(func() {
		close(t.closed)
	})
	return nil
}
func (t *blockingDrainTerminal) CloseForDrain() error { return t.Close() }
func (t *blockingDrainTerminal) isClosed() bool {
	select {
	case <-t.closed:
		return true
	default:
		return false
	}
}
