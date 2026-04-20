//go:build unix

package commandtools

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestOSSessionManagerForceStopTerminatesProcessTreeUnix(t *testing.T) {
	fixture := newUnixTickerFixture(t)
	manager, err := NewOSSessionManager(
		t.TempDir(),
		WithOSSessionManagerStopGrace(25*time.Millisecond),
		WithOSSessionManagerDrainTimeout(25*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("NewOSSessionManager returned error: %v", err)
	}
	started := fixture.start(t, manager, 0)
	if !started.SignalsProcessTree {
		t.Fatalf("started = %#v, want Unix session to signal process tree", started)
	}
	fixture.waitForTicks(t)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stopped, err := manager.StopCommand(ctx, StopRequest{
		SessionID: "session-1",
		ID:        started.ID,
		Force:     true,
	})
	if err != nil {
		t.Fatalf("StopCommand returned error: %v", err)
	}
	if stopped.Status != SessionStopped {
		t.Fatalf("stopped = %#v, want stopped session", stopped)
	}
	fixture.assertTicksStopped(t)
}

func TestOSSessionManagerTimeoutTerminatesProcessTreeUnix(t *testing.T) {
	fixture := newUnixTickerFixture(t)
	manager, err := NewOSSessionManager(
		t.TempDir(),
		WithOSSessionManagerStopGrace(25*time.Millisecond),
		WithOSSessionManagerDrainTimeout(25*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("NewOSSessionManager returned error: %v", err)
	}
	started := fixture.start(t, manager, 75*time.Millisecond)
	if !started.SignalsProcessTree {
		t.Fatalf("started = %#v, want Unix session to signal process tree", started)
	}
	fixture.waitForTicks(t)
	result := waitForOutput(t, manager, ReadRequest{SessionID: "session-1", ID: started.ID}, func(result ReadResult) bool {
		return result.Session.Status == SessionExited
	})
	if !result.Session.TimedOut {
		t.Fatalf("session = %#v, want timeout marker", result.Session)
	}
	fixture.assertTicksStopped(t)
}

type unixTickerFixture struct {
	sh       string
	sleep    string
	tickFile string
	pidFile  string
}

func newUnixTickerFixture(t *testing.T) unixTickerFixture {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh not available: %v", err)
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("sleep not available: %v", err)
	}
	dir := t.TempDir()
	return unixTickerFixture{
		sh:       sh,
		sleep:    sleep,
		tickFile: dir + "/ticks",
		pidFile:  dir + "/child.pid",
	}
}

func (f unixTickerFixture) start(t *testing.T, manager *OSSessionManager, timeout time.Duration) CommandSession {
	t.Helper()
	script := "(" +
		"trap '' INT; " +
		"while true; do printf tick >> \"$TICK_FILE\"; " + shellQuote(f.sleep) + " 0.05; done" +
		") & " +
		"echo $! > \"$PID_FILE\"; " +
		"printf 'ready\\n'; " +
		"trap '' INT; wait"
	started, err := manager.StartCommand(context.Background(), StartRequest{
		SessionID: "session-1",
		Argv:      []string{f.sh, "-c", script},
		Env: map[string]string{
			"PID_FILE":  f.pidFile,
			"TICK_FILE": f.tickFile,
		},
		Timeout: timeout,
	})
	if err != nil {
		t.Fatalf("StartCommand returned error: %v", err)
	}
	_ = waitForOutput(t, manager, ReadRequest{SessionID: "session-1", ID: started.ID}, func(result ReadResult) bool {
		return strings.Contains(joinChunkText(result.Chunks), "ready\n")
	})
	childPID := f.childPID(t)
	t.Cleanup(func() {
		_ = syscall.Kill(childPID, syscall.SIGKILL)
	})
	return started
}

func (f unixTickerFixture) waitForTicks(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if fileSize(f.tickFile) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for child process to write %s", f.tickFile)
}

func (f unixTickerFixture) assertTicksStopped(t *testing.T) {
	t.Helper()
	before := fileSize(f.tickFile)
	time.Sleep(200 * time.Millisecond)
	after := fileSize(f.tickFile)
	if after != before {
		t.Fatalf("tick file grew from %d to %d bytes after session termination; child process survived", before, after)
	}
}

func (f unixTickerFixture) childPID(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile(f.pidFile)
	if err != nil {
		t.Fatalf("read child pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parse child pid %q: %v", data, err)
	}
	return pid
}

func fileSize(path string) int64 {
	stat, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return stat.Size()
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
