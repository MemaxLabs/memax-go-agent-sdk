//go:build !unix

package commandtools

import (
	"fmt"
	"os"
	"os/exec"
)

func startPTYCommand(_ *exec.Cmd, _, _ int) (*os.File, error) {
	return nil, fmt.Errorf("commandtools: PTY sessions are not supported on this platform")
}

func resizePTY(_ *os.File, _, _ int) error {
	return fmt.Errorf("commandtools: PTY sessions are not supported on this platform")
}
