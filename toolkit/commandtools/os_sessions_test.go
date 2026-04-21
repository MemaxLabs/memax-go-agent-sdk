package commandtools

import (
	"context"
	"errors"
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
	if _, err := manager.ReadCommandOutput(context.Background(), ReadRequest{SessionID: "session-2", ID: started.ID}); !errors.Is(err, ErrCommandSessionNotVisible) {
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
	if _, err := manager.ReadCommandOutput(context.Background(), ReadRequest{SessionID: "session-2", ID: second.ID}); !errors.Is(err, ErrCommandSessionUnknown) {
		t.Fatalf("ReadCommandOutput after cleanup error = %v, want unknown session", err)
	}
}

func TestOSSessionManagerSweepPersistedRunningCommandsMarksOrphaned(t *testing.T) {
	store := NewMemoryCommandTranscriptStore()
	now := time.Now().UTC()
	running := CommandSession{
		ID:        "persisted-running",
		SessionID: "session-1",
		Status:    SessionRunning,
		StartedAt: now,
		NextSeq:   1,
	}
	exited := CommandSession{
		ID:         "persisted-exited",
		SessionID:  "session-1",
		Status:     SessionExited,
		StartedAt:  now.Add(time.Second),
		FinishedAt: ptrTime(now.Add(2 * time.Second)),
		ExitCode:   ptrInt(0),
		NextSeq:    1,
	}
	if err := store.SaveCommandSession(context.Background(), running); err != nil {
		t.Fatalf("SaveCommandSession(running) returned error: %v", err)
	}
	if err := store.SaveCommandSession(context.Background(), exited); err != nil {
		t.Fatalf("SaveCommandSession(exited) returned error: %v", err)
	}
	manager, err := NewOSSessionManager(t.TempDir(), WithOSSessionManagerTranscriptStore(store))
	if err != nil {
		t.Fatalf("NewOSSessionManager returned error: %v", err)
	}
	swept, err := manager.SweepPersistedRunningCommands(context.Background())
	if err != nil {
		t.Fatalf("SweepPersistedRunningCommands returned error: %v", err)
	}
	if swept != 1 {
		t.Fatalf("swept = %d, want 1", swept)
	}
	orphaned, err := store.CommandSession(context.Background(), running.ID)
	if err != nil {
		t.Fatalf("CommandSession(running) returned error: %v", err)
	}
	if orphaned.Status != SessionOrphaned || orphaned.FinishedAt == nil {
		t.Fatalf("orphaned = %#v, want orphaned status with finished_at", orphaned)
	}
	unchanged, err := store.CommandSession(context.Background(), exited.ID)
	if err != nil {
		t.Fatalf("CommandSession(exited) returned error: %v", err)
	}
	if unchanged.Status != SessionExited {
		t.Fatalf("unchanged = %#v, want exited status untouched", unchanged)
	}
	listed, err := manager.ListCommands(context.Background(), ListRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatalf("ListCommands running-only returned error: %v", err)
	}
	if len(listed) != 0 {
		t.Fatalf("listed = %#v, want no running sessions after sweep", listed)
	}
	sweptAgain, err := manager.SweepPersistedRunningCommands(context.Background())
	if err != nil {
		t.Fatalf("second SweepPersistedRunningCommands returned error: %v", err)
	}
	if sweptAgain != 0 {
		t.Fatalf("sweptAgain = %d, want idempotent second sweep to return 0", sweptAgain)
	}
}

func TestOSSessionManagerSweepPersistedRunningCommandsSkipsLiveSession(t *testing.T) {
	store := NewMemoryCommandTranscriptStore()
	manager, err := NewOSSessionManager(t.TempDir(), WithOSSessionManagerTranscriptStore(store))
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
			"linger",
		},
		Env: map[string]string{"GO_WANT_HELPER_PROCESS": "1"},
	})
	if err != nil {
		t.Fatalf("StartCommand returned error: %v", err)
	}
	defer func() {
		_, _ = manager.StopCommand(context.Background(), StopRequest{SessionID: "session-1", ID: started.ID, Force: true})
	}()
	swept, err := manager.SweepPersistedRunningCommands(context.Background())
	if err != nil {
		t.Fatalf("SweepPersistedRunningCommands returned error: %v", err)
	}
	if swept != 0 {
		t.Fatalf("swept = %d, want 0 while live session exists", swept)
	}
	persisted, err := store.CommandSession(context.Background(), started.ID)
	if err != nil {
		t.Fatalf("CommandSession(started) returned error: %v", err)
	}
	if persisted.Status != SessionRunning {
		t.Fatalf("persisted = %#v, want running status", persisted)
	}
}

