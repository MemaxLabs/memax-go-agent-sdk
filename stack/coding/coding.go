// Package coding provides an opinionated coding-agent stack over the neutral
// Memax runtime.
//
// The package assembles explicit host-owned tools and policy presets into one
// reusable coding workflow configuration without changing the core kernel:
// workspace editing, command execution, managed command sessions, verification,
// approval requests, task planning, and common safety policies remain normal
// tools and hooks.
package coding

import (
	"fmt"
	"strings"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/hook"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/planner"
	"github.com/MemaxLabs/memax-go-agent-sdk/session"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/agentpolicy"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/approvaltools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/commandtools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/tasktools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/verifytools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/workspacetools"
	"github.com/MemaxLabs/memax-go-agent-sdk/workspace"
)

// Config assembles a coding-agent stack from explicit host-owned components.
//
// Base carries neutral memaxagent.Options that should remain in force. Base
// tool registries are cloned before stack tools are registered, so the caller's
// registry is never mutated. If Base already registers one of the stack's tool
// names, New returns the underlying registry duplicate-name error. Base hooks
// are cloned before stack policies are added, so callers can safely reuse a
// base option bundle across multiple stacks.
type Config struct {
	Base     memaxagent.Options
	Sessions session.Store

	Workspace     workspace.Store
	PatchReviewer workspacetools.PatchReviewer
	// WorkspacePatchInputMode controls the model-facing input schema for
	// workspace_apply_patch. The zero value keeps the flexible SDK schema.
	// CLIs should prefer WorkspacePatchInputUnifiedDiff to reduce malformed
	// patch calls from models that only need one edit representation.
	WorkspacePatchInputMode WorkspacePatchInputMode

	Tasks                       tasktools.Store
	TaskPlannerOptions          []planner.TaskSourceOption
	DisableVerificationProgress bool
	VerificationProgressOptions []tasktools.VerificationProgressOption

	Verifier        verifytools.Config
	Command         commandtools.Config
	CommandSessions commandtools.SessionManager
	// CommandSessionStartInputMode controls the model-facing input schema for
	// start_command. The zero value keeps the exact-argv SDK schema. CLIs
	// should prefer CommandSessionStartInputShellCommand so models can start
	// long-running commands with the same shell-string shape as run_command.
	CommandSessionStartInputMode CommandSessionStartInputMode
	Approval                     approvaltools.Config

	Policies Policies
}

// WorkspacePatchInputMode selects the input contract exposed by the
// workspace_apply_patch tool in the coding stack.
type WorkspacePatchInputMode string

const (
	// WorkspacePatchInputFlexible accepts either structured operations or a
	// unified diff. This is the default SDK surface for hosts that want both
	// machine-generated guarded edits and diff-based edits.
	WorkspacePatchInputFlexible WorkspacePatchInputMode = ""
	// WorkspacePatchInputUnifiedDiff accepts only unified_diff plus dry_run.
	// This is the preferred coding-agent CLI surface because it removes the
	// mutually-exclusive operations/unified_diff choice from the model.
	WorkspacePatchInputUnifiedDiff WorkspacePatchInputMode = "unified_diff"
)

// CommandSessionStartInputMode selects the input contract exposed by the
// start_command tool in the coding stack.
type CommandSessionStartInputMode string

const (
	// CommandSessionStartInputArgv accepts an exact argv vector. This is the
	// default SDK surface for hosts that need shell-free process semantics.
	CommandSessionStartInputArgv CommandSessionStartInputMode = ""
	// CommandSessionStartInputShellCommand accepts a shell command string.
	// This is the preferred coding-agent CLI surface because it matches
	// run_command and avoids argv construction errors.
	CommandSessionStartInputShellCommand CommandSessionStartInputMode = "shell_command"
)

