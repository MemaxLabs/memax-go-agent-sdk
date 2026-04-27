package coding

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/hook"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/planner"
	"github.com/MemaxLabs/memax-go-agent-sdk/providers/anthropic"
	"github.com/MemaxLabs/memax-go-agent-sdk/providers/openai"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/agentpolicy"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/approvaltools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/commandtools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/tasktools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/verifytools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/workspacetools"
	"github.com/MemaxLabs/memax-go-agent-sdk/workspace"
)

func TestNewAssemblesCodingRuntime(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	ws := workspace.NewMemoryStore(map[string]string{
		"README.md": "before\n",
	})
	tasks := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:     "task-1",
		Title:  "Ship README update",
		Status: tasktools.StatusInProgress,
	}})
	runner := commandtools.NewScriptedRunner(commandtools.Result{
		Argv:     []string{"go", "test", "./..."},
		ExitCode: 0,
		Stdout:   "ok\n",
	})
	manager := commandtools.NewScriptedSessionManager(commandtools.ScriptedCommand{ID: "watch-1"})
	approver := approvaltools.StaticApprover{
		Decision: approvaltools.Decision{
			Approved: true,
			Reason:   "approved",
		},
	}

	stack, err := New(Config{
		Workspace: ws,
		Tasks:     tasks,
		Command: commandtools.Config{
			Runner:          runner,
			ConcurrencySafe: true,
		},
		CommandSessions: manager,
		Approval: approvaltools.Config{
			Approver: approver,
		},
		Verifier: verifytools.Config{
			Verifier: verifytools.VerifierFunc(func(context.Context, verifytools.Request) (verifytools.Result, error) {
				return verifytools.Result{
					Name:   "test",
					Passed: true,
				}, nil
			}),
		},
		Policies: Policies{
			RequireCheckpointBeforePatch: true,
			RequirePatchApproval:         true,
			SingleUseApprovals:           true,
			InputBoundApprovals:          true,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	opts := stack.Options()
	if opts.Tools == nil {
		t.Fatal("stack options missing tool registry")
	}
	if opts.Hooks == nil {
		t.Fatal("stack options missing hook runner")
	}
	if opts.Planner == nil {
		t.Fatal("stack options missing planner")
	}

	specNames := toolNames(opts.Tools)
	for _, want := range []string{
		workspacetools.ReadToolName,
		workspacetools.ListToolName,
		workspacetools.ApplyPatchToolName,
		workspacetools.CheckpointToolName,
		workspacetools.RestoreToolName,
		verifytools.ToolName,
		commandtools.ToolName,
		commandtools.StartToolName,
		commandtools.ReadOutputToolName,
		commandtools.StopToolName,
		commandtools.ListToolName,
		tasktools.ListToolName,
		tasktools.UpsertToolName,
		tasktools.DeleteToolName,
		approvaltools.ToolName,
	} {
		if !contains(specNames, want) {
			t.Fatalf("assembled registry missing %q; got %v", want, specNames)
		}
	}

	exec := tool.Executor{
		Registry: opts.Tools,
		Hooks:    opts.Hooks,
		Runtime: tool.Runtime{
			SessionID: "session-1",
			Sessions:  opts.Sessions,
		},
	}

	patch1 := map[string]any{
		"operations": []map[string]any{{
			"path":        "README.md",
			"old_content": "before\n",
			"new_content": "after\n",
		}},
	}
	result := runTool(t, exec, workspacetools.ApplyPatchToolName, patch1)
	if !result.IsError || !strings.Contains(result.Content, agentpolicy.ApprovalBeforeToolReason(workspacetools.ApplyPatchToolName)) {
		t.Fatalf("expected approval denial, got error=%v content=%q", result.IsError, result.Content)
	}

	result = runTool(t, exec, approvaltools.ToolName, map[string]any{
		"action":     workspacetools.ApplyPatchToolName,
		"reason":     "update readme",
		"tool_input": patch1,
	})
	if result.IsError {
		t.Fatalf("approval should succeed: %s", result.Content)
	}

	result = runTool(t, exec, workspacetools.ApplyPatchToolName, patch1)
	if result.IsError {
		t.Fatalf("approved patch should succeed: %s", result.Content)
	}
	if result.Metadata[model.MetadataWorkspaceCheckpointID] != "checkpoint-1" || result.Metadata["auto_checkpoint"] != true {
		t.Fatalf("patch metadata = %#v, want automatic checkpoint", result.Metadata)
	}
	content, err := ws.ReadFile(ctx, "README.md")
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if content != "after\n" {
		t.Fatalf("patched content = %q, want %q", content, "after\n")
	}
	checkpoints, err := ws.ListCheckpoints(ctx)
	if err != nil {
		t.Fatalf("ListCheckpoints() error = %v", err)
	}
	if len(checkpoints) != 2 || checkpoints[1].ID != "checkpoint-1" {
		t.Fatalf("checkpoints = %#v, want initial plus automatic checkpoint", checkpoints)
	}

	patch2 := map[string]any{
		"operations": []map[string]any{{
			"path":        "README.md",
			"old_content": "after\n",
			"new_content": "done\n",
		}},
	}
	result = runTool(t, exec, workspacetools.ApplyPatchToolName, patch2)
	if !result.IsError || !strings.Contains(result.Content, agentpolicy.ApprovalBeforeToolReason(workspacetools.ApplyPatchToolName)) {
		t.Fatalf("expected single-use/input-bound approval denial, got error=%v content=%q", result.IsError, result.Content)
	}

	result = runTool(t, exec, verifytools.ToolName, map[string]any{
		"name": "test",
		"metadata": map[string]any{
			model.MetadataTaskID: "task-1",
		},
	})
	if result.IsError {
		t.Fatalf("verification should succeed: %s", result.Content)
	}

	listed, err := tasks.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("task count = %d, want 1", len(listed))
	}
	if listed[0].Status != tasktools.StatusCompleted {
		t.Fatalf("task status = %s, want %s", listed[0].Status, tasktools.StatusCompleted)
	}
	if !contains(listed[0].Evidence, "verification:test") {
		t.Fatalf("task evidence = %v, want verification marker", listed[0].Evidence)
	}

	plan, err := opts.Planner.Prepare(ctx, planner.Request{})
	if err != nil {
		t.Fatalf("Planner.Prepare() error = %v", err)
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("plan steps = %d, want 1", len(plan.Steps))
	}
	step := plan.Steps[0]
	if step.Status != planner.StatusCompleted {
		t.Fatalf("step status = %s, want %s", step.Status, planner.StatusCompleted)
	}
	if !contains(step.ToolHints, workspacetools.ApplyPatchToolName) || !contains(step.ToolHints, commandtools.ToolName) {
		t.Fatalf("step tool hints = %v, want workspace patch and command hints", step.ToolHints)
	}
	for _, want := range []string{
		commandtools.StartToolName,
		commandtools.ReadOutputToolName,
		commandtools.StopToolName,
		commandtools.ListToolName,
		commandtools.WriteInputToolName,
		commandtools.ResizeToolName,
	} {
		if !contains(step.ToolHints, want) {
			t.Fatalf("step tool hints = %v, want managed session hint %q", step.ToolHints, want)
		}
	}
	if !contains(step.VerificationHints, verifytools.ToolName) {
		t.Fatalf("step verification hints = %v, want %q", step.VerificationHints, verifytools.ToolName)
	}
}

func TestNewCanExposeUnifiedDiffOnlyPatchTool(t *testing.T) {
	t.Parallel()

	stack, err := New(Config{
		Workspace:               workspace.NewMemoryStore(map[string]string{"README.md": "before\n"}),
		WorkspacePatchInputMode: WorkspacePatchInputUnifiedDiff,
		Policies: Policies{
			RequireCheckpointBeforePatch: false,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	patchSpec, ok := toolSpec(stack.Registry(), workspacetools.ApplyPatchToolName)
	if !ok {
		t.Fatalf("registry missing %q", workspacetools.ApplyPatchToolName)
	}
	properties, ok := patchSpec.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("patch input schema properties = %#v, want object", patchSpec.InputSchema["properties"])
	}
	if _, ok := properties["unified_diff"]; !ok {
		t.Fatalf("patch input schema = %#v, want unified_diff property", patchSpec.InputSchema)
	}
	if _, ok := properties["operations"]; ok {
		t.Fatalf("patch input schema = %#v, did not want operations property", patchSpec.InputSchema)
	}

	result := runTool(t, tool.Executor{Registry: stack.Registry()}, workspacetools.ApplyPatchToolName, map[string]any{
		"operations": []map[string]any{{
			"path":        "README.md",
			"new_content": "after\n",
		}},
	})
	if !result.IsError || !strings.Contains(result.Content, "additional properties") {
		t.Fatalf("result = %#v, want schema rejection for operations", result)
	}
}

func TestNewRejectsUnknownWorkspacePatchInputMode(t *testing.T) {
	t.Parallel()

	_, err := New(Config{
		Workspace:               workspace.NewMemoryStore(nil),
		WorkspacePatchInputMode: WorkspacePatchInputMode("magic"),
	})
	if err == nil || !strings.Contains(err.Error(), `unknown workspace patch input mode "magic"`) {
		t.Fatalf("New() error = %v, want unknown patch input mode", err)
	}
}

func TestNewRejectsUnifiedDiffModeWithoutUnifiedDiffStore(t *testing.T) {
	t.Parallel()

	_, err := New(Config{
		Workspace:               patchOnlyWorkspaceStore{},
		WorkspacePatchInputMode: WorkspacePatchInputUnifiedDiff,
	})
	if err == nil || !strings.Contains(err.Error(), "requires workspace store with unified diff support") {
		t.Fatalf("New() error = %v, want unified diff support error", err)
	}
}

func TestNewCanExposeShellCommandSessionStartTool(t *testing.T) {
	t.Parallel()

	stack, err := New(Config{
		CommandSessions:              commandtools.NewScriptedSessionManager(commandtools.ScriptedCommand{ID: "dev-1"}),
		CommandSessionStartInputMode: CommandSessionStartInputShellCommand,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	startSpec, ok := toolSpec(stack.Registry(), commandtools.StartToolName)
	if !ok {
		t.Fatalf("registry missing %q", commandtools.StartToolName)
	}
	properties, ok := startSpec.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("start input schema properties = %#v, want object", startSpec.InputSchema["properties"])
	}
	command, ok := properties["command"].(map[string]any)
	if !ok {
		t.Fatalf("start input schema = %#v, want command property", startSpec.InputSchema)
	}
	if command["type"] != "string" {
		t.Fatalf("start command schema = %#v, want shell command string", startSpec.InputSchema)
	}
}

func TestNewRejectsUnknownCommandSessionStartInputMode(t *testing.T) {
	t.Parallel()

	_, err := New(Config{
		CommandSessions:              commandtools.NewScriptedSessionManager(commandtools.ScriptedCommand{ID: "dev-1"}),
		CommandSessionStartInputMode: CommandSessionStartInputMode("magic"),
	})
	if err == nil || !strings.Contains(err.Error(), `unknown command session start input mode "magic"`) {
		t.Fatalf("New() error = %v, want unknown command session start input mode", err)
	}
}

func TestNewRejectsCommandSessionStartInputModeWithoutManager(t *testing.T) {
	t.Parallel()

	_, err := New(Config{
		CommandSessionStartInputMode: CommandSessionStartInputShellCommand,
	})
	if err == nil || !strings.Contains(err.Error(), "command session start input mode requires command session manager") {
		t.Fatalf("New() error = %v, want missing command session manager", err)
	}
}

func TestNewClonesBaseHooks(t *testing.T) {
	t.Parallel()

	base := hook.NewRunner(hook.WithBeforeToolUse(func(context.Context, hook.BeforeToolUseInput) (hook.BeforeToolUseResult, error) {
		return hook.BeforeToolUseResult{}, nil
	}))

	stackOne, err := New(Config{
		Base: memaxagent.Options{Hooks: base},
		Workspace: workspace.NewMemoryStore(map[string]string{
			"README.md": "before\n",
		}),
		Verifier: verifytools.Config{
			Verifier: verifytools.VerifierFunc(func(context.Context, verifytools.Request) (verifytools.Result, error) {
				return verifytools.Result{Passed: true}, nil
			}),
		},
		Policies: DefaultPolicies(),
	})
	if err != nil {
		t.Fatalf("first New() error = %v", err)
	}
	stackTwo, err := New(Config{
		Base: memaxagent.Options{Hooks: base},
		Workspace: workspace.NewMemoryStore(map[string]string{
			"README.md": "before\n",
		}),
		Verifier: verifytools.Config{
			Verifier: verifytools.VerifierFunc(func(context.Context, verifytools.Request) (verifytools.Result, error) {
				return verifytools.Result{Passed: true}, nil
			}),
		},
		Policies: DefaultPolicies(),
	})
	if err != nil {
		t.Fatalf("second New() error = %v", err)
	}

	if stackOne.Hooks() == base {
		t.Fatal("first stack reused base hook runner pointer")
	}
	if stackTwo.Hooks() == base {
		t.Fatal("second stack reused base hook runner pointer")
	}
	if stackOne.Hooks() == stackTwo.Hooks() {
		t.Fatal("two stacks unexpectedly share the same hook runner")
	}
}

func TestNewWrapsRollbackVerifier(t *testing.T) {
	t.Parallel()

	ws := workspace.NewMemoryStore(map[string]string{
		"README.md": "before\n",
	})
	stack, err := New(Config{
		Workspace: ws,
		Verifier: verifytools.Config{
			Verifier: verifytools.VerifierFunc(func(context.Context, verifytools.Request) (verifytools.Result, error) {
				return verifytools.Result{
					Name:   "test",
					Passed: false,
					Output: "tests failed",
				}, nil
			}),
		},
		Policies: Policies{
			RecommendRollbackOnFailedVerification: true,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	opts := stack.Options()
	exec := tool.Executor{
		Registry: opts.Tools,
		Hooks:    opts.Hooks,
		Runtime: tool.Runtime{
			SessionID: "session-2",
			Sessions:  opts.Sessions,
		},
	}

	result := runTool(t, exec, workspacetools.CheckpointToolName, map[string]any{
		"label": "before verify",
	})
	if result.IsError {
		t.Fatalf("checkpoint should succeed: %s", result.Content)
	}

	result = runTool(t, exec, verifytools.ToolName, map[string]any{
		"name": "test",
	})
	if !result.IsError {
		t.Fatalf("verification should fail")
	}
	if !strings.Contains(result.Content, "Rollback policy: restore workspace checkpoint") {
		t.Fatalf("verification output missing rollback guidance: %q", result.Content)
	}
	if recommended, _ := result.Metadata[agentpolicy.MetadataRollbackRecommended].(bool); !recommended {
		t.Fatalf("rollback metadata missing recommendation flag: %v", result.Metadata)
	}
}

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config Config
		want   string
	}{
		{
			name: "patch approval without approver",
			config: Config{
				Workspace: workspace.NewMemoryStore(nil),
				Policies: Policies{
					RequirePatchApproval: true,
				},
			},
			want: "patch approval requires approval approver",
		},
		{
			name: "rollback guidance without verifier",
			config: Config{
				Workspace: workspace.NewMemoryStore(nil),
				Policies: Policies{
					RecommendRollbackOnFailedVerification: true,
				},
			},
			want: "rollback guidance requires verifier",
		},
		{
			name: "verify after commands without verifier",
			config: Config{
				Command: commandtools.Config{
					Runner: commandtools.NewScriptedRunner(commandtools.Result{}),
				},
				Policies: Policies{
					RequireVerificationAfterCommands: []agentpolicy.CommandMatcher{
						agentpolicy.MatchCommandPrefix("go", "test"),
					},
				},
			},
			want: "verify-after-commands requires verifier",
		},
		{
			name: "verify after commands with custom command name",
			config: Config{
				Command: commandtools.Config{
					Name:   "project_command",
					Runner: commandtools.NewScriptedRunner(commandtools.Result{}),
				},
				Verifier: verifytools.Config{
					Verifier: verifytools.VerifierFunc(func(context.Context, verifytools.Request) (verifytools.Result, error) {
						return verifytools.Result{Passed: true}, nil
					}),
				},
				Policies: Policies{
					RequireVerificationAfterCommands: []agentpolicy.CommandMatcher{
						agentpolicy.MatchCommandPrefix("go", "test"),
					},
				},
			},
			want: "requires default command tool name",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := New(tc.config)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("New() error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestDefaultPoliciesSmoke(t *testing.T) {
	t.Parallel()

	_, err := New(Config{
		Workspace: workspace.NewMemoryStore(map[string]string{
			"README.md": "before\n",
		}),
		Verifier: verifytools.Config{
			Verifier: verifytools.VerifierFunc(func(context.Context, verifytools.Request) (verifytools.Result, error) {
				return verifytools.Result{Passed: true}, nil
			}),
		},
		Policies: DefaultPolicies(),
	})
	if err != nil {
		t.Fatalf("New(DefaultPolicies) error = %v", err)
	}
}

func TestPresetConfigs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		preset          Preset
		maxTurns        int
		maxConcurrency  int
		commandTimeout  string
		promptSubstring string
	}{
		{
			preset:          PresetSafeLocal,
			maxTurns:        24,
			maxConcurrency:  4,
			commandTimeout:  "0s",
			promptSubstring: "Operate cautiously",
		},
		{
			preset:          PresetCIRepair,
			maxTurns:        32,
			maxConcurrency:  6,
			commandTimeout:  "10m0s",
			promptSubstring: "reproducible repair loops",
		},
		{
			preset:          PresetInteractiveDev,
			maxTurns:        40,
			maxConcurrency:  8,
			commandTimeout:  "10m0s",
			promptSubstring: "managed command sessions",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(string(tc.preset), func(t *testing.T) {
			t.Parallel()
			config, err := tc.preset.Config()
			if err != nil {
				t.Fatalf("Config() error = %v", err)
			}
			if config.Base.MaxTurns != tc.maxTurns {
				t.Fatalf("MaxTurns = %d, want %d", config.Base.MaxTurns, tc.maxTurns)
			}
			if config.Base.MaxToolConcurrency != tc.maxConcurrency {
				t.Fatalf("MaxToolConcurrency = %d, want %d", config.Base.MaxToolConcurrency, tc.maxConcurrency)
			}
			if got := fmt.Sprint(config.Command.DefaultTimeout); got != tc.commandTimeout {
				t.Fatalf("DefaultTimeout = %s, want %s", got, tc.commandTimeout)
			}
			if !strings.Contains(config.Base.AppendSystemPrompt, tc.promptSubstring) {
				t.Fatalf("AppendSystemPrompt = %q, want substring %q", config.Base.AppendSystemPrompt, tc.promptSubstring)
			}
			if !config.Policies.RequireCheckpointBeforePatch ||
				!config.Policies.RecommendRollbackOnFailedVerification ||
				!config.Policies.RequireVerificationBeforeFinal {
				t.Fatalf("preset policies = %+v, want DefaultPolicies protections", config.Policies)
			}
		})
	}
}

func TestPresetConfigUnknown(t *testing.T) {
	t.Parallel()

	_, err := Preset("nope").Config()
	if err == nil || !strings.Contains(err.Error(), `unknown preset "nope"`) {
		t.Fatalf("Config() error = %v, want unknown preset", err)
	}
}

func TestPresetsBuildStacks(t *testing.T) {
	t.Parallel()

	for _, preset := range Presets() {
		preset := preset
		t.Run(string(preset), func(t *testing.T) {
			t.Parallel()

			config, err := preset.Config()
			if err != nil {
				t.Fatalf("Config() error = %v", err)
			}
			config.Workspace = workspace.NewMemoryStore(map[string]string{
				"README.md": "before\n",
			})
			config.Verifier.Verifier = verifytools.VerifierFunc(func(context.Context, verifytools.Request) (verifytools.Result, error) {
				return verifytools.Result{Passed: true}, nil
			})

			if preset != PresetSafeLocal {
				config.Command.Runner = commandtools.NewScriptedRunner(commandtools.Result{
					Argv:     []string{"go", "test", "./..."},
					ExitCode: 0,
				})
			}
			if preset == PresetInteractiveDev {
				config.CommandSessions = commandtools.NewScriptedSessionManager(commandtools.ScriptedCommand{ID: "dev-1"})
			}

			if _, err := New(config); err != nil {
				t.Fatalf("New() error = %v", err)
			}
		})
	}
}

func TestWithModel(t *testing.T) {
	t.Parallel()

	stack := Stack{options: memaxagent.Options{}}
	client := fakeClient{}
	opts := stack.WithModel(client)
	if opts.Model != client {
		t.Fatal("WithModel() did not install provided client")
	}
}

func TestModelProfiles(t *testing.T) {
	t.Parallel()

	if DefaultModelProfile != ModelProfileBalanced {
		t.Fatalf("DefaultModelProfile = %q, want %q", DefaultModelProfile, ModelProfileBalanced)
	}
	profiles := ModelProfiles()
	want := []ModelProfile{ModelProfileFast, ModelProfileBalanced, ModelProfileDeep}
	if fmt.Sprint(profiles) != fmt.Sprint(want) {
		t.Fatalf("ModelProfiles() = %v, want %v", profiles, want)
	}
	profiles[0] = "mutated"
	if got := ModelProfiles()[0]; got != ModelProfileFast {
		t.Fatalf("ModelProfiles() returned shared backing array; first profile = %q", got)
	}
	for _, profile := range want {
		if profile.Description() == "" {
			t.Fatalf("%s Description() is empty", profile)
		}
	}
	if got := ModelProfile("unknown").Description(); got != "" {
		t.Fatalf("unknown Description() = %q, want empty", got)
	}
	if got := ModelProfileDeep.String(); got != "deep" {
		t.Fatalf("ModelProfileDeep.String() = %q, want deep", got)
	}
}

func TestParseModelProfile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		raw  string
		want ModelProfile
	}{
		{raw: "", want: DefaultModelProfile},
		{raw: "  ", want: DefaultModelProfile},
		{raw: "fast", want: ModelProfileFast},
		{raw: "BALANCED", want: ModelProfileBalanced},
		{raw: " Deep ", want: ModelProfileDeep},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(fmt.Sprintf("%q", tc.raw), func(t *testing.T) {
			t.Parallel()

			got, err := ParseModelProfile(tc.raw)
			if err != nil {
				t.Fatalf("ParseModelProfile() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("ParseModelProfile() = %q, want %q", got, tc.want)
			}
		})
	}

	if _, err := ParseModelProfile("experimental"); err == nil || !strings.Contains(err.Error(), `unknown model profile "experimental"`) {
		t.Fatalf("ParseModelProfile() error = %v, want unknown profile", err)
	}
}

func TestModelEfforts(t *testing.T) {
	t.Parallel()

	efforts := ModelEfforts()
	want := []ModelEffort{ModelEffortLow, ModelEffortMedium, ModelEffortHigh, ModelEffortXHigh}
	if fmt.Sprint(efforts) != fmt.Sprint(want) {
		t.Fatalf("ModelEfforts() = %v, want %v", efforts, want)
	}
	efforts[0] = "mutated"
	if got := ModelEfforts()[0]; got != ModelEffortLow {
		t.Fatalf("ModelEfforts() returned shared backing array; first effort = %q", got)
	}
	for _, effort := range append([]ModelEffort{ModelEffortAuto}, want...) {
		if effort.Description() == "" {
			t.Fatalf("%s Description() is empty", effort)
		}
	}
	if got := ModelEffort("unknown").Description(); got != "" {
		t.Fatalf("unknown Description() = %q, want empty", got)
	}
	if got := ModelEffortAuto.String(); got != "auto" {
		t.Fatalf("ModelEffortAuto.String() = %q, want auto", got)
	}
	if got := ModelEffortXHigh.String(); got != "xhigh" {
		t.Fatalf("ModelEffortXHigh.String() = %q, want xhigh", got)
	}
}

func TestParseModelEffort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		raw  string
		want ModelEffort
	}{
		{raw: "", want: ModelEffortAuto},
		{raw: "  ", want: ModelEffortAuto},
		{raw: "auto", want: ModelEffortAuto},
		{raw: "LOW", want: ModelEffortLow},
		{raw: " medium ", want: ModelEffortMedium},
		{raw: "high", want: ModelEffortHigh},
		{raw: "xhigh", want: ModelEffortXHigh},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(fmt.Sprintf("%q", tc.raw), func(t *testing.T) {
			t.Parallel()

			got, err := ParseModelEffort(tc.raw)
			if err != nil {
				t.Fatalf("ParseModelEffort() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("ParseModelEffort() = %q, want %q", got, tc.want)
			}
		})
	}

	if _, err := ParseModelEffort("maximum"); err == nil || !strings.Contains(err.Error(), `unknown model effort "maximum"`) {
		t.Fatalf("ParseModelEffort() error = %v, want unknown effort", err)
	}
}

func TestOpenAIModelOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		profile      ModelProfile
		effort       openai.ReasoningEffort
		verbosity    openai.TextVerbosity
		wantArtifact bool
	}{
		{profile: ModelProfileFast, effort: openai.ReasoningEffortLow, verbosity: openai.TextVerbosityLow, wantArtifact: true},
		{profile: ModelProfileBalanced, effort: openai.ReasoningEffortMedium, verbosity: openai.TextVerbosityMedium, wantArtifact: true},
		{profile: ModelProfileDeep, effort: openai.ReasoningEffortXHigh, verbosity: openai.TextVerbosityHigh, wantArtifact: true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(string(tc.profile), func(t *testing.T) {
			t.Parallel()

			opts, err := OpenAIModelOptions(tc.profile)
			if err != nil {
				t.Fatalf("OpenAIModelOptions() error = %v", err)
			}
			client := openai.New("key", "model", opts...)
			if client.Reasoning == nil || client.Reasoning.Effort != tc.effort {
				t.Fatalf("Reasoning = %+v, want effort %q", client.Reasoning, tc.effort)
			}
			if client.Text == nil || client.Text.Verbosity != tc.verbosity {
				t.Fatalf("Text = %+v, want verbosity %q", client.Text, tc.verbosity)
			}
			if got := contains(client.Include, "reasoning.encrypted_content"); got != tc.wantArtifact {
				t.Fatalf("Include reasoning artifact = %v, want %v; include=%v", got, tc.wantArtifact, client.Include)
			}
		})
	}
}

func TestOpenAIModelEffortOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		effort ModelEffort
		want   openai.ReasoningEffort
	}{
		{effort: ModelEffortLow, want: openai.ReasoningEffortLow},
		{effort: ModelEffortMedium, want: openai.ReasoningEffortMedium},
		{effort: ModelEffortHigh, want: openai.ReasoningEffortHigh},
		{effort: ModelEffortXHigh, want: openai.ReasoningEffortXHigh},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(string(tc.effort), func(t *testing.T) {
			t.Parallel()

			opts, err := OpenAIModelEffortOptions(tc.effort)
			if err != nil {
				t.Fatalf("OpenAIModelEffortOptions() error = %v", err)
			}
			client := openai.New("key", "model", opts...)
			if client.Reasoning == nil || client.Reasoning.Effort != tc.want {
				t.Fatalf("Reasoning = %+v, want effort %q", client.Reasoning, tc.want)
			}
		})
	}

	opts, err := OpenAIModelEffortOptions(ModelEffortAuto)
	if err != nil {
		t.Fatalf("OpenAIModelEffortOptions(auto) error = %v", err)
	}
	if len(opts) != 0 {
		t.Fatalf("OpenAIModelEffortOptions(auto) = %d opts, want 0", len(opts))
	}
	profileOpts, err := OpenAIModelOptions(ModelProfileFast)
	if err != nil {
		t.Fatalf("OpenAIModelOptions(fast) error = %v", err)
	}
	client := openai.New("key", "model", append(profileOpts, opts...)...)
	if client.Reasoning == nil || client.Reasoning.Effort != openai.ReasoningEffortLow {
		t.Fatalf("Reasoning after auto override = %+v, want profile effort low", client.Reasoning)
	}
	if _, err := OpenAIModelEffortOptions("maximum"); err == nil || !strings.Contains(err.Error(), `unknown model effort "maximum"`) {
		t.Fatalf("OpenAIModelEffortOptions() error = %v, want unknown effort", err)
	}
}

func TestAnthropicModelOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		profile ModelProfile
		effort  anthropic.Effort
	}{
		{profile: ModelProfileFast, effort: anthropic.EffortLow},
		{profile: ModelProfileBalanced, effort: anthropic.EffortMedium},
		{profile: ModelProfileDeep, effort: anthropic.EffortXHigh},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(string(tc.profile), func(t *testing.T) {
			t.Parallel()

			opts, err := AnthropicModelOptions(tc.profile)
			if err != nil {
				t.Fatalf("AnthropicModelOptions() error = %v", err)
			}
			client := anthropic.New("key", "model", opts...)
			if client.Effort != tc.effort {
				t.Fatalf("Effort = %q, want %q", client.Effort, tc.effort)
			}
			if client.Thinking == nil || client.Thinking.Type != anthropic.ThinkingAdaptive {
				t.Fatalf("Thinking = %+v, want adaptive", client.Thinking)
			}
		})
	}
}

func TestAnthropicModelEffortOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		effort ModelEffort
		want   anthropic.Effort
	}{
		{effort: ModelEffortLow, want: anthropic.EffortLow},
		{effort: ModelEffortMedium, want: anthropic.EffortMedium},
		{effort: ModelEffortHigh, want: anthropic.EffortHigh},
		{effort: ModelEffortXHigh, want: anthropic.EffortXHigh},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(string(tc.effort), func(t *testing.T) {
			t.Parallel()

			opts, err := AnthropicModelEffortOptions(tc.effort)
			if err != nil {
				t.Fatalf("AnthropicModelEffortOptions() error = %v", err)
			}
			client := anthropic.New("key", "model", opts...)
			if client.Effort != tc.want {
				t.Fatalf("Effort = %q, want %q", client.Effort, tc.want)
			}
		})
	}

	opts, err := AnthropicModelEffortOptions(ModelEffortAuto)
	if err != nil {
		t.Fatalf("AnthropicModelEffortOptions(auto) error = %v", err)
	}
	if len(opts) != 0 {
		t.Fatalf("AnthropicModelEffortOptions(auto) = %d opts, want 0", len(opts))
	}
	profileOpts, err := AnthropicModelOptions(ModelProfileFast)
	if err != nil {
		t.Fatalf("AnthropicModelOptions(fast) error = %v", err)
	}
	client := anthropic.New("key", "model", append(profileOpts, opts...)...)
	if client.Effort != anthropic.EffortLow {
		t.Fatalf("Effort after auto override = %q, want profile effort low", client.Effort)
	}
	if _, err := AnthropicModelEffortOptions("maximum"); err == nil || !strings.Contains(err.Error(), `unknown model effort "maximum"`) {
		t.Fatalf("AnthropicModelEffortOptions() error = %v, want unknown effort", err)
	}
}

