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

## Code Review Process

When reviewing code changes (commits, PRs, or diffs), follow this process in order.

### Phase 1: Understand

Before forming any opinion, build a complete picture of what changed and why.

- Read the full diff. Do not skip files. Read every changed line.
- Read surrounding context. If a function was modified, read the full function and its callers. If a new type was added, read the package it belongs to. If interfaces changed, find all implementations.
- Understand intent. What problem does this solve? What user-facing or developer-facing behavior changes? What was the alternative approach and why was this one chosen?
- Trace data flow. Follow new types and fields from definition through construction, agent loop integration, serialization, and test coverage. A field that is defined but never set is a bug. A field that is set but never read is dead code.
- Check backward compatibility. Does existing code that does not use the new feature still work identically? Look for changes to default behavior, interface additions, and new required fields.

### Phase 2: Verify Correctness and Quality

Evaluate the changes against these criteria, in priority order.

**Correctness (blocking)**
- Does the code do what it claims? Trace the happy path and the error paths.
- Are edge cases handled? Empty inputs, nil values, zero structs, context cancellation, concurrent access, and the boundary between "nothing configured" and "explicitly configured to empty."
- Are errors propagated correctly? Wrapped with context? Returned to callers who can act on them? Not swallowed silently?
- Is concurrency safe? Check for shared mutable state, missing locks, goroutine leaks, and channel lifecycle.
- Do tests actually verify the behavior they claim to test? Read assertions carefully. A test that passes for the wrong reason is worse than no test.

**Architecture (blocking for structural issues)**
- Does this belong in the right package? Check for package cycle risks and separation of concerns. Refer to the Architecture Rules section and `docs/architecture.md`.
- Are interfaces minimal? A new interface should have the fewest methods that satisfy the use cases. Prefer optional extension interfaces (like `StoreWithFork`) over growing the base interface.
- Is the API consistent with existing SDK patterns? Compare naming, constructor style (`New*`), option handling (`With*`), error handling, and test helpers against existing packages.
- Does this introduce coupling that will be hard to undo? A toolkit package importing the root `memaxagent` package is fine. The root package importing a toolkit package is not.

**Maintainability (important)**
- Is the code readable without the PR description? Clear names, documented exported symbols, logical grouping.
- Is complexity justified? A binary search is warranted for budget truncation. A binary search is not warranted for a 5-element list.
- Are helper functions extracted where they reduce duplication, but not where they obscure flow?
- Will this be easy to modify in 6 months when requirements change?

**User experience (important)**
- How does a developer discover and use this feature? Is the zero-value safe? Are defaults sensible? Does the feature compose with existing features?
- What error messages does a developer see when they misconfigure this? Are those messages actionable?
- Does the feature work without reading the source code, by reading only the doc comments and examples?

**Performance (check when relevant)**
- Is this on the per-turn hot path (context policy, tool selection, prompt assembly)? If so, allocation count and computational complexity matter.
- Is this on the per-session path (session creation, fork)? Acceptable latency is higher.
- Is this on the per-query path (startup, options validation)? Runs once, almost anything is fine.
- Reference the existing benchmarks in `contextwindow/`, `tool/`, and `session/` for baseline expectations.

### Phase 3: Report

Structure the review output as follows.

1. **Verdict** — One sentence: is this good to merge, good with changes, or needs rework?

2. **What's good** — Call out the best design decisions. Be specific about why they are good. This is not politeness; it locks in patterns that should be repeated.

3. **Issues** — List problems in priority order:
   - **Correctness bugs** — Must fix. Include the file, the problematic code, and why it is wrong.
   - **Architecture concerns** — Must fix if structural, otherwise flag for discussion. Explain what coupling or complexity this introduces.
   - **Observations** — Non-blocking notes. Performance considerations, future extensibility risks, inconsistencies with existing patterns, or suggestions for follow-up work.

4. **Status update** — Where does the project stand after this change? Reference the roadmap phases and checklist items from `docs/roadmap.md`.

5. **Next steps** — What should be built next, based on the roadmap and what this change enables or reveals?

### Review Standards

When evaluating design decisions, apply these standards drawn from Go ecosystem best practices and this SDK's established patterns.

**Interface design**
- Accept interfaces, return structs. Callers should depend on behavior, not implementation.
- Keep interfaces small. `io.Reader` has one method. `session.Store` has three. Grow via optional interfaces (`StoreWithFork`), not by adding methods to the base.
- The zero value should be useful or clearly invalid. `Identity{}` is detected by `IsZero()`. `TokenBudget{}` returns an error for `MaxTokens <= 0`.

**Error handling**
- Wrap errors with context: `fmt.Errorf("create session: %w", err)`.
- Return errors to the model as tool results when the model can recover. Return errors to the caller when recovery is not possible.
- Observer hooks (after-tool, session-end) should not fail the primary operation. Gate hooks (before-tool, user-prompt) should be able to deny.

**Concurrency**
- Tools must explicitly opt in to concurrency via `ConcurrencySafe`.
- Shared state must use `sync.RWMutex` with snapshot iteration (copy under read lock, iterate outside lock).
- Context cancellation must be checked at operation boundaries, not polled in loops.

**Testing**
- Use deterministic fakes (`fakeModel`, `fakeStream`) instead of real providers.
- Golden tests protect public API contracts (event streams, prompt format).
- Benchmarks belong on the hot path: context policy, tool selection, session operations.
- Test edge cases: empty inputs, nil interfaces, concurrent access, error propagation.

**Provider neutrality**
- No provider-specific types in the core packages.
- Provider adapters live in `providers/` and map to/from `model.Client` and `model.Stream`.
- System prompt assembly, tool specs, and message history use SDK types, not provider types.

**Backward compatibility**
- New features must be opt-in. Existing code that sets no new fields must behave identically.
- New optional interfaces must have helper functions with graceful fallbacks (see `session.Create`, `session.Fork`).
- New `Options` fields must be handled in `withDefaults()` and `Options.Merge()`.

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
