package commandtools

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
	"github.com/mattn/go-isatty"
)

func TestRunCommandToolReturnsProcessFailureAsToolError(t *testing.T) {
	runner := NewScriptedRunner(Result{
		ExitCode: 2,
		Stdout:   "running tests",
		Stderr:   "README.md: expected fixed",
		Duration: 15 * time.Millisecond,
	})
	runTool := NewTool(Config{Runner: runner})
	result, err := runTool.Execute(context.Background(), tool.Call{
		Use: model.ToolUse{
			ID:    "cmd-1",
			Name:  ToolName,
			Input: json.RawMessage(`{"command":["go","test","./..."],"cwd":"pkg","timeout_ms":1000,"purpose":"run tests"}`),
		},
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("result.IsError = false, want true for non-zero exit")
	}
	if !strings.Contains(result.Content, "command failed: go test ./...") || !strings.Contains(result.Content, "README.md: expected fixed") {
		t.Fatalf("result.Content = %q", result.Content)
	}
	if result.Metadata[MetadataCommandOperation] != "run" || result.Metadata[MetadataCommandExitCode] != 2 {
		t.Fatalf("metadata = %#v", result.Metadata)
	}
	if got := result.Metadata[MetadataCommandArgv]; !sameStrings(got.([]string), []string{"go", "test", "./..."}) {
		t.Fatalf("argv metadata = %#v", got)
	}
	requests := runner.Requests()
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	if requests[0].CWD != "pkg" || requests[0].Purpose != "run tests" || requests[0].Timeout != time.Second {
		t.Fatalf("request = %#v", requests[0])
	}
}

func TestScriptedRunnerReturnsDefensiveCopies(t *testing.T) {
	runner := NewScriptedRunner(Result{Argv: []string{"go", "test"}, Metadata: map[string]any{"k": "v"}})
	result, err := runner.RunCommand(context.Background(), Request{Argv: []string{"go", "test"}, Env: map[string]string{"A": "B"}})
	if err != nil {
		t.Fatalf("RunCommand returned error: %v", err)
	}
	result.Argv[0] = "mutated"
	result.Metadata["k"] = "mutated"
	requests := runner.Requests()
	requests[0].Argv[0] = "mutated"
	requests[0].Env["A"] = "mutated"
	requests = runner.Requests()
	if requests[0].Argv[0] != "go" || requests[0].Env["A"] != "B" {
		t.Fatalf("captured request was mutated: %#v", requests[0])
	}
}

func TestRunCommandToolCapsRunnerOutput(t *testing.T) {
	runner := NewScriptedRunner(Result{
		ExitCode: 0,
		Stdout:   "abcdef",
		Stderr:   "uvwxyz",
	})
	runTool := NewTool(Config{Runner: runner, MaxOutputBytes: 3})
	result, err := runTool.Execute(context.Background(), tool.Call{
		Use: model.ToolUse{
			ID:    "cmd-1",
			Name:  ToolName,
			Input: json.RawMessage(`{"command":["echo","large"]}`),
		},
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !strings.Contains(result.Content, "stdout:\nabc") || strings.Contains(result.Content, "abcdef") {
		t.Fatalf("content = %q, want capped stdout", result.Content)
	}
	if result.Metadata[MetadataCommandOutputTruncated] != true {
		t.Fatalf("metadata = %#v, want truncated output", result.Metadata)
	}
}

func TestRunCommandToolRejectsOversizedStdin(t *testing.T) {
	runner := NewScriptedRunner(Result{ExitCode: 0})
	runTool := NewTool(Config{Runner: runner, MaxStdinBytes: 3})
	_, err := runTool.Execute(context.Background(), tool.Call{
		Use: model.ToolUse{
			ID:    "cmd-1",
			Name:  ToolName,
			Input: json.RawMessage(`{"command":["cat"],"stdin":"abcd"}`),
		},
	})
	if err == nil || !strings.Contains(err.Error(), "stdin") {
		t.Fatalf("Execute error = %v, want oversized stdin error", err)
	}
	if len(runner.Requests()) != 0 {
		t.Fatalf("runner requests = %#v, want none after stdin rejection", runner.Requests())
	}
}

func TestLimitStringBytesDoesNotSplitRunes(t *testing.T) {
	got, truncated := limitStringBytes("éé", 1)
	if got != "" || !truncated {
		t.Fatalf("limitStringBytes = %q, %v; want empty truncated string", got, truncated)
	}
	got, truncated = limitStringBytes("éé", 2)
	if got != "é" || !truncated {
		t.Fatalf("limitStringBytes = %q, %v; want first rune", got, truncated)
	}
}

func TestApprovalSummaryFromRunInput(t *testing.T) {
	summary, err := ApprovalSummaryFromRunInput([]byte(`{"command":["go","test","./..."],"purpose":"verify all packages"}`))
	if err != nil {
		t.Fatalf("ApprovalSummaryFromRunInput returned error: %v", err)
	}
	if summary.Title != "Run command: go test ./..." || summary.Description != "verify all packages" || summary.Changes != 1 {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestOSRunnerExecutesArgvWithRootedCWDAndOutputCap(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "input.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner, err := NewOSRunner(root, WithOSRunnerMaxOutputBytes(5))
	if err != nil {
		t.Fatalf("NewOSRunner returned error: %v", err)
	}
	result, err := runner.RunCommand(context.Background(), Request{
		Argv:    []string{os.Args[0], "-test.run=TestHelperProcess", "--", "cat", "input.txt"},
		Env:     map[string]string{"GO_WANT_HELPER_PROCESS": "1"},
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("RunCommand returned error: %v", err)
	}
	if result.ExitCode != 0 || result.Stdout != "hello" || result.StdoutBytes != 5 {
		t.Fatalf("result = %#v", result)
	}
	if !strings.HasPrefix(result.CWD, root) {
		t.Fatalf("cwd = %q, want under %q", result.CWD, root)
	}
}

func TestOSRunnerDoesNotInheritEnvByDefault(t *testing.T) {
	t.Setenv("MEMAX_COMMANDTOOLS_TEST_SECRET", "secret")
	runner, err := NewOSRunner(t.TempDir())
	if err != nil {
		t.Fatalf("NewOSRunner returned error: %v", err)
	}
	result, err := runner.RunCommand(context.Background(), Request{
		Argv: []string{
			os.Args[0],
			"-test.run=TestHelperProcess",
			"--",
			"env",
			"MEMAX_COMMANDTOOLS_TEST_SECRET",
		},
		Env:     map[string]string{"GO_WANT_HELPER_PROCESS": "1"},
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("RunCommand returned error: %v", err)
	}
	if result.ExitCode != 0 || strings.Contains(result.Stdout, "secret") {
		t.Fatalf("result = %#v, want no inherited secret", result)
	}
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) < 2 {
		os.Exit(2)
	}
	switch args[1] {
	case "cat":
		if len(args) < 3 {
			os.Exit(2)
		}
		content, err := os.ReadFile(filepath.Clean(args[2]))
		if err != nil {
			_, _ = os.Stderr.WriteString(err.Error())
			os.Exit(1)
		}
		_, _ = os.Stdout.Write(content)
	case "env":
		if len(args) < 3 {
			os.Exit(2)
		}
		_, _ = os.Stdout.WriteString(os.Getenv(args[2]))
	case "session":
		if len(args) < 3 {
			os.Exit(2)
		}
		switch args[2] {
		case "ready-then-finish":
			delay := 200 * time.Millisecond
			if len(args) >= 4 {
				parsed, err := time.ParseDuration(args[3])
				if err != nil {
					os.Exit(2)
				}
				delay = parsed
			}
			_, _ = os.Stdout.WriteString("ready\n")
			time.Sleep(delay)
			_, _ = os.Stdout.WriteString("done\n")
		case "linger":
			_, _ = os.Stdout.WriteString("ready\n")
			time.Sleep(30 * time.Second)
		case "echo-stdin":
			_, _ = os.Stdout.WriteString("ready\n")
			scanner := bufio.NewScanner(os.Stdin)
			for scanner.Scan() {
				line := scanner.Text()
				if line == "exit" {
					_, _ = os.Stdout.WriteString("bye\n")
					os.Exit(0)
				}
				_, _ = os.Stdout.WriteString("echo:" + line + "\n")
			}
			if err := scanner.Err(); err != nil {
				_, _ = os.Stderr.WriteString(err.Error())
				os.Exit(1)
			}
		case "tty-echo":
			if !isatty.IsTerminal(os.Stdin.Fd()) || !isatty.IsTerminal(os.Stdout.Fd()) {
				_, _ = os.Stdout.WriteString("not-tty\n")
				os.Exit(3)
			}
			_, _ = os.Stdout.WriteString("tty-ready\n")
			scanner := bufio.NewScanner(os.Stdin)
			for scanner.Scan() {
				line := scanner.Text()
				if line == "exit" {
					_, _ = os.Stdout.WriteString("tty-bye\n")
					os.Exit(0)
				}
				_, _ = os.Stdout.WriteString("tty:" + line + "\n")
			}
			if err := scanner.Err(); err != nil {
				_, _ = os.Stderr.WriteString(err.Error())
				os.Exit(1)
			}
		case "cwd":
			wd, err := os.Getwd()
			if err != nil {
				_, _ = os.Stderr.WriteString(err.Error())
				os.Exit(1)
			}
			_, _ = os.Stdout.WriteString(wd)
		case "sleep-exit":
			if len(args) < 4 {
				os.Exit(2)
			}
			millis, err := strconv.Atoi(args[3])
			if err != nil {
				os.Exit(2)
			}
			time.Sleep(time.Duration(millis) * time.Millisecond)
		default:
			os.Exit(2)
		}
	default:
		os.Exit(2)
	}
	os.Exit(0)
}

func TestOSRunnerRejectsRootEscapeCWD(t *testing.T) {
	runner, err := NewOSRunner(t.TempDir())
	if err != nil {
		t.Fatalf("NewOSRunner returned error: %v", err)
	}
	_, err = runner.RunCommand(context.Background(), Request{
		Argv:    []string{"true"},
		CWD:     "../outside",
		Timeout: time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "escapes runner root") {
		t.Fatalf("RunCommand error = %v, want root escape", err)
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