func TestModelOptionsRejectUnknownProfile(t *testing.T) {
	t.Parallel()

	if _, err := OpenAIModelOptions("experimental"); err == nil || !strings.Contains(err.Error(), `unknown model profile "experimental"`) {
		t.Fatalf("OpenAIModelOptions() error = %v, want unknown profile", err)
	}
	if _, err := AnthropicModelOptions("experimental"); err == nil || !strings.Contains(err.Error(), `unknown model profile "experimental"`) {
		t.Fatalf("AnthropicModelOptions() error = %v, want unknown profile", err)
	}
}

type fakeClient struct{}

func (fakeClient) Stream(context.Context, model.Request) (model.Stream, error) {
	return nil, nil
}

type patchOnlyWorkspaceStore struct{}

func (patchOnlyWorkspaceStore) ReadFile(context.Context, string) (string, error) {
	return "", nil
}

func (patchOnlyWorkspaceStore) WriteFile(context.Context, string, string) error {
	return nil
}

func (patchOnlyWorkspaceStore) DeleteFile(context.Context, string) error {
	return nil
}

func (patchOnlyWorkspaceStore) ListFiles(context.Context, string) ([]string, error) {
	return nil, nil
}

func (patchOnlyWorkspaceStore) ApplyPatch(context.Context, []workspace.PatchOperation) (workspace.PatchResult, error) {
	return workspace.PatchResult{}, nil
}

