// Package memaxagent provides a Go-native agent orchestration SDK.
//
// The package is intentionally provider- and filesystem-neutral. Agent
// autonomy comes from the orchestration loop, session state, permission hooks,
// and tool execution contract. Concrete tools decide whether they operate on a
// real filesystem, virtual filesystem, remote API, or in-memory workspace.
package memaxagent
