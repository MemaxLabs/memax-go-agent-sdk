// Package memaxagent provides a Go-native agent orchestration SDK.
//
// The package is intentionally provider- and filesystem-neutral. Agent autonomy
// comes from the orchestration loop, session state, context-window policy,
// permissions, hooks, tool selection, and the tool execution contract. Concrete
// tools decide whether they operate on a real filesystem, virtual filesystem,
// remote API, browser sandbox, database, or in-memory workspace.
//
// The main entry point is Query. Query creates or resumes a session, persists
// the user prompt, streams a provider-neutral model.Client, executes requested
// tools through a tool.Registry, appends tool results, and continues until the
// model returns a final assistant message or a configured stop condition is
// reached. Callers consume the returned Event channel to drive CLIs, servers,
// tests, logs, traces, or custom UIs.
//
// Callers can supply raw system prompts or use the prompt and identity packages
// to assemble deterministic prompt parts from an agent profile, active tools,
// selected skills, and host instructions. The raw prompt path remains available
// for embedders that need complete control.
//
// QueryAsync starts the same run shape without blocking the caller on startup
// I/O. This is useful for HTTP and WebSocket servers that want all session,
// hook, and prompt setup work to happen outside the request dispatch path.
//
// Applications provide capabilities by registering tools. The core never
// bypasses the tool layer for files, shell commands, network calls, approvals,
// checkpoints, task state, or delegation. This keeps policy and workspace
// ownership in the host application while preserving a reusable autonomous loop.
package memaxagent
