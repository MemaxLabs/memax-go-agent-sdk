//go:build unix

package commandtools

import (
	"fmt"
	"os"
	"os/exec"
	"sync"

	"github.com/creack/pty"
)

type unixPTYTerminal struct {
	file      *os.File
	closeOnce sync.Once
	closeErr  error
}

func startPTYCommand(cmd *exec.Cmd, cols, rows int, signalsProcessTree bool) (terminalHandle, commandProcess, error) {
	size := &pty.Winsize{
		Cols: uint16(cols),
		Rows: uint16(rows),
	}
	file, err := pty.StartWithSize(cmd, size)
	if err != nil {
		return nil, nil, fmt.Errorf("commandtools: start PTY command: %w", err)
	}
	terminal := &unixPTYTerminal{file: file}
	return terminal, newExecCommandProcess(cmd, signalsProcessTree), nil
}

func (t *unixPTYTerminal) Read(p []byte) (int, error) {
	return t.file.Read(p)
}

func (t *unixPTYTerminal) Write(p []byte) (int, error) {
	return t.file.Write(p)
}

func (t *unixPTYTerminal) Close() error {
	t.closeOnce.Do(func() {
		t.closeErr = t.file.Close()
	})
	return t.closeErr
}

func (t *unixPTYTerminal) Resize(cols, rows int) error {
	if err := pty.Setsize(t.file, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}); err != nil {
		return fmt.Errorf("commandtools: set PTY size: %w", err)
	}
	return nil
}
