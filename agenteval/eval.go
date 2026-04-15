// Package agenteval provides deterministic evaluation helpers for agent
// orchestration behavior.
package agenteval

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

// Assertion verifies one completed evaluation result.
type Assertion struct {
	Name  string
	Check func(Result) error
}

// Case is one agent behavior scenario.
type Case struct {
	Name       string
	Prompt     string
	Options    memaxagent.Options
	Assertions []Assertion
	Timeout    time.Duration
}

// Result is the complete outcome of one Case.
type Result struct {
	Name            string
	Prompt          string
	Final           string
	Events          []memaxagent.Event
	SessionID       string
	ParentSessionID string
	Usage           model.Usage
	Err             error
	Duration        time.Duration
}

// Passed reports whether the case completed and all assertions passed.
func (r Result) Passed() bool {
	return r.Err == nil
}

// ToolUses returns the tool-use events emitted during the case.
func (r Result) ToolUses() []model.ToolUse {
	var uses []model.ToolUse
	for _, event := range r.Events {
		if event.Kind == memaxagent.EventToolUse && event.ToolUse != nil {
			uses = append(uses, *event.ToolUse)
		}
	}
	return uses
}

// ToolResults returns the tool-result events emitted during the case.
func (r Result) ToolResults() []model.ToolResult {
	var results []model.ToolResult
	for _, event := range r.Events {
		if event.Kind == memaxagent.EventToolResult && event.ToolResult != nil {
			results = append(results, *event.ToolResult)
		}
	}
	return results
}

// Report is the outcome of running a set of cases.
type Report struct {
	StartedAt  time.Time
	FinishedAt time.Time
	Results    []Result
}

// Passed reports whether every case passed.
func (r Report) Passed() bool {
	if len(r.Results) == 0 {
		return true
	}
	for _, result := range r.Results {
		if !result.Passed() {
			return false
		}
	}
	return true
}

// Error returns a compact summary of failed cases.
func (r Report) Error() error {
	var failures []string
	for _, result := range r.Results {
		if result.Err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", result.Name, result.Err))
		}
	}
	if len(failures) == 0 {
		return nil
	}
	return errors.New(strings.Join(failures, "; "))
}

// Runner runs agent evaluation cases with shared default options.
type Runner struct {
	Options memaxagent.Options
	Timeout time.Duration
}

// Run executes each case, continuing after failures so callers get a complete
// report for the suite.
func (r Runner) Run(ctx context.Context, cases ...Case) Report {
	report := Report{
		StartedAt: time.Now().UTC(),
		Results:   make([]Result, 0, len(cases)),
	}
	for _, c := range cases {
		report.Results = append(report.Results, r.runCase(ctx, c))
	}
	report.FinishedAt = time.Now().UTC()
	return report
}

func (r Runner) runCase(ctx context.Context, c Case) (result Result) {
	name := strings.TrimSpace(c.Name)
	if name == "" {
		name = "unnamed"
	}
	result = Result{Name: name, Prompt: c.Prompt}
	started := time.Now()
	defer func() {
		result.Duration = time.Since(started)
	}()

	timeout := c.Timeout
	if timeout <= 0 {
		timeout = r.Timeout
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	events, err := memaxagent.Query(ctx, c.Prompt, mergeOptions(r.Options, c.Options))
	if err != nil {
		result.Err = err
		return result
	}
	for event := range events {
		result.Events = append(result.Events, event)
		if event.SessionID != "" {
			result.SessionID = event.SessionID
		}
		if event.ParentSessionID != "" {
			result.ParentSessionID = event.ParentSessionID
		}
		switch event.Kind {
		case memaxagent.EventError:
			if result.Err == nil {
				result.Err = event.Err
			}
		case memaxagent.EventResult:
			result.Final = event.Result
			if event.Usage != nil {
				result.Usage = *event.Usage
			}
		case memaxagent.EventUsage:
			if event.Usage != nil {
				result.Usage = result.Usage.Add(*event.Usage)
			}
		}
	}
	if result.Err != nil {
		return result
	}
	for _, assertion := range c.Assertions {
		if assertion.Check == nil {
			continue
		}
		if err := assertion.Check(result); err != nil {
			name := strings.TrimSpace(assertion.Name)
			if name == "" {
				result.Err = err
			} else {
				result.Err = fmt.Errorf("%s: %w", name, err)
			}
			return result
		}
	}
	return result
}

func mergeOptions(base memaxagent.Options, override memaxagent.Options) memaxagent.Options {
	if override.Model != nil {
		base.Model = override.Model
	}
	if override.Tools != nil {
		base.Tools = override.Tools
	}
	if override.Permissions != nil {
		base.Permissions = override.Permissions
	}
	if override.Sessions != nil {
		base.Sessions = override.Sessions
	}
	if override.Hooks != nil {
		base.Hooks = override.Hooks
	}
	if override.Context != nil {
		base.Context = override.Context
	}
	if override.ContextRetry != nil {
		base.ContextRetry = override.ContextRetry
	}
	if override.ToolSelector != nil {
		base.ToolSelector = override.ToolSelector
	}
	if override.ResultStore != nil {
		base.ResultStore = override.ResultStore
	}
	if override.Output.Enabled() || override.Output.MaxRetries != 0 {
		base.Output = override.Output
	}
	if override.Tracer != nil {
		base.Tracer = override.Tracer
	}
	if override.Meter != nil {
		base.Meter = override.Meter
	}
	if override.PromptBuilder != nil {
		base.PromptBuilder = override.PromptBuilder
	}
	if override.PromptProfile != "" {
		base.PromptProfile = override.PromptProfile
	}
	if !override.Identity.IsZero() {
		base.Identity = override.Identity
	}
	if override.MemorySource != nil {
		base.MemorySource = override.MemorySource
	}
	if override.Memories != nil {
		base.Memories = override.Memories
	}
	if override.SkillSource != nil {
		base.SkillSource = override.SkillSource
	}
	if override.Skills != nil {
		base.Skills = override.Skills
	}
	if override.SystemPrompt != "" {
		base.SystemPrompt = override.SystemPrompt
	}
	if override.AppendSystemPrompt != "" {
		base.AppendSystemPrompt = override.AppendSystemPrompt
	}
	if override.SessionID != "" {
		base.SessionID = override.SessionID
	}
	if override.ParentSessionID != "" {
		base.ParentSessionID = override.ParentSessionID
	}
	if override.MaxTurns != 0 {
		base.MaxTurns = override.MaxTurns
	}
	if override.MaxToolConcurrency != 0 {
		base.MaxToolConcurrency = override.MaxToolConcurrency
	}
	if override.MaxRunDuration != 0 {
		base.MaxRunDuration = override.MaxRunDuration
	}
	return base
}