// Policies configures the optional safety and governance layer for the coding
// stack. The zero value is intentionally conservative about hidden behavior:
// policies are disabled until the host opts in.
type Policies struct {
	RequireCheckpointBeforePatch          bool
	RecommendRollbackOnFailedVerification bool
	RequireVerificationBeforeFinal        bool
	RequirePatchApproval                  bool

	AllowCommands                    []agentpolicy.CommandMatcher
	DenyCommands                     []agentpolicy.CommandMatcher
	RequireApprovalBeforeCommands    []agentpolicy.CommandMatcher
	RequireVerificationAfterCommands []agentpolicy.CommandMatcher

	SingleUseApprovals  bool
	InputBoundApprovals bool
}

// DefaultPolicies returns a practical default coding-governance preset.
//
// The default enables checkpoint-before-patch, rollback guidance on failed
// verification, and verify-before-final. Approval-related policies remain off
// until the host provides an approval tool and explicitly opts into those
// gates. When approval policies are enabled, the default strictness is
// single-use and input-bound.
func DefaultPolicies() Policies {
	return Policies{
		RequireCheckpointBeforePatch:          true,
		RecommendRollbackOnFailedVerification: true,
		RequireVerificationBeforeFinal:        true,
		SingleUseApprovals:                    true,
		InputBoundApprovals:                   true,
	}
}

// Stack is one assembled coding runtime profile.
type Stack struct {
	options memaxagent.Options
}

// New assembles a coding-agent stack from the configured host-owned
// capabilities. Returned options are ready to pass to memaxagent.Query after
// the caller sets a model.
func New(config Config) (Stack, error) {
	if err := validateConfig(config); err != nil {
		return Stack{}, err
	}

	opts := config.Base
	opts.Hooks = cloneHooks(opts.Hooks)
	if config.Sessions != nil {
		opts.Sessions = config.Sessions
	}

	registry := cloneRegistry(opts.Tools)
	wrappedVerifier, rollbackPolicy := configureVerifier(config)
	if err := registerTools(registry, config, wrappedVerifier); err != nil {
		return Stack{}, err
	}
	opts.Tools = registry

	if opts.Planner == nil && config.Tasks != nil {
		opts.Planner = tasktools.Planner(config.Tasks, defaultPlannerOptions(config)...)
	}
	if err := installHooks(&opts, config, rollbackPolicy); err != nil {
		return Stack{}, err
	}

	return Stack{options: opts}, nil
}

// Options returns the assembled agent options. The returned value is a copy of
// the stack-level option struct; referenced registries and hook runners remain
// shared.
func (s Stack) Options() memaxagent.Options {
	return s.options
}

// WithModel returns assembled options with client installed as the model.
func (s Stack) WithModel(client model.Client) memaxagent.Options {
	opts := s.options
	opts.Model = client
	return opts
}

// Registry returns the assembled tool registry.
func (s Stack) Registry() *tool.Registry {
	return s.options.Tools
}

// Hooks returns the assembled hook runner.
func (s Stack) Hooks() *hook.Runner {
	return s.options.Hooks
}