func TestOSSessionManagerSweepPersistedRunningCommandsWithoutStore(t *testing.T) {
	manager, err := NewOSSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewOSSessionManager returned error: %v", err)
	}
	swept, err := manager.SweepPersistedRunningCommands(context.Background())
	if err != nil {
		t.Fatalf("SweepPersistedRunningCommands returned error: %v", err)
	}
	if swept != 0 {
		t.Fatalf("swept = %d, want 0 without transcript store", swept)
	}
}

func TestOSSessionManagerSweepPersistedRunningCommandsGlobalScope(t *testing.T) {
	store := NewMemoryCommandTranscriptStore()
	now := time.Now().UTC()
	for _, session := range []CommandSession{
		{ID: "cmd-a", SessionID: "session-1", Status: SessionRunning, StartedAt: now, NextSeq: 1},
		{ID: "cmd-b", SessionID: "session-2", Status: SessionRunning, StartedAt: now.Add(time.Second), NextSeq: 1},
	} {
		if err := store.SaveCommandSession(context.Background(), session); err != nil {
			t.Fatalf("SaveCommandSession(%s) returned error: %v", session.ID, err)
		}
	}
	manager, err := NewOSSessionManager(t.TempDir(), WithOSSessionManagerTranscriptStore(store))
	if err != nil {
		t.Fatalf("NewOSSessionManager returned error: %v", err)
	}
	swept, err := manager.SweepPersistedRunningCommands(context.Background())
	if err != nil {
		t.Fatalf("SweepPersistedRunningCommands returned error: %v", err)
	}
	if swept != 2 {
		t.Fatalf("swept = %d, want 2 for global sweep scope", swept)
	}
	for _, id := range []string{"cmd-a", "cmd-b"} {
		session, err := store.CommandSession(context.Background(), id)
		if err != nil {
			t.Fatalf("CommandSession(%s) returned error: %v", id, err)
		}
		if session.Status != SessionOrphaned {
			t.Fatalf("session %s = %#v, want orphaned after sweep", id, session)
		}
	}
}

func TestOSSessionManagerTranscriptOnlySessionReturnsNotRunningForLiveOps(t *testing.T) {
	store := NewMemoryCommandTranscriptStore()
	now := time.Now().UTC()
	persisted := CommandSession{
		ID:        "persisted-running",
		SessionID: "session-1",
		Status:    SessionRunning,
		TTY:       true,
		Cols:      80,
		Rows:      24,
		StartedAt: now,
		NextSeq:   1,
	}
	if err := store.SaveCommandSession(context.Background(), persisted); err != nil {
		t.Fatalf("SaveCommandSession returned error: %v", err)
	}
	manager, err := NewOSSessionManager(t.TempDir(), WithOSSessionManagerTranscriptStore(store))
	if err != nil {
		t.Fatalf("NewOSSessionManager returned error: %v", err)
	}

	if _, err := manager.WriteCommandInput(context.Background(), WriteRequest{
		SessionID: "session-1",
		ID:        persisted.ID,
		Input:     "hello\n",
	}); !errors.Is(err, ErrCommandSessionNotRunning) {
		t.Fatalf("WriteCommandInput error = %v, want ErrCommandSessionNotRunning", err)
	}
	if _, err := manager.ResizeCommandTerminal(context.Background(), ResizeRequest{
		SessionID: "session-1",
		ID:        persisted.ID,
		Cols:      100,
		Rows:      30,
	}); !errors.Is(err, ErrCommandSessionNotRunning) {
		t.Fatalf("ResizeCommandTerminal error = %v, want ErrCommandSessionNotRunning", err)
	}
	if _, err := manager.StopCommand(context.Background(), StopRequest{
		SessionID: "session-1",
		ID:        persisted.ID,
		Force:     true,
	}); !errors.Is(err, ErrCommandSessionNotRunning) {
		t.Fatalf("StopCommand error = %v, want ErrCommandSessionNotRunning", err)
	}
}

