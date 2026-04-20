//go:build windows

package commandtools

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const windowsStillActive = 259

type windowsProcess struct {
	mu        sync.Mutex
	handle    windows.Handle
	pid       int
	closeOnce sync.Once
	closeErr  error
}

type conPTYTerminal struct {
	input     *os.File
	output    *os.File
	console   windows.Handle
	closeOnce sync.Once
	closeErr  error
}

func startPTYCommand(cmd *exec.Cmd, cols, rows int, signalsProcessTree bool) (terminalHandle, commandProcess, error) {
	size, err := conPTYCoord(cols, rows)
	if err != nil {
		return nil, nil, err
	}
	inputR, inputW, err := os.Pipe()
	if err != nil {
		return nil, nil, fmt.Errorf("commandtools: create ConPTY input pipe: %w", err)
	}
	outputR, outputW, err := os.Pipe()
	if err != nil {
		_ = inputR.Close()
		_ = inputW.Close()
		return nil, nil, fmt.Errorf("commandtools: create ConPTY output pipe: %w", err)
	}
	var console windows.Handle
	if err := windows.CreatePseudoConsole(size, windows.Handle(inputR.Fd()), windows.Handle(outputW.Fd()), 0, &console); err != nil {
		_ = inputR.Close()
		_ = inputW.Close()
		_ = outputR.Close()
		_ = outputW.Close()
		return nil, nil, fmt.Errorf("commandtools: create ConPTY: %w", err)
	}
	_ = inputR.Close()
	_ = outputW.Close()

	terminal := &conPTYTerminal{
		input:   inputW,
		output:  outputR,
		console: console,
	}
	process, err := startConPTYProcess(cmd, console)
	if err != nil {
		_ = terminal.Close()
		return nil, nil, err
	}
	_ = signalsProcessTree
	return terminal, process, nil
}

func startConPTYProcess(cmd *exec.Cmd, console windows.Handle) (commandProcess, error) {
	if cmd == nil {
		return nil, fmt.Errorf("commandtools: nil PTY command")
	}
	appName, err := windows.UTF16PtrFromString(cmd.Path)
	if err != nil {
		return nil, fmt.Errorf("commandtools: encode PTY executable path: %w", err)
	}
	cmdline, err := utf16PtrOrNil(windows.ComposeCommandLine(cmd.Args))
	if err != nil {
		return nil, fmt.Errorf("commandtools: encode PTY command line: %w", err)
	}
	currentDir, err := utf16PtrOrNil(cmd.Dir)
	if err != nil {
		return nil, fmt.Errorf("commandtools: encode PTY cwd: %w", err)
	}
	env, err := encodeWindowsEnvironment(cmd.Env)
	if err != nil {
		return nil, err
	}
	attributeList, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return nil, fmt.Errorf("commandtools: create ConPTY attribute list: %w", err)
	}
	defer attributeList.Delete()
	consoleValue := console
	if err := attributeList.Update(
		windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE,
		unsafe.Pointer(&consoleValue),
		unsafe.Sizeof(consoleValue),
	); err != nil {
		return nil, fmt.Errorf("commandtools: attach ConPTY attribute: %w", err)
	}
	startupInfo := &windows.StartupInfoEx{
		StartupInfo:             windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfoEx{}))},
		ProcThreadAttributeList: attributeList.List(),
	}
	var processInfo windows.ProcessInformation
	var envPtr *uint16
	if len(env) > 0 {
		envPtr = &env[0]
	}
	if err := windows.CreateProcess(
		appName,
		cmdline,
		nil,
		nil,
		false,
		windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_UNICODE_ENVIRONMENT,
		envPtr,
		currentDir,
		&startupInfo.StartupInfo,
		&processInfo,
	); err != nil {
		return nil, fmt.Errorf("commandtools: start PTY command: %w", err)
	}
	_ = windows.CloseHandle(processInfo.Thread)
	return &windowsProcess{
		handle: processInfo.Process,
		pid:    int(processInfo.ProcessId),
	}, nil
}