func validateConfig(config Config) error {
	policies := config.Policies
	if config.PatchReviewer != nil && config.Workspace == nil {
		return fmt.Errorf("coding stack: patch reviewer requires workspace store")
	}
	switch config.WorkspacePatchInputMode {
	case WorkspacePatchInputFlexible, WorkspacePatchInputUnifiedDiff:
	default:
		return fmt.Errorf("coding stack: unknown workspace patch input mode %q", config.WorkspacePatchInputMode)
	}
	if config.WorkspacePatchInputMode != WorkspacePatchInputFlexible && config.Workspace == nil {
		return fmt.Errorf("coding stack: workspace patch input mode requires workspace store")
	}
	if config.WorkspacePatchInputMode == WorkspacePatchInputUnifiedDiff {
		if _, ok := any(config.Workspace).(workspacetools.UnifiedDiffPatchStore); !ok {
			return fmt.Errorf("coding stack: unified-diff patch input mode requires workspace store with unified diff support")
		}
	}
	switch config.CommandSessionStartInputMode {
	case CommandSessionStartInputArgv, CommandSessionStartInputShellCommand:
	default:
		return fmt.Errorf("coding stack: unknown command session start input mode %q", config.CommandSessionStartInputMode)
	}
	if config.CommandSessionStartInputMode != CommandSessionStartInputArgv && config.CommandSessions == nil {
		return fmt.Errorf("coding stack: command session start input mode requires command session manager")
	}
	if policies.RequireCheckpointBeforePatch && config.Workspace == nil {
		return fmt.Errorf("coding stack: checkpoint-before-patch requires workspace store")
	}
	if policies.RequirePatchApproval {
		if config.Workspace == nil {
			return fmt.Errorf("coding stack: patch approval requires workspace store")
		}
		if config.Approval.Approver == nil {
			return fmt.Errorf("coding stack: patch approval requires approval approver")
		}
	}
	if policies.RecommendRollbackOnFailedVerification && config.Verifier.Verifier == nil {
		return fmt.Errorf("coding stack: rollback guidance requires verifier")
	}
	if policies.RequireVerificationBeforeFinal && config.Verifier.Verifier == nil {
		return fmt.Errorf("coding stack: verify-before-final requires verifier")
	}
	if len(policies.AllowCommands) > 0 || len(policies.DenyCommands) > 0 ||
		len(policies.RequireApprovalBeforeCommands) > 0 || len(policies.RequireVerificationAfterCommands) > 0 {
		if config.Command.Runner == nil {
			return fmt.Errorf("coding stack: command policies require command runner")
		}
	}
	if len(policies.RequireApprovalBeforeCommands) > 0 && config.Approval.Approver == nil {
		return fmt.Errorf("coding stack: command approval requires approval approver")
	}
	if len(policies.RequireVerificationAfterCommands) > 0 {
		if config.Verifier.Verifier == nil {
			return fmt.Errorf("coding stack: verify-after-commands requires verifier")
		}
		if commandToolName(config.Command) != commandtools.ToolName {
			return fmt.Errorf("coding stack: verify-after-commands requires default command tool name %q", commandtools.ToolName)
		}
	}
	return nil
}

func configureVerifier(config Config) (verifytools.Verifier, *agentpolicy.RollbackOnFailedVerification) {
	verifier := config.Verifier.Verifier
	if verifier != nil && config.Tasks != nil && !config.DisableVerificationProgress {
		verifier = tasktools.NewVerificationProgressVerifier(
			config.Tasks,
			verifier,
			config.VerificationProgressOptions...,
		)
	}
	var rollbackPolicy *agentpolicy.RollbackOnFailedVerification
	if config.Policies.RecommendRollbackOnFailedVerification {
		rollbackPolicy = agentpolicy.RecommendRollbackOnFailedVerification()
		verifier = rollbackPolicy.WrapVerifier(verifier)
	}
	return verifier, rollbackPolicy
}