func (patchOnlyWorkspaceStore) Diff(context.Context, string) (workspace.Diff, error) {
	return workspace.Diff{}, nil
}

func (patchOnlyWorkspaceStore) Checkpoint(context.Context, workspace.CheckpointOptions) (workspace.Checkpoint, error) {
	return workspace.Checkpoint{}, nil
}

func (patchOnlyWorkspaceStore) Restore(context.Context, string) (workspace.Checkpoint, error) {
	return workspace.Checkpoint{}, nil
}

func (patchOnlyWorkspaceStore) ListCheckpoints(context.Context) ([]workspace.Checkpoint, error) {
	return nil, nil
}

func runTool(t *testing.T, exec tool.Executor, name string, input any) model.ToolResult {
	t.Helper()
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("json.Marshal(%s) error = %v", name, err)
	}
	results := exec.Run(context.Background(), []model.ToolUse{{
		ID:    "tool-" + name,
		Name:  name,
		Input: data,
	}})
	result, ok := <-results
	if !ok {
		t.Fatalf("executor returned no result for %s", name)
	}
	return result
}

func toolNames(registry *tool.Registry) []string {
	specs := registry.Specs()
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		names = append(names, spec.Name)
	}
	return names
}

func toolSpec(registry *tool.Registry, name string) (model.ToolSpec, bool) {
	for _, spec := range registry.Specs() {
		if spec.Name == name {
			return spec, true
		}
	}
	return model.ToolSpec{}, false
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
