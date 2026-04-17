package coding

import (
	"fmt"
	"time"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/approvaltools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/commandtools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/verifytools"
)

// Preset identifies a named coding-workflow profile.
//
// Presets intentionally keep the governance baseline uniform through
// DefaultPolicies. The primary differences between profiles are prompt
// guidance, turn/concurrency budgets, and command/session posture.
type Preset string

const (
	// PresetSafeLocal is a cautious local-editing profile for explicit,
	// verification-driven repair loops.
	PresetSafeLocal Preset = "safe_local"
	// PresetCIRepair is a non-interactive repair profile tuned for longer test
	// and build loops in CI-like environments.
	PresetCIRepair Preset = "ci_repair"
	// PresetInteractiveDev is a longer-horizon interactive profile for managed
	// command sessions such as watchers, dev servers, and REPL-style tooling.
	PresetInteractiveDev Preset = "interactive_dev"
)

var allPresets = []Preset{
	PresetSafeLocal,
	PresetCIRepair,
	PresetInteractiveDev,
}

// Presets returns the supported coding presets in stable order.
func Presets() []Preset {
	return append([]Preset(nil), allPresets...)
}

// Config returns the default stack configuration for p. Callers should fill in
// host-owned backends such as Workspace, Verifier, Command, Tasks, or Approval
// before passing the resulting config to New.
func (p Preset) Config() (Config, error) {
	switch p {
	case PresetSafeLocal:
		return SafeLocal(), nil
	case PresetCIRepair:
		return CIRepair(), nil
	case PresetInteractiveDev:
		return InteractiveDev(), nil
	default:
		return Config{}, fmt.Errorf("coding stack: unknown preset %q", p)
	}
}

// SafeLocal returns a cautious local-coding stack profile. It keeps the
// default governance protections on, biases toward bounded turns and lower
// concurrency, and adds prompt guidance for small verified edits.
func SafeLocal() Config {
	return Config{
		Base: memaxagent.Options{
			MaxTurns:           24,
			MaxToolConcurrency: 4,
			AppendSystemPrompt: "Operate cautiously in the local workspace. Prefer small edits, checkpoint before risky changes, and verify before finalizing.",
		},
		Command: commandtools.Config{
			ConcurrencySafe: false,
		},
		Verifier: verifytools.Config{
			ConcurrencySafe: true,
		},
		Approval: approvaltools.Config{
			ConcurrencySafe: true,
		},
		Policies: DefaultPolicies(),
	}
}

// CIRepair returns a non-interactive repair profile tuned for repeated build,
// test, and verification loops. It keeps the default governance protections
// while extending command time budgets and overall turn budget for longer CI
// repair passes.
func CIRepair() Config {
	return Config{
		Base: memaxagent.Options{
			MaxTurns:           32,
			MaxToolConcurrency: 6,
			AppendSystemPrompt: "Focus on reproducible repair loops. Prefer deterministic verification, minimal changes, and re-run the relevant checks before finalizing.",
		},
		Command: commandtools.Config{
			ConcurrencySafe: false,
			DefaultTimeout:  10 * time.Minute,
			MaxTimeout:      30 * time.Minute,
		},
		Verifier: verifytools.Config{
			ConcurrencySafe: true,
		},
		Approval: approvaltools.Config{
			ConcurrencySafe: true,
		},
		Policies: DefaultPolicies(),
	}
}

// InteractiveDev returns a long-horizon profile for coding workflows that use
// managed sessions such as watchers, dev servers, and interactive terminals. It
// keeps the default governance protections and adds prompt guidance about
// starting, observing, and stopping long-lived command sessions.
func InteractiveDev() Config {
	return Config{
		Base: memaxagent.Options{
			MaxTurns:           40,
			MaxToolConcurrency: 8,
			AppendSystemPrompt: "Use managed command sessions when continuous feedback helps. Read incremental output, keep long-running sessions organized, and stop sessions you no longer need.",
		},
		Command: commandtools.Config{
			ConcurrencySafe: false,
			DefaultTimeout:  10 * time.Minute,
			MaxTimeout:      30 * time.Minute,
		},
		Verifier: verifytools.Config{
			ConcurrencySafe: true,
		},
		Approval: approvaltools.Config{
			ConcurrencySafe: true,
		},
		Policies: DefaultPolicies(),
	}
}