func registerTools(registry *tool.Registry, config Config, verifier verifytools.Verifier) error {
	if config.Workspace != nil {
		patchTool := workspacetools.NewApplyPatchToolWithReview(config.Workspace, config.PatchReviewer)
		if config.WorkspacePatchInputMode == WorkspacePatchInputUnifiedDiff {
			unifiedStore, ok := any(config.Workspace).(workspacetools.UnifiedDiffPatchStore)
			if !ok {
				return fmt.Errorf("coding stack: unified-diff patch input mode requires workspace store with unified diff support")
			}
			patchTool = workspacetools.NewUnifiedDiffApplyPatchToolWithReview(unifiedStore, config.PatchReviewer)
		}
		tools := []tool.Tool{
			workspacetools.NewReadTool(config.Workspace),
			workspacetools.NewListTool(config.Workspace),
			patchTool,
			workspacetools.NewDiffTool(config.Workspace),
			workspacetools.NewCheckpointTool(config.Workspace),
			workspacetools.NewRestoreTool(config.Workspace),
		}
		if err := registerAll(registry, tools...); err != nil {
			return err
		}
	}
	if verifier != nil {
		verifyConfig := config.Verifier
		verifyConfig.Verifier = verifier
		if err := registry.Register(verifytools.NewTool(verifyConfig)); err != nil {
			return err
		}
	}
	if config.Command.Runner != nil {
		if err := registry.Register(commandtools.NewTool(config.Command)); err != nil {
			return err
		}
	}
	if config.CommandSessions != nil {
		var sessionTools []tool.Tool
		var err error
		if config.CommandSessionStartInputMode == CommandSessionStartInputShellCommand {
			sessionTools, err = commandtools.NewShellSessionTools(config.CommandSessions, config.Command.Shell)
		} else {
			sessionTools, err = commandtools.NewSessionTools(config.CommandSessions)
		}
		if err != nil {
			return err
		}
		if err := registerAll(registry, sessionTools...); err != nil {
			return err
		}
	}
	if config.Tasks != nil {
		if err := registerAll(registry,
			tasktools.NewListTool(config.Tasks),
			tasktools.NewUpsertTool(config.Tasks),
			tasktools.NewDeleteTool(config.Tasks),
		); err != nil {
			return err
		}
	}
	if config.Approval.Approver != nil {
		if err := registry.Register(approvaltools.NewTool(config.Approval)); err != nil {
			return err
		}
	}
	return nil
}

func installHooks(opts *memaxagent.Options, config Config, rollbackPolicy *agentpolicy.RollbackOnFailedVerification) error {
	var hookOptions []hook.Option
	policies := config.Policies

	if policies.RequireCheckpointBeforePatch {
		hookOptions = append(hookOptions, agentpolicy.RequireCheckpointBeforePatch().Options()...)
	}
	if rollbackPolicy != nil {
		hookOptions = append(hookOptions, rollbackPolicy.Options()...)
	}
	if policies.RequireVerificationBeforeFinal {
		hookOptions = append(hookOptions, agentpolicy.RequireVerificationBeforeFinal().Options()...)
	}
	if policies.RequirePatchApproval {
		hookOptions = append(hookOptions, patchApprovalPolicy(config).Options()...)
	}
	if len(policies.AllowCommands) > 0 {
		hookOptions = append(hookOptions, agentpolicy.AllowCommandsWithOptions(
			policies.AllowCommands,
			agentpolicy.WithCommandToolName(commandToolName(config.Command)),
		).Options()...)
	}
	if len(policies.DenyCommands) > 0 {
		hookOptions = append(hookOptions, agentpolicy.DenyCommandsWithOptions(
			policies.DenyCommands,
			agentpolicy.WithCommandToolName(commandToolName(config.Command)),
		).Options()...)
	}
	if len(policies.RequireApprovalBeforeCommands) > 0 {
		hookOptions = append(hookOptions, commandApprovalPolicy(config).Options()...)
	}
	if len(policies.RequireVerificationAfterCommands) > 0 {
		hookOptions = append(hookOptions, agentpolicy.RequireVerificationAfterCommands(
			policies.RequireVerificationAfterCommands...,
		).Options()...)
	}
	if cleaner, ok := any(config.CommandSessions).(commandtools.Cleaner); ok {
		hookOptions = append(hookOptions, commandtools.SessionCleanupOptions(cleaner)...)
	}
	if len(hookOptions) == 0 {
		return nil
	}
	if opts.Hooks == nil {
		opts.Hooks = hook.NewRunner()
	}
	for _, option := range hookOptions {
		if option != nil {
			option(opts.Hooks)
		}
	}
	return nil
}

