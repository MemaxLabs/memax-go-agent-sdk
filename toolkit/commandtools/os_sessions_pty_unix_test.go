//go:build unix

package commandtools

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/creack/pty"
)

func TestOSSessionManagerResizeTTYSession(t *testing.T) {
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
		Env: map[string]string{"GO_WANT_HELPER_PROCESS": "1"},
	})
	if err != nil {
		if errors.Is(err, ErrCommandSessionPTYUnsupported) {
			t.Skipf("pty unsupported: %v", err)
		}
		t.Fatalf("StartCommand returned error: %v", err)
	}
	if !started.TTY || started.Cols != 90 || started.Rows != 30 {
		t.Fatalf("started = %#v, want tty geometry", started)
	}
	state, err := manager.lookupSession("session-1", started.ID)
	if err != nil {
		t.Fatalf("lookupSession returned error: %v", err)
	}
	state.stdinMu.Lock()
	terminal := state.terminal
	state.stdinMu.Unlock()
	ptyTerminal, ok := terminal.(*unixPTYTerminal)
	if !ok {
		t.Fatalf("terminal = %T, want *unixPTYTerminal", terminal)
	}
	size, err := pty.GetsizeFull(ptyTerminal.file)
	if err != nil {
		t.Fatalf("GetsizeFull returned error: %v", err)
	}
	if int(size.Cols) != 90 || int(size.Rows) != 30 {
		t.Fatalf("initial size = %dx%d, want 90x30", size.Cols, size.Rows)
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
	if resized.Cols != 120 || resized.Rows != 45 {
		t.Fatalf("resized = %#v, want updated geometry", resized)
	}
	size, err = pty.GetsizeFull(ptyTerminal.file)
	if err != nil {
		t.Fatalf("GetsizeFull after resize returned error: %v", err)
	}
	if int(size.Cols) != 120 || int(size.Rows) != 45 {
		t.Fatalf("resized size = %dx%d, want 120x45", size.Cols, size.Rows)
	}
	if _, err := manager.StopCommand(context.Background(), StopRequest{SessionID: "session-1", ID: started.ID, Force: true}); err != nil {
		t.Fatalf("StopCommand returned error: %v", err)
	}
}
