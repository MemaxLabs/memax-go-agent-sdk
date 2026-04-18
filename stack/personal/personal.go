// Package personal provides an opinionated personal-intelligence stack over the
// neutral Memax runtime.
//
// The package assembles explicit host-owned tools and policy presets into one
// reusable workflow configuration without changing the core kernel: durable
// memory recall and mutation, note/document search and mutation, task
// planning, approval requests, scoped delegation, and skill disclosure remain
// normal prompt inputs, tools, and hooks.
package personal

import (
	"fmt"
	"strings"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/hook"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/planner"
	"github.com/MemaxLabs/memax-go-agent-sdk/session"
	"github.com/MemaxLabs/memax-go-agent-sdk/skill"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/agentpolicy"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/approvaltools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/memorytools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/notetools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/subagents"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/tasktools"
)

// Config assembles a personal-intelligence stack from explicit host-owned
// components.
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

	Memory memorytools.Config
	Notes  notetools.Config

	Tasks              tasktools.Store
	TaskPlannerOptions []planner.TaskSourceOption

	SkillSource         skill.Source
	SkillResourceSource skill.ResourceSource
	SkillDisclosure     skill.DisclosureMode

	Approval  approvaltools.Config
	Subagents *subagents.Config

	Policies Policies
}

// Policies configures optional governance for the personal stack. The zero
// value is intentionally conservative about hidden behavior: policies are
// disabled until the host opts in or uses a preset.
type Policies struct {
	RequireMemoryApproval     bool
	RequireNoteApproval       bool
	RequireDelegationApproval bool

	SingleUseApprovals  bool
	InputBoundApprovals bool
}

// DefaultPolicies returns a practical default personal-governance preset.
//
// Durable memory mutation is gated behind explicit approval because it affects
// user-facing long-lived context. Delegation approval remains opt-in because
// subagent use is often a workflow choice rather than a durable-state mutation.
func DefaultPolicies() Policies {
	return Policies{
		RequireMemoryApproval: true,
		RequireNoteApproval:   true,
		SingleUseApprovals:    true,
		InputBoundApprovals:   true,
	}
}

// Stack is one assembled personal runtime profile.
type Stack struct {
	options memaxagent.Options
}

// New assembles a personal-intelligence stack from configured host-owned
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
	if opts.MemorySource == nil && config.Memory.Source != nil {
		opts.MemorySource = config.Memory.Source
	}
	if opts.SkillSource == nil && config.SkillSource != nil {
		opts.SkillSource = config.SkillSource
	}
	if opts.SkillResourceSource == nil && config.SkillResourceSource != nil {
		opts.SkillResourceSource = config.SkillResourceSource
	}
	if opts.SkillDisclosure == "" && config.SkillDisclosure != "" {
		opts.SkillDisclosure = config.SkillDisclosure
	}

	registry := cloneRegistry(opts.Tools)
	if err := registerTools(registry, config); err != nil {
		return Stack{}, err
	}
	opts.Tools = registry

	if opts.Planner == nil && config.Tasks != nil {
		opts.Planner = tasktools.Planner(config.Tasks, defaultPlannerOptions(config)...)
	}
	if err := installHooks(&opts, config); err != nil {
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
	if config.Policies.RequireMemoryApproval && hasMutableMemoryTools(config.Memory) && config.Approval.Approver == nil {
		return fmt.Errorf("personal stack: memory approval requires approval approver")
	}
	if config.Policies.RequireNoteApproval && hasMutableNoteTools(config.Notes) && config.Approval.Approver == nil {
		return fmt.Errorf("personal stack: note approval requires approval approver")
	}
	if config.Policies.RequireDelegationApproval && config.Subagents != nil && config.Approval.Approver == nil {
		return fmt.Errorf("personal stack: delegation approval requires approval approver")
	}
	return nil
}

