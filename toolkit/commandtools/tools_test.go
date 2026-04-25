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
			Input: json.RawMessage(`{"command":"go test ./...","cwd":"pkg","timeout_ms":1000,"purpose":"run tests"}`),
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
	if got := result.Metadata[MetadataCommandString]; got != "go test ./..." {
		t.Fatalf("command metadata = %#v", got)
	}
	if got := result.Metadata[MetadataCommandArgv]; !sameStrings(got.([]string), shellArgv("go test ./...", nil)) {
		t.Fatalf("argv metadata = %#v", got)
	}
	requests := runner.Requests()
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	if requests[0].Command != "go test ./..." || !sameStrings(requests[0].Argv, shellArgv("go test ./...", nil)) || requests[0].CWD != "pkg" || requests[0].Purpose != "run tests" || requests[0].Timeout != time.Second {
		t.Fatalf("request = %#v", requests[0])
	}
}

func TestRunCommandToolNormalizesLegacyArgvInput(t *testing.T) {
	runner := NewScriptedRunner(Result{ExitCode: 0})
	registry := tool.NewRegistry(NewTool(Config{Runner: runner, Shell: []string{"bash", "-lc"}}))
	results := collectToolResults(tool.Executor{Registry: registry}.Run(context.Background(), []model.ToolUse{{
		ID:    "cmd-1",
		Name:  ToolName,
		Input: json.RawMessage(`{"command":["go","test","./..."],"timeout_ms":"1000"}`),
	}}))

	if got, want := len(results), 1; got != want {
		t.Fatalf("len(results) = %d, want %d", got, want)
	}
	if results[0].IsError {
		t.Fatalf("result = %#v, want success", results[0])
	}
	requests := runner.Requests()
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	if requests[0].Command != "go test ./..." || requests[0].Timeout != time.Second {
		t.Fatalf("request = %#v, want normalized command and timeout", requests[0])
	}
	if want := []string{"bash", "-lc", "go test ./..."}; !sameStrings(requests[0].Argv, want) {
		t.Fatalf("argv = %#v, want %#v", requests[0].Argv, want)
	}
}

func TestRunCommandToolShellQuotesNormalizedArgvInput(t *testing.T) {
	runner := NewScriptedRunner(Result{ExitCode: 0})
	registry := tool.NewRegistry(NewTool(Config{Runner: runner, Shell: []string{"bash", "-lc"}}))
	results := collectToolResults(tool.Executor{Registry: registry}.Run(context.Background(), []model.ToolUse{{
		ID:    "cmd-1",
		Name:  ToolName,
		Input: json.RawMessage(`{"command":["env","FOO=bar","printf","%s\\n","hello world","don't split"]}`),
	}}))

	if len(results) != 1 || results[0].IsError {
		t.Fatalf("results = %#v, want success", results)
	}
	requests := runner.Requests()
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	want := "env 'FOO=bar' printf '%s\\n' 'hello world' 'don'\\''t split'"
	if requests[0].Command != want {
		t.Fatalf("command = %q, want %q", requests[0].Command, want)
	}
}

func TestRunCommandToolNormalizedArgvStripsEmptyArgumentsLikeApprovalDisplay(t *testing.T) {
	runner := NewScriptedRunner(Result{ExitCode: 0})
	registry := tool.NewRegistry(NewTool(Config{Runner: runner, Shell: []string{"bash", "-lc"}}))
	results := collectToolResults(tool.Executor{Registry: registry}.Run(context.Background(), []model.ToolUse{{
		ID:    "cmd-1",
		Name:  ToolName,
		Input: json.RawMessage(`{"command":["go","","  ","test"]}`),
	}}))

	if len(results) != 1 || results[0].IsError {
		t.Fatalf("results = %#v, want success", results)
	}
	requests := runner.Requests()
	if len(requests) != 1 || requests[0].Command != "go test" {
		t.Fatalf("requests = %#v, want normalized command matching approval display", requests)
	}
	summary, err := ApprovalSummaryFromRunInput([]byte(`{"command":["go","","  ","test"]}`))
	if err != nil {
		t.Fatalf("ApprovalSummaryFromRunInput returned error: %v", err)
	}
	if summary.Title != "Run command: go test" {
		t.Fatalf("summary = %#v, want approval display to match runtime command", summary)
	}
}

func TestRunCommandToolRejectsEmptyLegacyArgvAfterNormalization(t *testing.T) {
	runner := NewScriptedRunner(Result{ExitCode: 0})
	registry := tool.NewRegistry(NewTool(Config{Runner: runner, Shell: []string{"bash", "-lc"}}))
	results := collectToolResults(tool.Executor{Registry: registry}.Run(context.Background(), []model.ToolUse{{
		ID:    "cmd-1",
		Name:  ToolName,
		Input: json.RawMessage(`{"command":["","  "]}`),
	}}))

	if len(results) != 1 || !results[0].IsError {
		t.Fatalf("results = %#v, want schema validation error", results)
	}
	if !strings.Contains(results[0].Content, "invalid input for tool") {
		t.Fatalf("content = %q, want validation error", results[0].Content)
	}
	if len(runner.Requests()) != 0 {
		t.Fatalf("runner requests = %#v, want none", runner.Requests())
	}
}

