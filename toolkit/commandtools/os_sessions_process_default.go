//go:build !unix

package commandtools

import (
	"os"
	"os/exec"
)

func configureSessionCommand(_ *exec.Cmd, _ bool) bool {
	return false
}

func interruptSessionProcess(process *os.Process, _ bool) error {
	if process == nil {
		return os.ErrProcessDone
	}
	return process.Signal(os.Interrupt)
}

func killSessionProcess(process *os.Process, _ bool) error {
	if process == nil {
		return os.ErrProcessDone
	}
	return process.Kill()
}
