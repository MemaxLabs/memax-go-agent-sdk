<!-- memax:start -->
## Memax — Persistent Memory

You have access to Memax, a persistent cloud knowledge hub shared across all your AI agents.
Use it proactively — don't wait for the user to ask.

**At session start:** Use memax_recall to check for relevant context about the current project or task.
**During work:** When you discover important decisions, architecture details, debugging solutions,
or useful context — use memax_push to save them for future sessions.
**At session end:** Summarize key decisions, learnings, or context worth remembering and push them.

**What to remember:** Architecture decisions, API conventions, deployment processes, debugging
solutions, team preferences, project-specific knowledge. If you'd want to know it in a future
session, push it now.

**What NOT to remember:** Ephemeral task details, file contents, obvious things.
<!-- memax:end -->

# Memax Agent SDK

This repository is a Go SDK for building autonomous agents with application-owned tools. The goal is high-quality autonomous orchestration without hard-coding access to the real filesystem, shell, browser, network, or OS permission model.

## Project Direction

- Build a Go-native SDK, not a TypeScript port.
- Use `.reference/ts-source-code` only as read-only architecture reference.
- Do not copy upstream source into SDK implementation files.
- Keep the orchestration layer provider-neutral and filesystem-neutral.
- Treat tools as the only capability boundary. If an agent can read, write, search, execute, browse, or ask a user, that capability must go through the tool interface.
- Prefer small, composable packages over a large monolithic runtime.

## Engineering Standards

- Run `gofmt` on changed Go files.
- Run `go test ./...` before finishing code changes.
- Keep exported APIs documented.
- Prefer context-aware APIs and honor cancellation.
- Avoid package cycles; shared protocol types belong in small leaf packages.
- Use deterministic fakes in tests instead of real model calls.
- Add tests around orchestration behavior, especially ordering, permission decisions, cancellation, compaction, and session persistence.

## Architecture Rules

- The root package `memaxagent` should expose the primary SDK experience.
- `model` owns provider-neutral message, stream, request, and tool-use protocol types.
- `tool` owns registry, execution, and tool helper APIs.
- `permission` owns reusable policy checkers and host approval seams.
- `session` owns conversation persistence, not workspace persistence.
- Future workspace or virtual filesystem packages must remain optional tool dependencies, not core agent dependencies.

## Reference Notes

The TypeScript source reference shows useful implementation patterns:

- query engine owns the turn lifecycle
- model output streams as events
- tool-use blocks drive the follow-up loop
- safe read/search tools may run concurrently
- mutating tools run serially
- validation, permissions, hooks, and execution are distinct phases
- session history persists messages and tool results
- compaction and retry paths are core autonomy features

Use these as design signals, not as code to copy.
