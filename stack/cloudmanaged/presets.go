package cloudmanaged

import (
	"fmt"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
)

// Preset identifies a named cloud-managed workflow profile.
//
// The first managed preset keeps the governance baseline explicit through
// DefaultPolicies. Its primary differences from neutral Options are tenant-aware
// prompt guidance and a bounded turn/concurrency posture suitable for hosted
// workers.
type Preset string

const (
	// PresetManagedWorker is a tenant-aware hosted-worker profile that expects
	// the host to attach a tenant scope to each run and enforce per-session
	// quota through the tenant seam.
	PresetManagedWorker Preset = "managed_worker"
)

var allPresets = []Preset{
	PresetManagedWorker,
}

// Presets returns the supported cloud-managed presets in stable order.
func Presets() []Preset {
	return append([]Preset(nil), allPresets...)
}

// Config returns the default stack configuration for p.
func (p Preset) Config() (Config, error) {
	switch p {
	case PresetManagedWorker:
		return ManagedWorker(), nil
	default:
		return Config{}, fmt.Errorf("cloud-managed stack: unknown preset %q", p)
	}
}

// ManagedWorker returns a tenant-aware hosted-worker profile. It keeps the
// managed governance baseline on, biases toward bounded turns and concurrency,
// and adds prompt guidance about respecting tenant scope and quota.
func ManagedWorker() Config {
	return Config{
		Base: memaxagent.Options{
			MaxTurns:           24,
			MaxToolConcurrency: 4,
			AppendSystemPrompt: "Operate within the tenant's explicit scope and quota. Prefer small recoverable steps, keep tool use proportional to the task, and avoid spending model or tool budget on speculative work.",
		},
		Policies: DefaultPolicies(),
	}
}