func registerTools(registry *tool.Registry, config Config) error {
	if configuredMemory(config.Memory) {
		tools, err := memorytools.NewTools(config.Memory)
		if err != nil {
			return err
		}
		if err := registerAll(registry, tools...); err != nil {
			return err
		}
	}
	if configuredNotes(config.Notes) {
		tools, err := notetools.NewTools(config.Notes)
		if err != nil {
			return err
		}
		if err := registerAll(registry, tools...); err != nil {
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
	if config.Subagents != nil {
		subagentConfig, err := configuredSubagents(config)
		if err != nil {
			return err
		}
		delegate, err := subagents.NewTool(subagentConfig)
		if err != nil {
			return err
		}
		if err := registry.Register(delegate); err != nil {
			return err
		}
	}
	return nil
}

func installHooks(opts *memaxagent.Options, config Config) error {
	var hookOptions []hook.Option
	policies := config.Policies

	if policies.RequireMemoryApproval && hasMutableMemoryTools(config.Memory) {
		hookOptions = append(hookOptions, memoryApprovalPolicy(config).Options()...)
	}
	if policies.RequireNoteApproval && hasMutableNoteTools(config.Notes) {
		hookOptions = append(hookOptions, noteApprovalPolicy(config).Options()...)
	}
	if policies.RequireDelegationApproval && config.Subagents != nil {
		hookOptions = append(hookOptions, delegationApprovalPolicy(config).Options()...)
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

func memoryApprovalPolicy(config Config) *agentpolicy.ApprovalBeforeTool {
	options := []agentpolicy.ApprovalBeforeToolOption{
		agentpolicy.WithApprovalToolName(approvalToolName(config.Approval)),
	}
	if config.Policies.SingleUseApprovals {
		options = append(options, agentpolicy.WithSingleUseApprovals())
	}
	if config.Policies.InputBoundApprovals {
		options = append(options, agentpolicy.WithInputBoundApprovals())
	}
	return agentpolicy.RequireApprovalBeforeToolsWithOptions(memoryMutationToolNames(config.Memory), options...)
}

func delegationApprovalPolicy(config Config) *agentpolicy.ApprovalBeforeTool {
	options := []agentpolicy.ApprovalBeforeToolOption{
		agentpolicy.WithApprovalToolName(approvalToolName(config.Approval)),
	}
	if config.Policies.SingleUseApprovals {
		options = append(options, agentpolicy.WithSingleUseApprovals())
	}
	if config.Policies.InputBoundApprovals {
		options = append(options, agentpolicy.WithInputBoundApprovals())
	}
	return agentpolicy.RequireApprovalBeforeToolsWithOptions([]string{subagentToolName(config.Subagents)}, options...)
}

func noteApprovalPolicy(config Config) *agentpolicy.ApprovalBeforeTool {
	options := []agentpolicy.ApprovalBeforeToolOption{
		agentpolicy.WithApprovalToolName(approvalToolName(config.Approval)),
	}
	if config.Policies.SingleUseApprovals {
		options = append(options, agentpolicy.WithSingleUseApprovals())
	}
	if config.Policies.InputBoundApprovals {
		options = append(options, agentpolicy.WithInputBoundApprovals())
	}
	return agentpolicy.RequireApprovalBeforeToolsWithOptions(noteMutationToolNames(config.Notes), options...)
}

func defaultPlannerOptions(config Config) []planner.TaskSourceOption {
	var toolHints []string
	if config.Memory.Source != nil {
		toolHints = append(toolHints, memorySearchToolName(config.Memory))
	}
	if config.Memory.Writer != nil {
		toolHints = append(toolHints, memorySaveToolName(config.Memory))
	}
	if config.Memory.Deleter != nil {
		toolHints = append(toolHints, memoryDeleteToolName(config.Memory))
	}
	if config.Notes.Searcher != nil {
		toolHints = append(toolHints, noteSearchToolName(config.Notes))
	}
	if config.Notes.Reader != nil {
		toolHints = append(toolHints, noteReadToolName(config.Notes))
	}
	if config.Notes.Writer != nil {
		toolHints = append(toolHints, noteSaveToolName(config.Notes))
	}
	if config.Notes.Deleter != nil {
		toolHints = append(toolHints, noteDeleteToolName(config.Notes))
	}
	if config.Tasks != nil {
		toolHints = append(toolHints,
			tasktools.ListToolName,
			tasktools.UpsertToolName,
			tasktools.DeleteToolName,
		)
	}
	if config.Approval.Approver != nil {
		toolHints = append(toolHints, approvalToolName(config.Approval))
	}
	if config.Subagents != nil {
		toolHints = append(toolHints, subagentToolName(config.Subagents))
	}
	if config.SkillSource != nil && config.SkillDisclosure == skill.DisclosureProgressive {
		toolHints = append(toolHints, skill.LoadToolName)
		if config.SkillResourceSource != nil {
			toolHints = append(toolHints, skill.ResourceToolName)
		}
	}

	options := make([]planner.TaskSourceOption, 0, 1+len(config.TaskPlannerOptions))
	if len(toolHints) > 0 {
		options = append(options, planner.WithTaskToolHints(toolHints...))
	}
	options = append(options, config.TaskPlannerOptions...)
	return options
}

func configuredSubagents(config Config) (subagents.Config, error) {
	cfg := *config.Subagents
	// Child agents inherit the stack's posture by default so delegated work
	// keeps the same identity, durable context sources, note discovery/loading,
	// and skill-disclosure policy unless the host overrides those fields
	// explicitly. Note mutation is intentionally not inherited by default.
	cfg.DefaultOptions = config.Base.Merge(cfg.DefaultOptions)
	if cfg.DefaultOptions.MemorySource == nil && config.Memory.Source != nil {
		cfg.DefaultOptions.MemorySource = config.Memory.Source
	}
	if inherited, err := inheritSubagentNotes(cfg.DefaultOptions, config.Notes); err != nil {
		return subagents.Config{}, err
	} else {
		cfg.DefaultOptions = inherited
	}
	if cfg.DefaultOptions.SkillSource == nil && config.SkillSource != nil {
		cfg.DefaultOptions.SkillSource = config.SkillSource
	}
	if cfg.DefaultOptions.SkillResourceSource == nil && config.SkillResourceSource != nil {
		cfg.DefaultOptions.SkillResourceSource = config.SkillResourceSource
	}
	if cfg.DefaultOptions.SkillDisclosure == "" && config.SkillDisclosure != "" {
		cfg.DefaultOptions.SkillDisclosure = config.SkillDisclosure
	}
	if cfg.PlanSource == nil && config.Tasks != nil {
		cfg.PlanSource = tasktools.SubagentPlanner(config.Tasks, defaultPlannerOptions(config)...)
	}
	if cfg.ResultHandler == nil && config.Tasks != nil {
		cfg.ResultHandler = tasktools.NewSubagentProgressHandler(config.Tasks)
	}
	return cfg, nil
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

func configuredMemory(config memorytools.Config) bool {
	return config.Source != nil || config.Writer != nil || config.Deleter != nil
}

func hasMutableMemoryTools(config memorytools.Config) bool {
	return config.Writer != nil || config.Deleter != nil
}

func configuredNotes(config notetools.Config) bool {
	return config.Searcher != nil || config.Reader != nil || config.Writer != nil || config.Deleter != nil
}

func hasMutableNoteTools(config notetools.Config) bool {
	return config.Writer != nil || config.Deleter != nil
}

func inheritSubagentNotes(opts memaxagent.Options, config notetools.Config) (memaxagent.Options, error) {
	if config.Searcher == nil && config.Reader == nil {
		return opts, nil
	}
	registry := cloneRegistry(opts.Tools)
	if config.Searcher != nil {
		name := noteSearchToolName(config)
		if !registryHasTool(registry, name) {
			searchTool, err := notetools.NewSearchTool(notetools.Config{
				Searcher:       config.Searcher,
				SearchName:     config.SearchName,
				DefaultLimit:   config.DefaultLimit,
				MaxResultBytes: config.MaxResultBytes,
			})
			if err != nil {
				return opts, err
			}
			if err := registry.Register(searchTool); err != nil {
				return opts, err
			}
		}
	}
	if config.Reader != nil {
		name := noteReadToolName(config)
		if !registryHasTool(registry, name) {
			readTool, err := notetools.NewReadTool(notetools.Config{
				Reader:         config.Reader,
				ReadName:       config.ReadName,
				MaxResultBytes: config.MaxResultBytes,
			})
			if err != nil {
				return opts, err
			}
			if err := registry.Register(readTool); err != nil {
				return opts, err
			}
		}
	}
	opts.Tools = registry
	return opts, nil
}

func registryHasTool(registry *tool.Registry, name string) bool {
	if registry == nil {
		return false
	}
	for _, spec := range registry.Specs() {
		if spec.Name == name {
			return true
		}
	}
	return false
}

func memoryMutationToolNames(config memorytools.Config) []string {
	names := make([]string, 0, 2)
	if config.Writer != nil {
		names = append(names, memorySaveToolName(config))
	}
	if config.Deleter != nil {
		names = append(names, memoryDeleteToolName(config))
	}
	return names
}

func noteMutationToolNames(config notetools.Config) []string {
	names := make([]string, 0, 2)
	if config.Writer != nil {
		names = append(names, noteSaveToolName(config))
	}
	if config.Deleter != nil {
		names = append(names, noteDeleteToolName(config))
	}
	return names
}

func memorySearchToolName(config memorytools.Config) string {
	if name := strings.TrimSpace(config.SearchName); name != "" {
		return name
	}
	return memorytools.SearchToolName
}

func memorySaveToolName(config memorytools.Config) string {
	if name := strings.TrimSpace(config.SaveName); name != "" {
		return name
	}
	return memorytools.SaveToolName
}

func memoryDeleteToolName(config memorytools.Config) string {
	if name := strings.TrimSpace(config.DeleteName); name != "" {
		return name
	}
	return memorytools.DeleteToolName
}

func noteSearchToolName(config notetools.Config) string {
	if name := strings.TrimSpace(config.SearchName); name != "" {
		return name
	}
	return notetools.SearchToolName
}

func noteReadToolName(config notetools.Config) string {
	if name := strings.TrimSpace(config.ReadName); name != "" {
		return name
	}
	return notetools.ReadToolName
}

func noteSaveToolName(config notetools.Config) string {
	if name := strings.TrimSpace(config.SaveName); name != "" {
		return name
	}
	return notetools.SaveToolName
}

func noteDeleteToolName(config notetools.Config) string {
	if name := strings.TrimSpace(config.DeleteName); name != "" {
		return name
	}
	return notetools.DeleteToolName
}

func approvalToolName(config approvaltools.Config) string {
	if name := strings.TrimSpace(config.Name); name != "" {
		return name
	}
	return approvaltools.ToolName
}

func subagentToolName(config *subagents.Config) string {
	if config != nil {
		if name := strings.TrimSpace(config.Name); name != "" {
			return name
		}
	}
	return subagents.ToolName
}