func TestOSSessionManagerTranscriptOnlySessionPreservesVisibility(t *testing.T) {
	store := NewMemoryCommandTranscriptStore()
	now := time.Now().UTC()
	persisted := CommandSession{
		ID:        "persisted-running",
		SessionID: "session-1",
		Status:    SessionRunning,
		StartedAt: now,
		NextSeq:   1,
	}
	if err := store.SaveCommandSession(context.Background(), persisted); err != nil {
		t.Fatalf("SaveCommandSession returned error: %v", err)
	}
	manager, err := NewOSSessionManager(t.TempDir(), WithOSSessionManagerTranscriptStore(store))
	if err != nil {
		t.Fatalf("NewOSSessionManager returned error: %v", err)
	}
	if _, err := manager.StopCommand(context.Background(), StopRequest{
		SessionID: "session-2",
		ID:        persisted.ID,
		Force:     true,
	}); !errors.Is(err, ErrCommandSessionNotVisible) {
		t.Fatalf("StopCommand error = %v, want ErrCommandSessionNotVisible", err)
	}
}

func TestOSSessionManagerTranscriptOnlyTerminalSessionStillNotRunning(t *testing.T) {
	store := NewMemoryCommandTranscriptStore()
	now := time.Now().UTC()
	persisted := CommandSession{
		ID:         "persisted-exited",
		SessionID:  "session-1",
		Status:     SessionExited,
		StartedAt:  now.Add(-time.Minute),
		FinishedAt: ptrTime(now),
		ExitCode:   ptrInt(0),
		NextSeq:    3,
	}
	if err := store.SaveCommandSession(context.Background(), persisted); err != nil {
		t.Fatalf("SaveCommandSession returned error: %v", err)
	}
	manager, err := NewOSSessionManager(t.TempDir(), WithOSSessionManagerTranscriptStore(store))
	if err != nil {
		t.Fatalf("NewOSSessionManager returned error: %v", err)
	}
	if _, err := manager.StopCommand(context.Background(), StopRequest{
		SessionID: "session-1",
		ID:        persisted.ID,
		Force:     true,
	}); !errors.Is(err, ErrCommandSessionNotRunning) {
		t.Fatalf("StopCommand error = %v, want ErrCommandSessionNotRunning", err)
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

func TestOSSessionManagerWaitCommandOutput(t *testing.T) {
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
			"ready-then-finish",
			"300ms",
		},
		Env: map[string]string{"GO_WANT_HELPER_PROCESS": "1"},
	})
	if err != nil {
		t.Fatalf("StartCommand returned error: %v", err)
	}
	first := waitForOutput(t, manager, ReadRequest{SessionID: "session-1", ID: started.ID}, func(result ReadResult) bool {
		return strings.Contains(joinChunkText(result.Chunks), "ready\n")
	})
	waited, err := manager.WaitCommandOutput(context.Background(), WaitRequest{
		SessionID: "session-1",
		ID:        started.ID,
		AfterSeq:  max(0, first.NextSeq-1),
		Timeout:   2 * time.Second,
	})
	if err != nil {
		t.Fatalf("WaitCommandOutput returned error: %v", err)
	}
	if !strings.Contains(joinChunkText(waited.Chunks), "done\n") {
		t.Fatalf("waited chunks = %#v, want done output", waited.Chunks)
	}
	final := waitForOutput(t, manager, ReadRequest{
		SessionID: "session-1",
		ID:        started.ID,
		AfterSeq:  max(0, waited.NextSeq-1),
	}, func(result ReadResult) bool {
		return result.Session.Status == SessionExited
	})
	if final.Session.ExitCode == nil || *final.Session.ExitCode != 0 {
		t.Fatalf("final session = %#v, want exited zero status", final.Session)
	}
}

