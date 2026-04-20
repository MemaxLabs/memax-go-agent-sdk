// Package sessiontest provides reusable conformance tests for managed command
// session adapters.
//
// The harness intentionally exercises only the public commandtools interfaces:
// start, read, stop, list, and optional write, resize, and cleanup extensions.
// Sandbox, remote, or container-backed adapters can run the same contract tests
// without depending on commandtools' local OS implementation.
package sessiontest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/commandtools"
)

// Scenario identifies one conformance scenario. Contract.NewManager receives
// the scenario so deterministic managers can install only the scripted sessions
// needed by that subtest.
type Scenario string

const (
	ScenarioNaturalExit Scenario = "natural_exit"
	ScenarioStopCleanup Scenario = "stop_cleanup"
	ScenarioWriteInput  Scenario = "write_input"
	ScenarioResizeTTY   Scenario = "resize_tty"
)

// Contract describes how to construct a managed-session adapter and the
// commands used by the shared conformance tests.
type Contract struct {
	// Name optionally labels the adapter under test in failure messages.
	Name string
	// NewManager returns a fresh manager for the given scenario.
	NewManager func(testing.TB, Scenario) commandtools.SessionManager

	// NaturalExitRequest returns a command that prints ReadyText, prints
	// DoneText, and exits with status 0.
	NaturalExitRequest func(testing.TB) commandtools.StartRequest
	// LongRunningRequest returns a command that prints ReadyText and stays
	// running until it is stopped or cleaned up.
	LongRunningRequest func(testing.TB) commandtools.StartRequest

	// InteractiveRequest enables the optional Writer contract when NewManager
	// returns a commandtools.Writer.
	InteractiveRequest func(testing.TB) commandtools.StartRequest
	// TTYRequest enables the optional Resizer contract when NewManager returns a
	// commandtools.Resizer.
	TTYRequest func(testing.TB) commandtools.StartRequest

	// ReadyText is the output marker used by scenario commands to signal that
	// they have started and are ready for the next harness action.
	ReadyText string
	// DoneText is the output marker NaturalExitRequest prints before exiting.
	DoneText string

	// EchoInput is written to InteractiveRequest. The default includes a
	// trailing newline so line-oriented commands can consume it.
	EchoInput string
	// EchoText is the expected output marker after EchoInput is written.
	EchoText string
	// ExitInput is written to InteractiveRequest to request clean process exit.
	ExitInput string
	// ExitText is the expected output marker after ExitInput is written.
	ExitText string

	// IsNotVisible classifies the expected error returned when a command is
	// read across a session boundary. The default accepts the OS adapter's
	// current "not visible" phrasing.
	IsNotVisible func(error) bool
	// IsTTYUnsupported classifies adapter errors that mean PTY/TTY sessions are
	// unavailable on this platform. The resize scenario skips when it returns
	// true. The default accepts the OS adapter's current unsupported-PTY
	// phrasing.
	IsTTYUnsupported func(error) bool

	// WaitTimeout bounds each polling wait in the conformance harness.
	WaitTimeout time.Duration
	// PollEvery controls how often the harness polls command output while
	// waiting for lifecycle or output markers.
	PollEvery time.Duration
}

