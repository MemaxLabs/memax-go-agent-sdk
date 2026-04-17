//go:build unix

package commandtools

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/creack/pty"
)

func startPTYCommand(cmd *exec.Cmd) (*os.File, error) {
	file, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("commandtools: start PTY command: %w", err)
	}
	return file, nil
}