func TestOSSessionManagerWaitCommandOutputTimeoutNoNewOutput(t *testing.T) {
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
			"linger",
		},
		Env: map[string]string{"GO_WANT_HELPER_PROCESS": "1"},
	})
	if err != nil {
		t.Fatalf("StartCommand returned error: %v", err)
	}
	defer func() {
		_, _ = manager.StopCommand(context.Background(), StopRequest{SessionID: "session-1", ID: started.ID, Force: true})
	}()
	first := waitForOutput(t, manager, ReadRequest{SessionID: "session-1", ID: started.ID}, func(result ReadResult) bool {
		return strings.Contains(joinChunkText(result.Chunks), "ready\n")
	})
	waited, err := manager.WaitCommandOutput(context.Background(), WaitRequest{
		SessionID: "session-1",
		ID:        started.ID,
		AfterSeq:  max(0, first.NextSeq-1),
		Timeout:   50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("WaitCommandOutput returned error: %v", err)
	}
	if len(waited.Chunks) != 0 || waited.Session.Status != SessionRunning {
		t.Fatalf("waited = %#v, want timeout snapshot with no new chunks and running status", waited)
	}
}

func TestOSSessionManagerWaitCommandOutputFallbackToTranscriptStore(t *testing.T) {
	store := NewMemoryCommandTranscriptStore()
	session := CommandSession{
		ID:        "persisted-running",
		SessionID: "session-1",
		Status:    SessionRunning,
		StartedAt: time.Now().UTC(),
		NextSeq:   2,
		Argv:      []string{"npm", "run", "watch"},
	}
	if err := store.SaveCommandSession(context.Background(), session); err != nil {
		t.Fatalf("SaveCommandSession returned error: %v", err)
	}
	if err := store.AppendCommandOutput(context.Background(), session.ID, []OutputChunk{{
		Seq:    1,
		Stream: "stdout",
		Text:   "persisted\n",
		Time:   time.Now().UTC(),
	}}); err != nil {
		t.Fatalf("AppendCommandOutput returned error: %v", err)
	}
	manager, err := NewOSSessionManager(t.TempDir(), WithOSSessionManagerTranscriptStore(store))
	if err != nil {
		t.Fatalf("NewOSSessionManager returned error: %v", err)
	}
	waited, err := manager.WaitCommandOutput(context.Background(), WaitRequest{
		SessionID: "session-1",
		ID:        session.ID,
		AfterSeq:  0,
		Timeout:   time.Second,
	})
	if err != nil {
		t.Fatalf("WaitCommandOutput returned error: %v", err)
	}
	if len(waited.Chunks) != 1 || waited.Chunks[0].Text != "persisted\n" {
		t.Fatalf("waited chunks = %#v, want transcript store output", waited.Chunks)
	}
}

func TestOSSessionManagerWaitCommandOutputUnknownWithoutStore(t *testing.T) {
	manager, err := NewOSSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewOSSessionManager returned error: %v", err)
	}
	_, err = manager.WaitCommandOutput(context.Background(), WaitRequest{
		SessionID: "session-1",
		ID:        "missing",
		Timeout:   10 * time.Millisecond,
	})
	if !errors.Is(err, ErrCommandSessionUnknown) {
		t.Fatalf("WaitCommandOutput error = %v, want ErrCommandSessionUnknown", err)
	}
}

