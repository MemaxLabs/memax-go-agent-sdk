//go:build !unix && !windows

package commandtools

import (
	"fmt"
	"os/exec"
)

func startPTYCommand(_ *exec.Cmd, _, _ int, _ bool) (terminalHandle, commandProcess, error) {
	return nil, nil, fmt.Errorf("%w on this platform", ErrCommandSessionPTYUnsupported)
}
