//go:build unix

package commandtools

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/creack/pty"
)

func startPTYCommand(cmd *exec.Cmd, cols, rows int) (*os.File, error) {
	size := &pty.Winsize{
		Cols: uint16(cols),
		Rows: uint16(rows),
	}
	file, err := pty.StartWithSize(cmd, size)
	if err != nil {
		return nil, fmt.Errorf("commandtools: start PTY command: %w", err)
	}
	return file, nil
}

func resizePTY(file *os.File, cols, rows int) error {
	if err := pty.Setsize(file, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}); err != nil {
		return fmt.Errorf("commandtools: set PTY size: %w", err)
	}
	return nil
}