func patchApprovalPolicy(config Config) *agentpolicy.ApprovalBeforeTool {
	options := []agentpolicy.ApprovalBeforeToolOption{
		agentpolicy.WithApprovalToolName(approvalToolName(config.Approval)),
	}
	if config.Policies.SingleUseApprovals {
		options = append(options, agentpolicy.WithSingleUseApprovals())
	}
	if config.Policies.InputBoundApprovals {
		options = append(options, agentpolicy.WithInputBoundApprovals())
	}
	return agentpolicy.RequireApprovalBeforeToolsWithOptions(
		[]string{workspacetools.ApplyPatchToolName},
		options...,
	)
}

func commandApprovalPolicy(config Config) *agentpolicy.CommandApprovalPolicy {
	options := []agentpolicy.CommandApprovalPolicyOption{
		agentpolicy.WithCommandApprovalToolName(approvalToolName(config.Approval)),
		agentpolicy.WithCommandApprovalCommandToolName(commandToolName(config.Command)),
	}
	if config.Policies.SingleUseApprovals {
		options = append(options, agentpolicy.WithCommandSingleUseApprovals())
	}
	if config.Policies.InputBoundApprovals {
		options = append(options, agentpolicy.WithCommandInputBoundApprovals())
	}
	return agentpolicy.RequireApprovalBeforeCommands(
		config.Policies.RequireApprovalBeforeCommands,
		options...,
	)
}

func defaultPlannerOptions(config Config) []planner.TaskSourceOption {
	var toolHints []string
	if config.Workspace != nil {
		toolHints = append(toolHints,
			workspacetools.ReadToolName,
			workspacetools.ListToolName,
			workspacetools.ApplyPatchToolName,
			workspacetools.DiffToolName,
			workspacetools.CheckpointToolName,
			workspacetools.RestoreToolName,
		)
	}
	if config.Command.Runner != nil {
		toolHints = append(toolHints, commandToolName(config.Command))
	}
	if config.CommandSessions != nil {
		toolHints = append(toolHints,
			commandtools.StartToolName,
			commandtools.ReadOutputToolName,
			commandtools.WaitOutputToolName,
			commandtools.StopToolName,
			commandtools.ListToolName,
		)
		if _, ok := any(config.CommandSessions).(commandtools.Writer); ok {
			toolHints = append(toolHints, commandtools.WriteInputToolName)
		}
		if _, ok := any(config.CommandSessions).(commandtools.Resizer); ok {
			toolHints = append(toolHints, commandtools.ResizeToolName)
		}
	}

	options := make([]planner.TaskSourceOption, 0, 2+len(config.TaskPlannerOptions))
	if len(toolHints) > 0 {
		options = append(options, planner.WithTaskToolHints(toolHints...))
	}
	if config.Verifier.Verifier != nil {
		options = append(options, planner.WithTaskVerificationHints(verificationToolName(config.Verifier)))
	}
	options = append(options, config.TaskPlannerOptions...)
	return options
}

func cloneRegistry(registry *tool.Registry) *tool.Registry {
	if registry == nil {
		return tool.NewRegistry()
	}
	return registry.Clone()
}

func cloneHooks(runner *hook.Runner) *hook.Runner {
	if runner == nil {
		return nil
	}
	return runner.Clone()
}

func registerAll(registry *tool.Registry, tools ...tool.Tool) error {
	for _, t := range tools {
		if err := registry.Register(t); err != nil {
			return err
		}
	}
	return nil
}

func commandToolName(config commandtools.Config) string {
	if name := strings.TrimSpace(config.Name); name != "" {
		return name
	}
	return commandtools.ToolName
}

func verificationToolName(config verifytools.Config) string {
	if name := strings.TrimSpace(config.Name); name != "" {
		return name
	}
	return verifytools.ToolName
}

func approvalToolName(config approvaltools.Config) string {
	if name := strings.TrimSpace(config.Name); name != "" {
		return name
	}
	return approvaltools.ToolName
}
