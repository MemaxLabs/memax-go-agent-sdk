//go:build windows

package commandtools

import (
	"context"
	"os"
	"strings"
	"testing"
)

func windowsHelperProcessEnv() map[string]string {
	env := map[string]string{"GO_WANT_HELPER_PROCESS": "1"}
	if value := os.Getenv("SystemRoot"); value != "" {
		env["SystemRoot"] = value
	}
	if value := os.Getenv("PATH"); value != "" {
		env["PATH"] = value
	}
	return env
}

func TestEncodeWindowsEnvironmentPreservesNilVsEmpty(t *testing.T) {
	block, err := encodeWindowsEnvironment(nil)
	if err != nil {
		t.Fatalf("encodeWindowsEnvironment(nil) returned error: %v", err)
	}
	if block != nil {
		t.Fatalf("encodeWindowsEnvironment(nil) = %v, want nil", block)
	}

	block, err = encodeWindowsEnvironment([]string{})
	if err != nil {
		t.Fatalf("encodeWindowsEnvironment(empty) returned error: %v", err)
	}
	if len(block) != 2 || block[0] != 0 || block[1] != 0 {
		t.Fatalf("encodeWindowsEnvironment(empty) = %v, want double-NUL block", block)
	}
}

func TestOSSessionManagerTTYSessionDoesNotInheritEnvByDefault(t *testing.T) {
	t.Setenv("MEMAX_COMMANDTOOLS_TEST_SECRET", "secret")
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
			"env",
			"MEMAX_COMMANDTOOLS_TEST_SECRET",
		},
		Env: windowsHelperProcessEnv(),
	})
	if err != nil {
		t.Fatalf("StartCommand returned error: %v", err)
	}
	result := waitForOutput(t, manager, ReadRequest{SessionID: "session-1", ID: started.ID}, func(result ReadResult) bool {
		return result.Session.Status == SessionExited
	})
	if result.Session.ExitCode == nil || *result.Session.ExitCode != 0 {
		t.Fatalf("result session = %#v, want clean helper exit", result.Session)
	}
	if strings.Contains(joinChunkText(result.Chunks), "secret") {
		t.Fatalf("result chunks = %#v, want no inherited secret", result.Chunks)
	}
}

func TestOSSessionManagerResizeTTYSessionWindows(t *testing.T) {
	manager, err := NewOSSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewOSSessionManager returned error: %v", err)
	}
	started, err := manager.StartCommand(context.Background(), StartRequest{
		SessionID: "session-1",
		TTY:       true,
		Cols:      90,
		Rows:      30,
		Argv: []string{
			os.Args[0],
			"-test.run=TestHelperProcess",
			"--",
			"session",
			"linger",
		},
		Env: windowsHelperProcessEnv(),
	})
	if err != nil {
		t.Fatalf("StartCommand returned error: %v", err)
	}
	if !started.TTY || started.Cols != 90 || started.Rows != 30 {
		t.Fatalf("started = %#v, want tty geometry", started)
	}
	resized, err := manager.ResizeCommandTerminal(context.Background(), ResizeRequest{
		SessionID: "session-1",
		ID:        started.ID,
		Cols:      120,
		Rows:      45,
	})
	if err != nil {
		t.Fatalf("ResizeCommandTerminal returned error: %v", err)
	}
	if !resized.TTY || resized.Cols != 120 || resized.Rows != 45 {
		t.Fatalf("resized = %#v, want updated tty geometry", resized)
	}
	if _, err := manager.StopCommand(context.Background(), StopRequest{
		SessionID: "session-1",
		ID:        started.ID,
		Force:     true,
	}); err != nil {
		t.Fatalf("StopCommand returned error: %v", err)
	}
}
