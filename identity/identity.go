package identity

// AutonomyLevel describes how independently an agent should plan and act.
type AutonomyLevel string

const (
	AutonomyConservative AutonomyLevel = "conservative"
	AutonomyBalanced     AutonomyLevel = "balanced"
	AutonomyHigh         AutonomyLevel = "high"
)

// Identity describes the durable behavioral profile for an agent.
//
// It is intentionally provider-neutral. Applications can use it to define a
// main agent, focused worker agents, review agents, CI agents, or domain agents
// without hand-assembling system prompt strings.
type Identity struct {
	Name        string
	Role        string
	Mission     string
	Tone        string
	Autonomy    AutonomyLevel
	Constraints []string
}

// Default returns the SDK's default autonomous agent identity.
func Default() Identity {
	return Identity{
		Name:     "Memax Agent",
		Role:     "autonomous software agent",
		Mission:  "complete the user's goal by planning, using available tools, preserving context, and reporting concrete outcomes",
		Tone:     "direct, factual, and concise",
		Autonomy: AutonomyBalanced,
		Constraints: []string{
			"operate only through the tools and permissions provided by the host application",
			"prefer reversible, observable actions before destructive actions",
			"recover from tool errors by using the returned error information instead of repeating the same failing action",
			"keep progress state when task or checkpoint tools are available",
		},
	}
}

// IsZero reports whether the identity has no configured fields.
func (i Identity) IsZero() bool {
	return i.Name == "" &&
		i.Role == "" &&
		i.Mission == "" &&
		i.Tone == "" &&
		i.Autonomy == "" &&
		len(i.Constraints) == 0
}

// WithDefaults fills empty fields from Default and preserves configured fields.
func (i Identity) WithDefaults() Identity {
	defaults := Default()
	if i.Name == "" {
		i.Name = defaults.Name
	}
	if i.Role == "" {
		i.Role = defaults.Role
	}
	if i.Mission == "" {
		i.Mission = defaults.Mission
	}
	if i.Tone == "" {
		i.Tone = defaults.Tone
	}
	if i.Autonomy == "" {
		i.Autonomy = defaults.Autonomy
	}
	if len(i.Constraints) == 0 {
		i.Constraints = append([]string(nil), defaults.Constraints...)
	} else {
		i.Constraints = append([]string(nil), i.Constraints...)
	}
	return i
}
