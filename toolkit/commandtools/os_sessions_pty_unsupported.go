//go:build !unix

package commandtools

import (
	"fmt"
	"os"
	"os/exec"
)

func startPTYCommand(_ *exec.Cmd) (*os.File, error) {
	return nil, fmt.Errorf("commandtools: PTY sessions are not supported on this platform")
}