// Run executes the shared managed-session conformance suite.
func Run(t *testing.T, contract Contract) {
	t.Helper()
	contract.withDefaults().validate(t)

	t.Run("start-read-list-visibility-and-natural-exit", func(t *testing.T) {
		c := contract.withDefaults()
		manager := c.newManager(t, ScenarioNaturalExit)
		req := c.NaturalExitRequest(t)
		req.SessionID = defaultString(req.SessionID, "contract-session-1")
		started, err := manager.StartCommand(context.Background(), req)
		if err != nil {
			t.Fatalf("StartCommand returned error: %v", err)
		}
		if started.ID == "" || started.SessionID != req.SessionID || started.Status != commandtools.SessionRunning {
			t.Fatalf("started = %#v, want running session with ID in %q", started, req.SessionID)
		}
		if len(started.Argv) == 0 {
			t.Fatalf("started.Argv = %#v, want command argv preserved", started.Argv)
		}

		visible, err := manager.ListCommands(context.Background(), commandtools.ListRequest{SessionID: req.SessionID})
		if err != nil {
			t.Fatalf("ListCommands visible returned error: %v", err)
		}
		if !containsSession(visible, started.ID, commandtools.SessionRunning) {
			t.Fatalf("visible sessions = %#v, want running %q", visible, started.ID)
		}

		other, err := manager.ListCommands(context.Background(), commandtools.ListRequest{SessionID: "contract-other-session"})
		if err != nil {
			t.Fatalf("ListCommands other returned error: %v", err)
		}
		if len(other) != 0 {
			t.Fatalf("other sessions = %#v, want none across session boundary", other)
		}
		if _, err := manager.ReadCommandOutput(context.Background(), commandtools.ReadRequest{
			SessionID: "contract-other-session",
			ID:        started.ID,
		}); err == nil || !c.IsNotVisible(err) {
			t.Fatalf("ReadCommandOutput cross-session error = %v, want visibility denial", err)
		}

		first := readUntil(t, manager, commandtools.ReadRequest{SessionID: req.SessionID, ID: started.ID}, c, func(result commandtools.ReadResult) bool {
			return strings.Contains(joinChunks(result.Chunks), c.ReadyText)
		})
		assertMonotonicChunks(t, first.Chunks)
		observed := joinChunks(first.Chunks)
		final := readUntil(t, manager, commandtools.ReadRequest{
			SessionID: req.SessionID,
			ID:        started.ID,
			AfterSeq:  max(0, first.NextSeq-1),
		}, c, func(result commandtools.ReadResult) bool {
			observed += joinChunks(result.Chunks)
			return result.Session.Status == commandtools.SessionExited && strings.Contains(observed, c.DoneText)
		})
		if final.Session.ExitCode == nil || *final.Session.ExitCode != 0 || final.Session.FinishedAt == nil {
			t.Fatalf("final session = %#v, want clean exited terminal session", final.Session)
		}
	})

	t.Run("stop-and-list", func(t *testing.T) {
		c := contract.withDefaults()
		manager := c.newManager(t, ScenarioStopCleanup)
		req := c.LongRunningRequest(t)
		req.SessionID = defaultString(req.SessionID, "contract-session-2")
		started, err := manager.StartCommand(context.Background(), req)
		if err != nil {
			t.Fatalf("StartCommand returned error: %v", err)
		}
		_ = readUntil(t, manager, commandtools.ReadRequest{SessionID: req.SessionID, ID: started.ID}, c, func(result commandtools.ReadResult) bool {
			return strings.Contains(joinChunks(result.Chunks), c.ReadyText)
		})
		stopped, err := manager.StopCommand(context.Background(), commandtools.StopRequest{SessionID: req.SessionID, ID: started.ID})
		if err != nil {
			t.Fatalf("StopCommand returned error: %v", err)
		}
		if stopped.Status != commandtools.SessionStopped || stopped.FinishedAt == nil {
			t.Fatalf("stopped = %#v, want stopped terminal session", stopped)
		}
		running, err := manager.ListCommands(context.Background(), commandtools.ListRequest{SessionID: req.SessionID})
		if err != nil {
			t.Fatalf("ListCommands running returned error: %v", err)
		}
		if len(running) != 0 {
			t.Fatalf("running sessions = %#v, want stopped session hidden by default", running)
		}
		all, err := manager.ListCommands(context.Background(), commandtools.ListRequest{SessionID: req.SessionID, IncludeCompleted: true})
		if err != nil {
			t.Fatalf("ListCommands completed returned error: %v", err)
		}
		if !containsSession(all, started.ID, commandtools.SessionStopped) {
			t.Fatalf("completed sessions = %#v, want stopped %q", all, started.ID)
		}
	})

	t.Run("cleanup", func(t *testing.T) {
		c := contract.withDefaults()
		manager := c.newManager(t, ScenarioStopCleanup)
		cleaner, ok := any(manager).(commandtools.Cleaner)
		if !ok {
			t.Skip("manager does not implement commandtools.Cleaner")
		}
		cleanupReq := c.LongRunningRequest(t)
		cleanupReq.SessionID = "contract-cleanup-session"
		cleanupStarted, err := manager.StartCommand(context.Background(), cleanupReq)
		if err != nil {
			t.Fatalf("StartCommand cleanup returned error: %v", err)
		}
		_ = readUntil(t, manager, commandtools.ReadRequest{SessionID: cleanupReq.SessionID, ID: cleanupStarted.ID}, c, func(result commandtools.ReadResult) bool {
			return strings.Contains(joinChunks(result.Chunks), c.ReadyText)
		})
		if err := cleaner.CleanupSession(context.Background(), cleanupReq.SessionID); err != nil {
			t.Fatalf("CleanupSession returned error: %v", err)
		}
		remaining, err := manager.ListCommands(context.Background(), commandtools.ListRequest{SessionID: cleanupReq.SessionID, IncludeCompleted: true})
		if err != nil {
			t.Fatalf("ListCommands after cleanup returned error: %v", err)
		}
		if len(remaining) != 0 {
			t.Fatalf("remaining sessions = %#v, want cleanup to remove owned sessions", remaining)
		}
		if _, err := manager.ReadCommandOutput(context.Background(), commandtools.ReadRequest{SessionID: cleanupReq.SessionID, ID: cleanupStarted.ID}); err == nil {
			t.Fatalf("ReadCommandOutput after cleanup returned nil error, want unknown session")
		}
	})

	t.Run("write-input", func(t *testing.T) {
		c := contract.withDefaults()
		if c.InteractiveRequest == nil {
			t.Skip("contract has no interactive command")
		}
		manager := c.newManager(t, ScenarioWriteInput)
		writer, ok := any(manager).(commandtools.Writer)
		if !ok {
			t.Skip("manager does not implement commandtools.Writer")
		}
		req := c.InteractiveRequest(t)
		req.SessionID = defaultString(req.SessionID, "contract-session-3")
		started, err := manager.StartCommand(context.Background(), req)
		if err != nil {
			t.Fatalf("StartCommand returned error: %v", err)
		}
		_ = readUntil(t, manager, commandtools.ReadRequest{SessionID: req.SessionID, ID: started.ID}, c, func(result commandtools.ReadResult) bool {
			return strings.Contains(joinChunks(result.Chunks), c.ReadyText)
		})
		first, err := writer.WriteCommandInput(context.Background(), commandtools.WriteRequest{
			SessionID: req.SessionID,
			ID:        started.ID,
			Input:     c.EchoInput,
			Yield:     250 * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("WriteCommandInput echo returned error: %v", err)
		}
		if first.InputBytes != len(c.EchoInput) || !strings.Contains(joinChunks(first.Chunks), c.EchoText) {
			t.Fatalf("first write = %#v chunks=%q, want echo %q and input bytes", first, joinChunks(first.Chunks), c.EchoText)
		}
		second, err := writer.WriteCommandInput(context.Background(), commandtools.WriteRequest{
			SessionID: req.SessionID,
			ID:        started.ID,
			Input:     c.ExitInput,
			Yield:     250 * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("WriteCommandInput exit returned error: %v", err)
		}
		if second.InputBytes != len(c.ExitInput) || !strings.Contains(joinChunks(second.Chunks), c.ExitText) {
			t.Fatalf("second write = %#v chunks=%q, want exit text %q and input bytes", second, joinChunks(second.Chunks), c.ExitText)
		}
		final := readUntil(t, manager, commandtools.ReadRequest{
			SessionID: req.SessionID,
			ID:        started.ID,
			AfterSeq:  max(0, second.NextSeq-1),
		}, c, func(result commandtools.ReadResult) bool {
			return result.Session.Status == commandtools.SessionExited
		})
		if final.Session.ExitCode == nil || *final.Session.ExitCode != 0 {
			t.Fatalf("final session = %#v, want clean exit after input", final.Session)
		}
	})

	t.Run("resize-tty", func(t *testing.T) {
		c := contract.withDefaults()
		if c.TTYRequest == nil {
			t.Skip("contract has no TTY command")
		}
		manager := c.newManager(t, ScenarioResizeTTY)
		resizer, ok := any(manager).(commandtools.Resizer)
		if !ok {
			t.Skip("manager does not implement commandtools.Resizer")
		}
		req := c.TTYRequest(t)
		req.SessionID = defaultString(req.SessionID, "contract-session-4")
		started, err := manager.StartCommand(context.Background(), req)
		if err != nil {
			if c.IsTTYUnsupported(err) {
				t.Skipf("TTY sessions unsupported: %v", err)
			}
			t.Fatalf("StartCommand returned error: %v", err)
		}
		if !started.TTY || started.Cols != req.Cols || started.Rows != req.Rows {
			t.Fatalf("started = %#v, want TTY geometry %dx%d", started, req.Cols, req.Rows)
		}
		resized, err := resizer.ResizeCommandTerminal(context.Background(), commandtools.ResizeRequest{
			SessionID: req.SessionID,
			ID:        started.ID,
			Cols:      req.Cols + 7,
			Rows:      req.Rows + 3,
		})
		if err != nil {
			t.Fatalf("ResizeCommandTerminal returned error: %v", err)
		}
		if resized.Cols != req.Cols+7 || resized.Rows != req.Rows+3 || !resized.TTY {
			t.Fatalf("resized = %#v, want updated TTY geometry", resized)
		}
		if _, err := manager.StopCommand(context.Background(), commandtools.StopRequest{SessionID: req.SessionID, ID: started.ID, Force: true}); err != nil {
			t.Fatalf("StopCommand returned error: %v", err)
		}
	})
}

func (c Contract) withDefaults() Contract {
	if c.WaitTimeout <= 0 {
		c.WaitTimeout = 5 * time.Second
	}
	if c.PollEvery <= 0 {
		c.PollEvery = 20 * time.Millisecond
	}
	if c.ReadyText == "" {
		c.ReadyText = "ready"
	}
	if c.DoneText == "" {
		c.DoneText = "done"
	}
	if c.EchoInput == "" {
		c.EchoInput = "hello\n"
	}
	if c.EchoText == "" {
		c.EchoText = "echo:hello"
	}
	if c.ExitInput == "" {
		c.ExitInput = "exit\n"
	}
	if c.ExitText == "" {
		c.ExitText = "bye"
	}
	if c.IsNotVisible == nil {
		c.IsNotVisible = func(err error) bool {
			return err != nil && strings.Contains(err.Error(), "not visible")
		}
	}
	if c.IsTTYUnsupported == nil {
		c.IsTTYUnsupported = func(err error) bool {
			return err != nil && strings.Contains(err.Error(), "PTY sessions are not supported")
		}
	}
	return c
}

func (c Contract) validate(t testing.TB) {
	t.Helper()
	if c.NewManager == nil {
		t.Fatal("sessiontest: Contract.NewManager is required")
	}
	if c.NaturalExitRequest == nil {
		t.Fatal("sessiontest: Contract.NaturalExitRequest is required")
	}
	if c.LongRunningRequest == nil {
		t.Fatal("sessiontest: Contract.LongRunningRequest is required")
	}
}

func (c Contract) newManager(t testing.TB, scenario Scenario) commandtools.SessionManager {
	t.Helper()
	manager := c.NewManager(t, scenario)
	if manager == nil {
		t.Fatalf("sessiontest: NewManager(%s) returned nil", scenario)
	}
	return manager
}

func readUntil(t testing.TB, manager commandtools.Reader, req commandtools.ReadRequest, c Contract, ok func(commandtools.ReadResult) bool) commandtools.ReadResult {
	t.Helper()
	deadline := time.Now().Add(c.WaitTimeout)
	var last commandtools.ReadResult
	for time.Now().Before(deadline) {
		result, err := manager.ReadCommandOutput(context.Background(), req)
		if err != nil {
			t.Fatalf("ReadCommandOutput returned error: %v", err)
		}
		last = result
		if ok(result) {
			return result
		}
		time.Sleep(c.PollEvery)
	}
	t.Fatalf("timed out waiting for command output; last session=%#v chunks=%q", last.Session, joinChunks(last.Chunks))
	return commandtools.ReadResult{}
}

func containsSession(sessions []commandtools.CommandSession, id string, status commandtools.SessionStatus) bool {
	for _, session := range sessions {
		if session.ID == id && session.Status == status {
			return true
		}
	}
	return false
}

func joinChunks(chunks []commandtools.OutputChunk) string {
	var b strings.Builder
	for _, chunk := range chunks {
		b.WriteString(chunk.Text)
	}
	return b.String()
}

func assertMonotonicChunks(t testing.TB, chunks []commandtools.OutputChunk) {
	t.Helper()
	for i, chunk := range chunks {
		if i == 0 {
			continue
		}
		prev := chunks[i-1].Seq
		if chunk.Seq <= prev {
			t.Fatalf("chunks = %#v, want strictly increasing sequence numbers", chunks)
		}
	}
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
