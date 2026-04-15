package memaxagent

import (
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/contextwindow"
	"github.com/MemaxLabs/memax-go-agent-sdk/hook"
	"github.com/MemaxLabs/memax-go-agent-sdk/identity"
	"github.com/MemaxLabs/memax-go-agent-sdk/memory"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/output"
	"github.com/MemaxLabs/memax-go-agent-sdk/permission"
	"github.com/MemaxLabs/memax-go-agent-sdk/prompt"
	"github.com/MemaxLabs/memax-go-agent-sdk/resultstore"
	"github.com/MemaxLabs/memax-go-agent-sdk/session"
	"github.com/MemaxLabs/memax-go-agent-sdk/skill"
	"github.com/MemaxLabs/memax-go-agent-sdk/telemetry"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

const defaultMaxTurns = 50

// Options configures one agent run.
type Options struct {
	Model model.Client

	Tools         *tool.Registry
	Permissions   permission.Checker
	Sessions      session.Store
	Hooks         *hook.Runner
	Context       contextwindow.Policy
	ContextRetry  contextwindow.Policy
	ToolSelector  tool.Selector
	ResultStore   resultstore.Store
	Output        output.Contract
	Tracer        telemetry.Tracer
	Meter         telemetry.Meter
	PromptBuilder prompt.Builder
	PromptProfile prompt.Profile
	Identity      identity.Identity
	MemorySource  memory.Source
	Memories      []memory.Memory
	SkillSource   skill.Source
	Skills        []skill.Skill

	SystemPrompt       string
	AppendSystemPrompt string
	SessionID          string
	ParentSessionID    string
	MaxTurns           int
	MaxToolConcurrency int
	MaxRunDuration     time.Duration
}

func (o Options) withDefaults() Options {
	if o.MaxTurns <= 0 {
		o.MaxTurns = defaultMaxTurns
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
