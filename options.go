package memaxagent

import (
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/budget"
	"github.com/MemaxLabs/memax-go-agent-sdk/contextwindow"
	"github.com/MemaxLabs/memax-go-agent-sdk/hook"
	"github.com/MemaxLabs/memax-go-agent-sdk/identity"
	"github.com/MemaxLabs/memax-go-agent-sdk/memory"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/output"
	"github.com/MemaxLabs/memax-go-agent-sdk/permission"
	"github.com/MemaxLabs/memax-go-agent-sdk/planner"
	"github.com/MemaxLabs/memax-go-agent-sdk/prompt"
	"github.com/MemaxLabs/memax-go-agent-sdk/resultstore"
	"github.com/MemaxLabs/memax-go-agent-sdk/session"
	"github.com/MemaxLabs/memax-go-agent-sdk/skill"
	"github.com/MemaxLabs/memax-go-agent-sdk/telemetry"
	"github.com/MemaxLabs/memax-go-agent-sdk/tenant"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

const (
	defaultMaxTurns        = 50
	defaultMaxFinalDenials = 3
)

// Options configures one agent run.
type Options struct {
	Model model.Client

	Tools           *tool.Registry
	Permissions     permission.Checker
	Sessions        session.Store
	Hooks           *hook.Runner
	Context         contextwindow.Policy
	ContextRetry    contextwindow.Policy
	ToolSelector    tool.Selector
	Budget          budget.Governor
	ResultStore     resultstore.Store
	Output          output.Contract
	Tracer          telemetry.Tracer
	Meter           telemetry.Meter
	PromptBuilder   prompt.Builder
	PromptProfile   prompt.Profile
	Identity        identity.Identity
	Tenant          tenant.Scope
	TenantValidator tenant.Validator
	Planner         planner.Policy
	MemorySource    memory.Source
	MemoryDistiller memory.Distiller
	// MemoryCandidateHandler handles distilled memory candidates after they are
	// emitted as EventMemoryCandidates. Nil preserves the safe default: propose
	// candidates but do not persist them. Handler errors emit
	// EventMemoryCandidateHandlerError but do not prevent the final result.
	MemoryCandidateHandler memory.CandidateHandler
	Memories               []memory.Memory
	SkillSource            skill.Source
	SkillResourceSource    skill.ResourceSource
	SkillDisclosure        skill.DisclosureMode
	Skills                 []skill.Skill

	SystemPrompt       string
	AppendSystemPrompt string
	SessionID          string
	ParentSessionID    string
	MaxTurns           int
	// MaxFinalDenials controls how many before-final denials may be repaired
	// with transcript prompts before the run fails. Zero uses the SDK default;
	// a negative value disables finalization retries.
	MaxFinalDenials    int
	MaxToolConcurrency int
	MaxRunDuration     time.Duration
}

// Merge returns a copy of o with non-zero fields from override applied.
// Slice fields replace the base when override provides a non-nil slice,
// including an explicitly empty slice. This is used by composed agents and
// evals to share the same option override semantics as new fields are added.
func (o Options) Merge(override Options) Options {
	if override.Model != nil {
		o.Model = override.Model
	}
	if override.Tools != nil {
		o.Tools = override.Tools
	}
	if override.Permissions != nil {
		o.Permissions = override.Permissions
	}
	if override.Sessions != nil {
		o.Sessions = override.Sessions
	}
	if override.Hooks != nil {
		o.Hooks = override.Hooks
	}
	if override.Context != nil {
		o.Context = override.Context
	}
	if override.ContextRetry != nil {
		o.ContextRetry = override.ContextRetry
	}
	if override.ToolSelector != nil {
		o.ToolSelector = override.ToolSelector
	}
	if override.Budget != nil {
		o.Budget = override.Budget
	}
	if override.ResultStore != nil {
		o.ResultStore = override.ResultStore
	}
	if override.Output.Enabled() || override.Output.MaxRetries != 0 {
		o.Output = override.Output
	}
	if override.Tracer != nil {
		o.Tracer = override.Tracer
	}
	if override.Meter != nil {
		o.Meter = override.Meter
	}
	if override.PromptBuilder != nil {
		o.PromptBuilder = override.PromptBuilder
	}
	if override.PromptProfile != "" {
		o.PromptProfile = override.PromptProfile
	}
	if !override.Identity.IsZero() {
		o.Identity = override.Identity
	}
	if !override.Tenant.IsZero() {
		o.Tenant = override.Tenant.Clone()
	}
	if override.TenantValidator != nil {
		o.TenantValidator = override.TenantValidator
	}
	if override.Planner != nil {
		o.Planner = override.Planner
	}
	if override.MemorySource != nil {
		o.MemorySource = override.MemorySource
	}
	if override.MemoryDistiller != nil {
		o.MemoryDistiller = override.MemoryDistiller
	}
	if override.MemoryCandidateHandler != nil {
		o.MemoryCandidateHandler = override.MemoryCandidateHandler
	}
	if override.Memories != nil {
		o.Memories = append([]memory.Memory(nil), override.Memories...)
	}
	if override.SkillSource != nil {
		o.SkillSource = override.SkillSource
	}
	if override.SkillResourceSource != nil {
		o.SkillResourceSource = override.SkillResourceSource
	}
	if override.SkillDisclosure != "" {
		o.SkillDisclosure = override.SkillDisclosure
	}
	if override.Skills != nil {
		o.Skills = append([]skill.Skill(nil), override.Skills...)
	}
	if override.SystemPrompt != "" {
		o.SystemPrompt = override.SystemPrompt
	}
	if override.AppendSystemPrompt != "" {
		o.AppendSystemPrompt = override.AppendSystemPrompt
	}
	if override.SessionID != "" {
		o.SessionID = override.SessionID
	}
	if override.ParentSessionID != "" {
		o.ParentSessionID = override.ParentSessionID
	}
	if override.MaxTurns != 0 {
		o.MaxTurns = override.MaxTurns
	}
	if override.MaxFinalDenials != 0 {
		o.MaxFinalDenials = override.MaxFinalDenials
	}
	if override.MaxToolConcurrency != 0 {
		o.MaxToolConcurrency = override.MaxToolConcurrency
	}
	if override.MaxRunDuration != 0 {
		o.MaxRunDuration = override.MaxRunDuration
	}
	return o
}

func (o Options) withDefaults() Options {
	if o.MaxTurns <= 0 {
		o.MaxTurns = defaultMaxTurns
	}
	if o.MaxFinalDenials == 0 {
		o.MaxFinalDenials = defaultMaxFinalDenials
	}
	if o.MaxToolConcurrency <= 0 {
		o.MaxToolConcurrency = tool.DefaultMaxConcurrency
	}
	if o.Tools == nil {
		o.Tools = tool.NewRegistry()
	}
	if o.Permissions == nil {
		o.Permissions = permission.AllowAll{}
	}
	if o.Sessions == nil {
		o.Sessions = session.NewMemoryStore()
	}
	if o.Tracer == nil {
		o.Tracer = telemetry.NoopTracer{}
	}
	if o.Meter == nil {
		o.Meter = telemetry.NoopMeter{}
	}
	return o
}
