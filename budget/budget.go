// Package budget defines provider-neutral run budget governors for agent runs.
package budget

import (
	"context"
	"fmt"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

// Governor decides whether an agent run may continue at a stable lifecycle
// boundary.
type Governor interface {
	Check(context.Context, Snapshot) Decision
}

// GovernorFunc adapts a function to Governor.
type GovernorFunc func(context.Context, Snapshot) Decision

// Check calls f(ctx, snapshot).
func (f GovernorFunc) Check(ctx context.Context, snapshot Snapshot) Decision {
	if f == nil {
		return Decision{Allow: true}
	}
	return f(ctx, snapshot)
}

// Decision is the result of a budget check.
type Decision struct {
	Allow  bool
	Reason string
}

// Snapshot describes current or prospective resource usage for one run. Agent
// integration points pass prospective counts before starting a turn, model
// call, or tool batch, and observed token usage after model streaming.
type Snapshot struct {
	StartedAt  time.Time
	Now        time.Time
	Elapsed    time.Duration
	Turns      int
	ModelCalls int
	ToolCalls  int
	Usage      model.Usage
}

// Policy is a zero-value-disabled budget governor. Positive limits are enforced
// independently; zero means unlimited for that dimension.
//
// MaxTurns is a budget limit checked by the agent loop at turn boundaries. It
// is separate from memaxagent.Options.MaxTurns, which is the hard loop bound.
// When both are configured, the lower effective limit wins; a Policy.MaxTurns
// denial finishes the run with the budget stop reason, while Options.MaxTurns
// exhaustion finishes with the max-turns stop reason.
type Policy struct {
	MaxTurns        int
	MaxModelCalls   int
	MaxToolCalls    int
	MaxInputTokens  int
	MaxOutputTokens int
	MaxTotalTokens  int
	MaxDuration     time.Duration
}

// Enabled reports whether any limit is configured.
func (p Policy) Enabled() bool {
	return p.MaxTurns > 0 ||
		p.MaxModelCalls > 0 ||
		p.MaxToolCalls > 0 ||
		p.MaxInputTokens > 0 ||
		p.MaxOutputTokens > 0 ||
		p.MaxTotalTokens > 0 ||
		p.MaxDuration > 0
}

// Check reports whether snapshot remains within every configured limit.
func (p Policy) Check(ctx context.Context, snapshot Snapshot) Decision {
	if err := ctx.Err(); err != nil {
		return Decision{Allow: false, Reason: err.Error()}
	}
	if !p.Enabled() {
		return Decision{Allow: true}
	}
	if snapshot.Now.IsZero() {
		snapshot.Now = time.Now().UTC()
	}
	if snapshot.Elapsed <= 0 && !snapshot.StartedAt.IsZero() {
		snapshot.Elapsed = snapshot.Now.Sub(snapshot.StartedAt)
	}
	if p.MaxDuration > 0 && snapshot.Elapsed > p.MaxDuration {
		return exceeded("max duration", snapshot.Elapsed.String(), p.MaxDuration.String())
	}
	if p.MaxTurns > 0 && snapshot.Turns > p.MaxTurns {
		return exceeded("max turns", snapshot.Turns, p.MaxTurns)
	}
	if p.MaxModelCalls > 0 && snapshot.ModelCalls > p.MaxModelCalls {
		return exceeded("max model calls", snapshot.ModelCalls, p.MaxModelCalls)
	}
	if p.MaxToolCalls > 0 && snapshot.ToolCalls > p.MaxToolCalls {
		return exceeded("max tool calls", snapshot.ToolCalls, p.MaxToolCalls)
	}
	if p.MaxInputTokens > 0 && snapshot.Usage.InputTokens > p.MaxInputTokens {
		return exceeded("max input tokens", snapshot.Usage.InputTokens, p.MaxInputTokens)
	}
	if p.MaxOutputTokens > 0 && snapshot.Usage.OutputTokens > p.MaxOutputTokens {
		return exceeded("max output tokens", snapshot.Usage.OutputTokens, p.MaxOutputTokens)
	}
	if p.MaxTotalTokens > 0 && snapshot.Usage.TotalTokens > p.MaxTotalTokens {
		return exceeded("max total tokens", snapshot.Usage.TotalTokens, p.MaxTotalTokens)
	}
	return Decision{Allow: true}
}

func exceeded(name string, got any, limit any) Decision {
	return Decision{
		Allow:  false,
		Reason: fmt.Sprintf("budget exceeded: %s (%v > %v)", name, got, limit),
	}
}