func TestOSSessionManagerWaitCommandOutputCanceledContext(t *testing.T) {
	manager, err := NewOSSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewOSSessionManager returned error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = manager.WaitCommandOutput(ctx, WaitRequest{ID: "cmd-1"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitCommandOutput error = %v, want context canceled", err)
	}
}

func TestOSSessionManagerClassifiesStateErrors(t *testing.T) {
	manager, err := NewOSSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewOSSessionManager returned error: %v", err)
	}
	started, err := manager.StartCommand(context.Background(), StartRequest{
		SessionID: "session-1",
		ID:        "fixed",
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
	defer func() {
		_, _ = manager.StopCommand(context.Background(), StopRequest{SessionID: "session-1", ID: started.ID, Force: true})
	}()
	if _, err := manager.StartCommand(context.Background(), StartRequest{
		SessionID: "session-1",
		ID:        "fixed",
		// Use an executable path that cannot start. Duplicate explicit IDs must
		// be rejected before the OS adapter attempts process creation.
		Argv: []string{filepath.Join(t.TempDir(), "missing-helper")},
		Env:  map[string]string{"GO_WANT_HELPER_PROCESS": "1"},
	}); err != nil {
		assertCommandSessionError(t, err, ErrCommandSessionAlreadyExists, "commandtools: command session fixed already exists")
	} else {
		t.Fatal("StartCommand duplicate returned nil error, want ErrCommandSessionAlreadyExists")
	}
}

func TestOSSessionManagerReleasesReservedIDAfterStartFailure(t *testing.T) {
	manager, err := NewOSSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewOSSessionManager returned error: %v", err)
	}
	id := "retry-after-start-failure"
	if _, err := manager.StartCommand(context.Background(), StartRequest{
		SessionID: "session-1",
		ID:        id,
		Argv:      []string{filepath.Join(t.TempDir(), "missing-helper")},
	}); err == nil || errors.Is(err, ErrCommandSessionAlreadyExists) {
		t.Fatalf("StartCommand missing executable error = %v, want non-duplicate start failure", err)
	}
	started, err := manager.StartCommand(context.Background(), StartRequest{
		SessionID: "session-1",
		ID:        id,
		Argv:      []string{os.Args[0], "-test.run=TestHelperProcess", "--", "session", "linger"},
		Env:       map[string]string{"GO_WANT_HELPER_PROCESS": "1"},
	})
	if err != nil {
		t.Fatalf("StartCommand retry returned error: %v", err)
	}
	defer func() {
		_, _ = manager.StopCommand(context.Background(), StopRequest{SessionID: "session-1", ID: started.ID, Force: true})
	}()
	if started.ID != id {
		t.Fatalf("started.ID = %q, want %q", started.ID, id)
	}
}

func TestOSSessionStateClassifiesInputAndResizeErrors(t *testing.T) {
	stdinClosed := &osSessionState{
		session: CommandSession{ID: "cmd-stdin", Status: SessionRunning, StartedAt: time.Now().UTC()},
	}
	if _, err := stdinClosed.writeInput(context.Background(), WriteRequest{ID: "cmd-stdin", Input: "hello"}); err != nil {
		assertCommandSessionError(t, err, ErrCommandSessionStdinClosed, "commandtools: command session cmd-stdin stdin is closed")
	} else {
		t.Fatal("writeInput with closed stdin returned nil error, want ErrCommandSessionStdinClosed")
	}

	notPTY := &osSessionState{
		session: CommandSession{ID: "cmd-plain", Status: SessionRunning, StartedAt: time.Now().UTC()},
	}
	if _, err := notPTY.resizeTerminal(context.Background(), ResizeRequest{ID: "cmd-plain", Cols: 100, Rows: 30}); err != nil {
		assertCommandSessionError(t, err, ErrCommandSessionNotPTY, "commandtools: command session cmd-plain is not PTY-backed")
	} else {
		t.Fatal("resizeTerminal non-PTY returned nil error, want ErrCommandSessionNotPTY")
	}

	notRunning := &osSessionState{
		session: CommandSession{ID: "cmd-exited", TTY: true, Status: SessionExited, StartedAt: time.Now().UTC()},
	}
	if _, err := notRunning.resizeTerminal(context.Background(), ResizeRequest{ID: "cmd-exited", Cols: 100, Rows: 30}); err != nil {
		assertCommandSessionError(t, err, ErrCommandSessionNotRunning, "commandtools: command session cmd-exited is not running")
	} else {
		t.Fatal("resizeTerminal exited session returned nil error, want ErrCommandSessionNotRunning")
	}

	terminalClosed := &osSessionState{
		session: CommandSession{ID: "cmd-terminal", TTY: true, Status: SessionRunning, StartedAt: time.Now().UTC()},
	}
	if _, err := terminalClosed.resizeTerminal(context.Background(), ResizeRequest{ID: "cmd-terminal", Cols: 100, Rows: 30}); err != nil {
		assertCommandSessionError(t, err, ErrCommandSessionTerminalClosed, "commandtools: command session cmd-terminal terminal is closed")
	} else {
		t.Fatal("resizeTerminal with closed terminal returned nil error, want ErrCommandSessionTerminalClosed")
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
		if errors.Is(err, ErrCommandSessionPTYUnsupported) {
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

func ptrInt(v int) *int {
	return &v
}

func ptrTime(v time.Time) *time.Time {
	return &v
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
