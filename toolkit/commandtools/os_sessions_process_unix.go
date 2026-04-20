//go:build unix

package commandtools

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func configureSessionCommand(cmd *exec.Cmd, tty bool) bool {
	if cmd == nil {
		return false
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	if tty {
		// github.com/creack/pty sets Setsid and Setctty before starting the
		// command. A new session also creates a process group whose ID is the
		// child PID, so group signalling is still valid for PTY sessions.
		return true
	}
	cmd.SysProcAttr.Setpgid = true
	return true
}

func interruptSessionProcess(process *os.Process, processTree bool) error {
	if process == nil {
		return os.ErrProcessDone
	}
	if processTree {
		return signalSessionProcessGroup(process.Pid, syscall.SIGINT)
	}
	return process.Signal(os.Interrupt)
}

func killSessionProcess(process *os.Process, processTree bool) error {
	if process == nil {
		return os.ErrProcessDone
	}
	if processTree {
		return signalSessionProcessGroup(process.Pid, syscall.SIGKILL)
	}
	return process.Kill()
}

func signalSessionProcessGroup(pid int, signal syscall.Signal) error {
	if pid <= 0 {
		return os.ErrProcessDone
	}
	if err := syscall.Kill(-pid, signal); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}