func utf16PtrOrNil(value string) (*uint16, error) {
	if value == "" {
		return nil, nil
	}
	return windows.UTF16PtrFromString(value)
}

func encodeWindowsEnvironment(env []string) ([]uint16, error) {
	if env == nil {
		return nil, nil
	}
	if len(env) == 0 {
		return []uint16{0, 0}, nil
	}
	var block []uint16
	for _, entry := range env {
		encoded, err := windows.UTF16FromString(entry)
		if err != nil {
			return nil, fmt.Errorf("commandtools: encode PTY environment: %w", err)
		}
		block = append(block, encoded...)
	}
	block = append(block, 0)
	return block, nil
}

func conPTYCoord(cols, rows int) (windows.Coord, error) {
	if err := validateTTYDimensions(cols, rows); err != nil {
		return windows.Coord{}, err
	}
	return windows.Coord{X: int16(cols), Y: int16(rows)}, nil
}

func (p *windowsProcess) PID() int {
	if p == nil {
		return 0
	}
	return p.pid
}

func (p *windowsProcess) Interrupt() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.handle == 0 || p.isDoneLocked() {
		return os.ErrProcessDone
	}
	return syscall.EWINDOWS
}

func (p *windowsProcess) Kill() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.handle == 0 || p.isDoneLocked() {
		return os.ErrProcessDone
	}
	if err := windows.TerminateProcess(p.handle, 1); err != nil {
		return err
	}
	return nil
}

func (p *windowsProcess) Wait() sessionWaitResult {
	p.mu.Lock()
	handle := p.handle
	p.mu.Unlock()
	if handle == 0 {
		return sessionWaitResult{exitCode: -1, err: os.ErrProcessDone}
	}
	status, err := windows.WaitForSingleObject(handle, windows.INFINITE)
	if err != nil {
		return sessionWaitResult{exitCode: -1, err: fmt.Errorf("commandtools: wait PTY command: %w", err)}
	}
	if status != windows.WAIT_OBJECT_0 {
		return sessionWaitResult{exitCode: -1, err: fmt.Errorf("commandtools: wait PTY command: unexpected wait status %d", status)}
	}
	var code uint32
	if err := windows.GetExitCodeProcess(handle, &code); err != nil {
		return sessionWaitResult{exitCode: -1, err: fmt.Errorf("commandtools: read PTY exit code: %w", err)}
	}
	return sessionWaitResult{exitCode: int(code)}
}

func (p *windowsProcess) Close() error {
	if p == nil {
		return nil
	}
	p.closeOnce.Do(func() {
		p.mu.Lock()
		handle := p.handle
		p.handle = 0
		p.mu.Unlock()
		if handle != 0 {
			p.closeErr = windows.CloseHandle(handle)
		}
	})
	return p.closeErr
}

func (p *windowsProcess) isDoneLocked() bool {
	var code uint32
	if err := windows.GetExitCodeProcess(p.handle, &code); err != nil {
		return true
	}
	return code != windowsStillActive
}

func (t *conPTYTerminal) Read(p []byte) (int, error) {
	return t.output.Read(p)
}

func (t *conPTYTerminal) Write(p []byte) (int, error) {
	return t.input.Write(p)
}

func (t *conPTYTerminal) Close() error {
	t.closeOnce.Do(func() {
		var errs []error
		if t.input != nil {
			errs = append(errs, t.input.Close())
			t.input = nil
		}
		if t.output != nil {
			errs = append(errs, t.output.Close())
			t.output = nil
		}
		if t.console != 0 {
			windows.ClosePseudoConsole(t.console)
			t.console = 0
		}
		t.closeErr = errors.Join(errs...)
	})
	return t.closeErr
}

func (t *conPTYTerminal) CloseForDrain() error {
	return t.Close()
}

func (t *conPTYTerminal) Resize(cols, rows int) error {
	size, err := conPTYCoord(cols, rows)
	if err != nil {
		return err
	}
	if err := windows.ResizePseudoConsole(t.console, size); err != nil {
		return fmt.Errorf("commandtools: set PTY size: %w", err)
	}
	return nil
}