func TestRunCommandToolLeavesNULArgvForSchemaValidation(t *testing.T) {
	runner := NewScriptedRunner(Result{ExitCode: 0})
	registry := tool.NewRegistry(NewTool(Config{Runner: runner, Shell: []string{"bash", "-lc"}}))
	results := collectToolResults(tool.Executor{Registry: registry}.Run(context.Background(), []model.ToolUse{{
		ID:    "cmd-1",
		Name:  ToolName,
		Input: json.RawMessage("{\"command\":[\"printf\",\"bad\\u0000arg\"]}"),
	}}))

	if len(results) != 1 || !results[0].IsError {
		t.Fatalf("results = %#v, want schema validation error", results)
	}
	if !strings.Contains(results[0].Content, "invalid input for tool") {
		t.Fatalf("content = %q, want validation error", results[0].Content)
	}
	if len(runner.Requests()) != 0 {
		t.Fatalf("runner requests = %#v, want none", runner.Requests())
	}
}

func TestExecCommandToolUsesExactArgv(t *testing.T) {
	runner := NewScriptedRunner(Result{ExitCode: 0})
	runTool := NewExecTool(Config{Runner: runner})
	result, err := runTool.Execute(context.Background(), tool.Call{
		Use: model.ToolUse{
			ID:    "cmd-1",
			Name:  ToolName,
			Input: json.RawMessage(`{"command":["go","test","./..."],"purpose":"run tests"}`),
		},
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.IsError {
		t.Fatalf("result.IsError = true, want success: %s", result.Content)
	}
	requests := runner.Requests()
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	if requests[0].Command != "" || !sameStrings(requests[0].Argv, []string{"go", "test", "./..."}) {
		t.Fatalf("request = %#v", requests[0])
	}
}

func TestExecCommandToolNormalizesNumericStringsWithoutChangingArgv(t *testing.T) {
	runner := NewScriptedRunner(Result{ExitCode: 0})
	registry := tool.NewRegistry(NewExecTool(Config{Runner: runner}))
	results := collectToolResults(tool.Executor{Registry: registry}.Run(context.Background(), []model.ToolUse{{
		ID:    "cmd-1",
		Name:  ToolName,
		Input: json.RawMessage(`{"command":["go","test","./..."],"timeout_ms":"1000"}`),
	}}))

	if len(results) != 1 || results[0].IsError {
		t.Fatalf("results = %#v, want success", results)
	}
	requests := runner.Requests()
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	if requests[0].Command != "" || !sameStrings(requests[0].Argv, []string{"go", "test", "./..."}) || requests[0].Timeout != time.Second {
		t.Fatalf("request = %#v, want exact argv with normalized timeout", requests[0])
	}
}

func collectToolResults(ch <-chan model.ToolResult) []model.ToolResult {
	var results []model.ToolResult
	for result := range ch {
		results = append(results, result)
	}
	return results
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
			Input: json.RawMessage(`{"command":"echo large"}`),
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
			Input: json.RawMessage(`{"command":"cat","stdin":"abcd"}`),
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
	summary, err := ApprovalSummaryFromRunInput([]byte(`{"command":"go test ./...","purpose":"verify all packages"}`))
	if err != nil {
		t.Fatalf("ApprovalSummaryFromRunInput returned error: %v", err)
	}
	if summary.Title != "Run command: go test ./..." || summary.Description != "verify all packages" || summary.Changes != 1 {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestApprovalSummaryFromRunInputAcceptsExecArgv(t *testing.T) {
	summary, err := ApprovalSummaryFromRunInput([]byte(`{"command":["go","test","./..."]}`))
	if err != nil {
		t.Fatalf("ApprovalSummaryFromRunInput returned error: %v", err)
	}
	if summary.Title != "Run command: go test ./..." || summary.Changes != 1 {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestApprovalSummaryFromRunInputUsesRuntimeShellQuoting(t *testing.T) {
	summary, err := ApprovalSummaryFromRunInput([]byte(`{"command":["env","FOO=bar","printf","%s\\n","hello world"]}`))
	if err != nil {
		t.Fatalf("ApprovalSummaryFromRunInput returned error: %v", err)
	}
	want := "Run command: env 'FOO=bar' printf '%s\\n' 'hello world'"
	if summary.Title != want {
		t.Fatalf("summary title = %q, want %q", summary.Title, want)
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

func TestOSRunnerEnvReturnsEmptySliceWhenInheritanceDisabled(t *testing.T) {
	runner, err := NewOSRunner(t.TempDir())
	if err != nil {
		t.Fatalf("NewOSRunner returned error: %v", err)
	}
	env := runner.env(nil)
	if env == nil {
		t.Fatal("runner.env(nil) = nil, want explicit empty environment slice")
	}
	if len(env) != 0 {
		t.Fatalf("runner.env(nil) = %v, want empty slice", env)
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
