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
	"github.com/MemaxLabs/memax-go-agent-sdk/memory"
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
	// AllowError lets assertions inspect an expected agent/query error without
	// making the case fail before assertions run. Unexpected errors are still
	// recorded as Result.RunErr and become Result.Err when AllowError is false.
	AllowError bool
	// Cleanup releases resources created for the case. It is called once after
	// the event stream is drained or Query fails.
	Cleanup func()
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
	// RunErr is the error emitted by Query or returned while starting the run.
	// It is separate from Err so cases can assert expected failures.
	RunErr   error
	Err      error
	Duration time.Duration
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

// MemoryCandidates returns memory candidates emitted during the case.
func (r Result) MemoryCandidates() []memory.Candidate {
	var candidates []memory.Candidate
	for _, event := range r.Events {
		if event.Kind == memaxagent.EventMemoryCandidates && event.Memory != nil {
			candidates = append(candidates, event.Memory.Candidates...)
		}
	}
	return candidates
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
		if c.Cleanup != nil {
			c.Cleanup()
		}
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

	events, err := memaxagent.Query(ctx, c.Prompt, r.Options.Merge(c.Options))
	if err != nil {
		result.RunErr = err
		if !c.AllowError {
			result.Err = err
		}
		return result
	}
	var eventUsage model.Usage
	resultUsage := false
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
			if result.RunErr == nil {
				result.RunErr = event.Err
			}
		case memaxagent.EventResult:
			result.Final = event.Result
			if event.Usage != nil {
				result.Usage = *event.Usage
				resultUsage = true
			}
		case memaxagent.EventUsage:
			if event.Usage != nil {
				eventUsage = eventUsage.Add(*event.Usage)
			}
		}
	}
	if !resultUsage {
		result.Usage = eventUsage
	}
	if result.RunErr != nil && !c.AllowError {
		result.Err = result.RunErr
		return result
	}
	var assertionErrors []error
	for _, assertion := range c.Assertions {
		if assertion.Check == nil {
			continue
		}
		if err := assertion.Check(result); err != nil {
			name := strings.TrimSpace(assertion.Name)
			if name == "" {
				assertionErrors = append(assertionErrors, err)
			} else {
				assertionErrors = append(assertionErrors, fmt.Errorf("%s: %w", name, err))
			}
		}
	}
	result.Err = errors.Join(assertionErrors...)
	return result
}
