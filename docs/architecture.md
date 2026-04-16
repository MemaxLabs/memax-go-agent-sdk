# Architecture Plan

## Product Goal

Build a Go SDK that lets applications run highly autonomous agents while keeping every operational capability pluggable. The SDK owns the loop, context, session, tool scheduling, permissions, hooks, and observability. Applications own concrete tools and decide what "read file", "write file", "run command", or "search" actually means.

This is deliberately different from a CLI-first agent. The SDK must be embeddable in servers, CI jobs, developer tools, web apps, and local experiments without assuming stdin/stdout, a terminal UI, or real system access.

## External Reference Points

Current agent SDKs commonly expose autonomous file reading, command execution, web search, hooks, subagents, permissions, sessions, MCP, checkpointing, cost tracking, OpenTelemetry, and tool search. The local TypeScript source reference shows the same deeper pattern in code:

- a query engine owns session lifecycle and turn state
- model responses stream back incrementally
- tool calls are detected during streaming
- concurrency-safe tools can start before the whole assistant message finishes
- unsafe or state-mutating tools run serially
- permission checks, input validation, hooks, and tool execution are separate phases
- tool results are normalized back into model messages
- context pressure triggers microcompaction, autocompaction, and retry paths
- sessions persist conversation history, not filesystem state

## Design Principles

- Provider-neutral core: model clients implement `model.Client`; hosted providers, local models, and tests can all adapt to the same stream protocol.
- Tool-first capability model: no built-in tool should bypass the tool interface. Real filesystem access is one possible tool implementation, not a core assumption.
- Deterministic orchestration: tool scheduling, permission decisions, retries, and session writes should be testable without a real model.
- Stream everything: callers receive events for model text, tool use, tool result, errors, and final results.
- Typed where it matters: Go interfaces define lifecycle contracts; JSON schemas define model-facing tool inputs.
- Conservative concurrency: only tools that explicitly opt into concurrency can run in parallel.
- Session state is not workspace state: sessions store messages and metadata; virtual filesystem or checkpoint state belongs to tools or workspace services.

## Package Shape

- `memaxagent`: public query/session convenience API.
- `model`: provider-neutral messages, tool-use blocks, streamed events, and model client interface.
- `tool`: registry, tool definition contract, decoder helpers, and executor.
- `hook`: lifecycle hooks for host policy, audit, and observability.
- `identity`: reusable agent identity profiles for role, mission, tone, autonomy, and constraints.
- `permission`: reusable permission checkers and policy composition.
- `prompt`: deterministic system prompt assembly from named parts, identity, tools, skills, and host guidance.
- `providers/openai`: optional Responses API adapter for hosted model streaming and function calls. Supports constructor options, default hosted endpoints, OpenAI-style `OPENAI_BASE_URL` API-version bases such as `/v1`, and explicit full-endpoint overrides.
- `providers/anthropic`: optional Messages API adapter for hosted model streaming and tool-use blocks. Supports constructor options, default hosted endpoints, Anthropic-style `ANTHROPIC_BASE_URL` service roots without `/v1`, and explicit full-endpoint overrides.

Provider base URL semantics intentionally follow each provider ecosystem rather
than a single SDK-wide rule: OpenAI `BaseURL` is the API-version base and
Anthropic `BaseURL` is the service root. Use the full `Endpoint` option when a
gateway needs a nonstandard route.
- `session`: session persistence interface plus in-memory and append-only JSONL implementations.
- `session/sqlitestore`: optional SQLite-backed session store for embedded durable agents.
- `skill`: local skill manifests, loaders, and relevance selection.
- `checkpoint`: checkpoint metadata, manager interface, and in-memory checkpoint manager.
- `workspace`: optional source-neutral workspace state, guarded patch, diff,
  checkpoint, and restore contracts for coding-agent toolkits.
- `contextwindow`: deterministic message-window policies used before model requests.
- `telemetry`: minimal SDK tracing and metrics interfaces used by core packages.
- `otel`: OpenTelemetry adapter for SDK tracing and metrics.
- `toolkit/filetools`: optional memory-backed file tools that demonstrate the tool contract without requiring real filesystem access.
- `toolkit/checkpointtools`: optional checkpoint tools over a checkpoint manager.
- `toolkit/toolsearch`: optional search tool for discovering deferred tool specs.
- `toolkit/subagents`: optional delegation tool for bounded child agents with parent/child session correlation.
- `toolkit/tasktools`: optional task-state tools for planning, progress tracking, and resumable work summaries.
- `toolkit/skilltools`: optional skill discovery tools over `skill.Source`.
- `toolkit/workspacetools`: optional workspace read/list/patch/diff/checkpoint/restore tools over `workspace.Store`.

Expected near-term packages:

- production workspace adapters for git-backed, database-backed, and remote
  sandbox-backed workspaces.

## Core Loop

The target loop is:

1. Create or resume a session.
2. Normalize user input into session messages.
3. Select active tool specs, build system prompt, user context, active skills, and model request.
4. Stream model events to the caller.
5. Collect assistant text and tool-use blocks. Provider adapters may emit
   tool-use lifecycle events before the complete call; only the complete
   `tool_use` event is executable.
6. Validate each tool input.
7. Run hook and permission checks.
8. Execute tools with safe concurrency. Read-only, concurrency-safe tools may
   start while the assistant stream is still producing trailing text, but they
   still pass through validation, hooks, permissions, result limiting, and
   telemetry.
9. Append tool results to the session.
10. Continue until the model returns no tool calls, a stop condition fires, or a configured limit is reached.

The current scaffold implements that loop with JSON Schema validation before
permission checks and execution, initial streaming execution for safe tools,
compaction, structured output enforcement, subagents, resumable durable
sessions, and context-aware cancellation. Future work should harden streaming
permission-denial evals and goroutine-leak detection for non-cooperative tool
handlers.

## Tool Layer

Tools expose:

- model-facing metadata: name, description, JSON input schema, search hint
- execution policy: read-only, destructive, concurrency-safe, result limits, defer/always-load hints
- handler: application code that receives JSON input and returns a tool result

This keeps the core neutral. A `Read` tool can read the host filesystem, a memory-backed tree, a database record, a Git blob, or a browser sandbox. The orchestrator should not know which one is in use.

Tool input schemas are compiled when tools are registered. Model-emitted inputs are validated before permission checks and before handlers run, and validation failures are returned as tool-result errors so the model can recover in the next turn.

Tools can set `MaxResultBytes` to cap the content returned to the model.
Truncated results preserve UTF-8 boundaries and carry metadata for original and
returned byte counts. Hosts can also configure `Options.ResultStore` with a
`resultstore.Store`. When a result exceeds the tool limit, the executor stores
the full content first, returns a bounded preview to the model, and attaches
handle metadata such as `stored_result_id`, `stored_result_uri`, and
`stored_result_bytes`. Store failures do not turn a successful tool call into an
error; the executor falls back to normal truncation and adds
`stored_result_error` metadata. This keeps oversized data host-owned while
allowing agents and UIs to recover the full payload through application policy.

Large registries can opt into `tool.SearchSelector` through `Options.ToolSelector`. The selector always keeps `AlwaysLoad` tools, defers unmatched `ShouldDefer` tools, ranks matches by transcript text against names, descriptions, and search hints, and sends only selected specs to the model. The optional `toolkit/toolsearch` package exposes a `search_tools` tool with `AlwaysLoad` set, so an agent can discover deferred tools and cause matching specs to be loaded on a later turn through normal transcript context.

The optional `toolkit/filetools` package provides `list_files`, `read_file`, and `write_file` tools over a `FileSystem` interface. It includes `MemoryFS` for deterministic tests and examples, `OSFS` for root-confined host directories, and `ReadOnlyFS` for standard `io/fs.FS` implementations such as embedded or map-backed filesystems. `OSFS` supports optional symlink containment, read-size limits, list-entry limits, and file mode configuration. It is a DX reference, not a privileged core capability.

Server embedders can wrap tools with `tool.WithTimeout` to bound individual
tool calls. The wrapper returns when the timeout expires even if the wrapped
tool ignores context cancellation, although the ignored work may continue in its
own goroutine until it returns. Tool implementations should still honor
`context.Context` for cleanup.

The optional `toolkit/tasktools` package provides `list_tasks`, `upsert_task`, and `delete_task` over a `Store` interface plus a concurrency-safe memory store. Task state is deliberately tool-owned state rather than implicit model memory; hosts can persist it in a database, scope it to a workspace, or discard it for short-lived runs.

The optional `workspace` package provides a stronger coding-agent workspace
contract than raw file reads and writes: file listing, guarded atomic patches,
standard unified diffs, dry-run patch previews, diffs against checkpoints,
checkpoint creation, and restore. The in-memory implementation is for tests and
examples. `workspace.OSStore` adapts a root-confined host directory with
symlink containment enabled by default, in-memory checkpoints, unified diffs,
dry-run previews, and restore. It is a reference adapter: its mutex protects
SDK calls through the store, but it does not stop external processes from
mutating the same directory, and restore is best-effort if the underlying
filesystem returns I/O errors mid-write. Production embedders can also
implement the same interface over git worktrees, databases, object snapshots,
or remote sandboxes.
The core agent loop does not import `workspace`; hosts expose workspace
capabilities only by registering tools such as `toolkit/workspacetools`.
`workspace.Store` is the convenience full-surface interface; individual
workspace tools accept smaller capability interfaces so hosts can expose
read/list, patch, diff, checkpoint, or restore independently. Unified diff and
dry-run support are optional extensions for patch-capable stores, so simple
embedders are not forced to implement the full mutation surface.
Workspace paths use forward-slash, workspace-relative syntax at the SDK
boundary. `workspace.Change` carries full before/after content for precise
host-side review; large production backends should cap content, return handles,
or provide summarized model-facing diffs when appropriate. `workspace.PatchSummary`
provides compact added/modified/deleted counts, byte deltas, and affected paths
for events, telemetry, dry-run previews, and host approval prompts. Workspace
tools mark results with provider-neutral metadata so the agent loop can emit
first-class workspace events without importing the workspace package into core.

The optional `toolkit/verifytools` package provides `workspace_verify` over a
small host-owned `Verifier` interface. This follows the same capability-boundary
rule as workspace tools: tests, typechecks, lint, policy checks, or remote CI
validators are explicit tools, not hidden SDK side effects or built-in shell
authority. Failed verification is returned as a model-visible tool error with
diagnostics, allowing the agent to repair and retry or restore a checkpoint
through normal transcript-visible tool calls.

The optional `toolkit/checkpointtools` package provides `create_checkpoint`, `list_checkpoints`, `restore_checkpoint`, and `delete_checkpoint` over the `checkpoint.Manager` interface. The SDK's in-memory manager stores checkpoint metadata and is useful for tests; production managers should connect these operations to a virtual workspace, filesystem snapshot service, database branch, or remote sandbox. Checkpoints are not stored inside session transcripts, but checkpoint records carry session and parent-session IDs for correlation.

Before-tool hooks run after validation and before permission checks. They can deny execution with a model-visible reason. After-tool hooks observe completed results; observer failures are attached to result metadata and do not convert successful tool output into a model-visible failure.

Session lifecycle hooks cover session start/end, user prompt submission, stop events, and context-window application. User prompt hooks may rewrite or deny the prompt before it is persisted. Session start/end, stop, and context-applied hooks are observational; their errors are surfaced as agent errors at stable lifecycle boundaries.

## Usage Accounting

`model.Usage` is the provider-neutral token accounting shape. Model streams can
emit `StreamUsage` events when a provider reports input, output, or total token
counts. `Query` forwards those as `EventUsage`, records token counters through
the configured meter, and attaches the aggregate usage to the final
`EventResult`. Usage is optional: providers that do not report token counts
continue to behave as before. The OpenAI Responses and Anthropic Messages
adapters map provider usage payloads into SDK usage events where available.
Usage metadata is merged across events with first-value-wins behavior for
duplicate keys. A parent run's usage covers the model calls made directly by
that run; subagent usage is available on the child run's events and tool-result
metadata can carry child session IDs for host-side rollups.

## Observability

`Query` exposes ordered lifecycle events for session start, context application,
compaction provenance, model requests, streaming assistant text, tool-use
starts/deltas/completions, tool results, usage, skill discovery/search/load,
memory candidates, terminal results, and errors. Events are the SDK's
application-facing observability contract; telemetry spans and metrics are the
aggregate monitoring layer. The detailed event ordering, action-specific event
payloads, metric names, and golden-test coverage are documented in
[observability.md](observability.md).

## Agent Evals

`agenteval` is an optional deterministic evaluation package for SDK embedders
and repository regression tests. It runs normal `memaxagent.Query` cases,
captures the complete event stream, final answer, usage, session IDs, tool uses,
and tool results, then applies caller-provided assertions. `ScriptedModel`
implements `model.Client` with predefined stream events so evals can cover
planning, tool recovery, structured-output repair, context retry, and session
resume behavior without a live provider. Provider scenarios use local HTTP
servers to exercise OpenAI and Anthropic adapters end to end without live API
credentials. This keeps autonomy quality executable while preserving the same
provider-neutral core loop used in production.
`agenteval/scenarios` contains reusable baseline cases for core behaviors such
as tool validation recovery, structured-output repair, memory search/save,
memory distillation candidates, session resume, context retry, subagent
delegation, planner-guided tool use, planner/task-state updates, provider usage
mapping, and provider tool-use round trips. Governance scenarios cover permission denial,
before-hook denial, oversized result storage, budget stops, and deferred tool
discovery recovery.

Eval cases can set `AllowError` when an agent error is the expected behavior.
The runner stores that error in `Result.RunErr` and still runs assertions,
while unexpected run errors continue to fail the case before assertions.

## Permissions

Permission checks run before execution and receive the raw tool use plus the tool spec. The permission package includes simple `AllowAll`, `ReadOnly`, and function-backed checkers plus a structured `Policy` for ordered rules. Rules can allow, deny, or ask a host application for approval. Matchers cover exact tool names, tool-name glob patterns, read-only/destructive tool metadata, top-level string fields in JSON tool input, and boolean composition with `All`, `AnyOf`, and `Not`.

If no structured rule matches, `Policy` denies by default unless an explicit default decision is configured. This keeps production policies conservative while preserving `AllowAll` as the SDK's default option for simple embedding.

Hooks complement permissions. Permissions answer "may this run?" while hooks let host applications add policy, audit, tracing, and future input rewriting without changing tool implementations.

## Prompt, Identity, Plans, Memories, and Skills

The prompt layer is a first-class part of the orchestration contract. Applications
can keep using raw `SystemPrompt` and `AppendSystemPrompt` fields for full
control. When an identity, planner, memories, skills, or a custom prompt builder are
configured, the SDK builds a deterministic system prompt from named parts and
passes that assembled prompt to the provider adapter.

`identity.Identity` captures stable agent behavior without requiring callers to
copy a long prompt: name, role, mission, tone, autonomy level, and constraints.
The default identity is deliberately tool-bounded: it tells the model to operate
only through host-provided tools and to prefer observable progress.

`prompt.Builder` receives the identity, selected model-visible tools, session
messages, configured plan, configured memories, configured skills, configured final-output
contract, and host prompt text. The default builder emits:

- core Memax runtime instructions
- identity and constraints
- tool-use guidance based on active tool count
- final-output JSON Schema contract
- host-provided plan context
- durable host memory context
- relevant skills
- host system and append-system prompt text

The builder returns named prompt parts and a stable hash so embedders can log,
test, snapshot, and compare prompt changes. This keeps prompt evolution visible
instead of hiding intelligence changes inside provider adapters.
`prompt.DefaultBuilder` also supports provider-family profiles for OpenAI and
Anthropic. Profiles add small provider-oriented guidance without importing
provider request types into core prompt assembly.

`output.Contract` is the provider-neutral structured final-answer contract.
Hosts can set `Options.Output` with a JSON Schema and a retry limit. The prompt
builder includes the schema as a named `memax.output_contract` part, and the
agent loop validates the final assistant text before emitting `EventResult`. If
validation fails and retries remain, the SDK appends a normal user message with
the validation error and asks the model to return only valid JSON. This keeps
structured output repair inside the same durable transcript, context policy,
tool-selection, hook, and telemetry flow as every other turn. Zero-value output
contracts are a no-op; `MaxRetries` zero uses the SDK default, and negative
values disable repair retries.

`planner.Policy` lets hosts provide explicit plan context without turning
planning into hidden core state. The policy receives the active session ID,
parent session ID, identity, current messages, and bounded recent user-query
text. It returns a `planner.Plan` containing a goal, overall state,
constraints, and ordered steps with status, evidence, tool hints, and
verification hints. The default builder injects non-empty plans as `memax.plan`
before memories and skills, so the model sees the host strategy while every
action still goes through normal tools, permissions, hooks, budgets, and
telemetry. Verification hints are advisory; hosts must expose the actual check
through a tool such as `workspace_verify`.

Planner policies are called on every model turn rather than cached for the
whole run. This is intentional: a host planner may reflect task progress,
external approvals, or other state that changes after tool results. Planners
that talk to remote services should be fast, cached, prefetched, or
timeout-bounded when per-turn freshness is not needed.

The core planner package also defines source-neutral `planner.Task` and
`planner.TaskSource` contracts. `planner.FromTaskSource` converts task state
into plan steps with deterministic priority ordering, inferred plan state,
global tool hints, and global verification hints. Custom task sources can add
per-task evidence, tool hints, and verification hints. The optional
`toolkit/tasktools` adapter exposes `tasktools.Planner(store)`, so the same task
store can be prompt-visible plan context and model-editable state through
`list_tasks` and `upsert_task`.

Task progress from verification remains explicit and host-owned. The
`toolkit/tasktools.NewVerificationProgressVerifier` helper wraps a
`verifytools.Verifier` and updates a task only when the verification request
metadata includes `task_id`. The verification result still flows through
`workspace_verify` as a normal tool result; task update failures are recorded in
result metadata instead of hiding verification diagnostics from the model. This
keeps the control loop observable: verify, update task state, reload planner on
the next turn.

`memory.Source` is the source-neutral loading contract for durable host context
such as project rules, user preferences, session notes, or organization policy.
Callers can pass explicit `Options.Memories` or a dynamic `Options.MemorySource`.
The source receives the active session ID, parent session ID, identity,
model-visible messages after context-window policy, and bounded recent
user-message query text. Dynamic memory sources are loaded once per `Query` run;
the cached memory set is then copied into each prompt build. The default prompt
builder injects selected memories as a named `memax.memories` prompt part.
`memory.Selector` keeps always-on memories and ranks relevant memories against
the current prompt and recent user-message text. Memory injection is prompt
context only; it does not grant filesystem, network, workspace, or OS
capabilities.

Memory mutation remains an explicit tool capability. Backends can optionally
implement `memory.Writer` and `memory.Deleter` in addition to `memory.Source`.
The optional `toolkit/memorytools` package exposes `search_memories`,
`save_memory`, and `delete_memory` only for configured capabilities, so hosts
can choose search-only, append-only, approval-gated, or full read/write/delete
memory behavior through the normal registry, permission, hook, and telemetry
layers. This is the intended integration point for cloud memory systems such as
Memax: implement the small memory interfaces, then register the tools and/or
configure `Options.MemorySource`.

Memory distillation is a separate post-result proposal path. Hosts can set
`Options.MemoryDistiller` to inspect the completed transcript, final answer,
identity, and current plan. The distiller returns `memory.Candidate` values,
which are emitted as `EventMemoryCandidates` before `EventResult`. The SDK does
not write those candidates automatically. Hosts that want a first-class write
path can set `Options.MemoryCandidateHandler`; the handler runs after
`EventMemoryCandidates` is emitted and before `EventResult`, so applications can
review, approve, discard, enqueue, or persist proposals through their own
`memory.Writer` policy. Handler errors emit
`EventMemoryCandidateHandlerError` and increment error telemetry, but they do
not block `EventResult`; memory persistence is a learning side effect, not part
of the model's completed answer. Hosts that need transactional all-or-nothing
learning should provide a custom `memory.CandidateHandler`. This keeps learning
observable and avoids silently polluting durable memory.

Distillers receive the durable message snapshot already available to the turn,
including the final assistant message. That avoids a second session-store read
on successful completion, but the snapshot can still be large for long
transcripts. Model-backed distillers should apply their own context budgeting or
summarization before sending transcript content to another model.

`skill.Source` is the source-neutral loading contract for instruction bundles.
Built-in helpers cover static slices, function-backed sources, merged sources,
policy-filtered sources,
cached sources, timeout-bounded sources, stale-while-revalidate prefetch
sources, HTTP JSON endpoints, host filesystem directories, and standard `fs.FS`
implementations. `skill.LoadDir` and `skill.LoadFS` load `SKILL.md` manifests
with simple frontmatter fields for name, description, when-to-use guidance,
tags, policy hints, and always-on behavior. Callers can pass explicit skills or
a dynamic `Options.SkillSource`. `skill.Selector` keeps always-on skills and ranks
relevant skills against the current prompt and transcript. By default, selected
skills are injected directly as named prompt parts for compatibility with small
trusted skill sets. With `Options.SkillDisclosure` set to
`skill.DisclosureProgressive`, the prompt contains only selected skill metadata
and the agent receives an SDK-provided read-only `load_skill` tool. Progressive
metadata discovery is bounded by default by both selected item count and prompt
bytes so large catalogs do not turn into prompt stuffing; hosts can override the
selector and byte budgets through a custom prompt builder. When metadata is
omitted, hosts can register `toolkit/skilltools` search against the same source
so the model can query metadata for the full catalog before calling
`load_skill`. Skill search is metadata-only by default; full skill bodies stay
behind `load_skill` unless a host explicitly enables full-content search.
Loading a skill returns the full instructions as a normal tool result, making
skill use visible in events and durable session history. Skills may advertise lightweight
supporting `skill.ResourceRef` metadata. If
`Options.SkillResourceSource` is configured, progressive mode also exposes
`read_skill_resource`, which loads host-owned resource content through the tool
layer instead of prompt-stuffing examples, checklists, templates, or schemas.
The optional `toolkit/skilltools` package separately exposes skill search
through the normal tool layer for hosts that want explicit catalog search. This
keeps skills inspectable and governable by the same registry, permission, hook,
and telemetry machinery as every other capability.

Skill visibility is evented. Progressive prompt metadata emits
`EventSkillDiscovery` with selected skill names, omitted count, and prompt byte
size. `toolkit/skilltools` results emit `EventSkillSearch` with query, match
count, and whether results were metadata-only. `load_skill` emits
`EventSkillLoaded`, and `read_skill_resource` emits `EventSkillResourceLoaded`.
The same operations increment `memax.skill.discovery`, `memax.skill.search`,
`memax.skill.loaded`, and `memax.skill.resource_loaded` counters.

If a provider rejects a model request because the context window is too large,
adapters can mark the error with `model.ErrContextWindowExceeded`. `Query` can
then apply `Options.ContextRetry` once and retry the model request without
mutating the durable session transcript. This is intended for emergency
compaction after an underestimated budget, not as a replacement for normal
context-window policy.

## Sessions

Sessions persist the conversation trajectory: user messages, assistant messages, tool uses, tool results, compact boundaries, and metadata. They must not silently persist workspace state. Checkpoints and virtual filesystem snapshots should be separate services referenced from session metadata.

The SDK includes an in-memory store for tests and short-lived agents, plus an append-only JSONL store for durable transcripts. The JSONL store validates session IDs before path construction and reports corrupt transcript lines with line numbers.

Stores can optionally implement `CreateWithOptions` to preserve parent session IDs, `Get` and `List` to inspect existing sessions, and `Fork` to create a child transcript from a source session through a message ID. The built-in stores assign IDs to appended messages that do not already have one, while preserving caller-provided IDs. Helper functions in the `session` package use optional store interfaces when present and return clear unsupported-operation errors otherwise. `Query` resumes an existing transcript when `Options.SessionID` is set; otherwise it creates a new session. Events, model requests, and tool runtime values all carry parent session IDs so subagent and forked runs can be correlated without requiring a specific storage backend.

## Subagents

Subagents are exposed through `toolkit/subagents`, not as a privileged orchestration shortcut. The toolkit registers a normal tool that receives an agent name and prompt, creates a child `Query` run with bounded turns and runtime duration, and returns the child result as a tool result. Because it is still a tool, hosts can gate delegation through the same validation, permission, hook, tracing, and result-size controls used for every other capability.

Child runs set `ParentSessionID` to the calling tool runtime session. When the child uses a store that supports parent-aware creation, the transcript records that relationship. The tool result metadata also includes the parent session ID, child session ID, and selected worker name for audit trails and UI linking.

Subagent plan scoping and progress return are explicit extension points.
`subagents.PlanSource` can prepare a child-only `planner.Plan` from the tool
input, such as a single task ID, before the child run starts.
`subagents.ResultHandler` can attach metadata or update host state after the
child run finishes. The `toolkit/tasktools` package provides
`SubagentPlanner(store)` and `NewSubagentProgressHandler(store)` adapters, so a
delegated task can appear as a scoped child plan and then return completion
evidence to the parent task store. Handler errors are surfaced as tool result
metadata, not hidden runtime side effects.

## Context Window

Context-window policies transform session messages before each model request without mutating the durable session transcript. `RecentMessages` keeps a bounded suffix. `TokenBudget` keeps the newest messages under a caller-defined estimate budget. Both drop leading orphan tool-result messages after trimming.

`SummarizingBudget` adds model-backed compaction behind the same `Policy` interface. It checks whether the full transcript fits, reserves part of the configured budget for a synthetic summary, asks a pluggable `Summarizer` to compact the older prefix, and prepends that summary to the newest structurally valid suffix. `ModelSummarizer` is the default model-client adapter; applications can provide their own summarizer for deterministic summaries, hosted summarization, cached summaries, or domain-specific compression.

Policies can optionally implement `contextwindow.PolicyWithResult` to return
structured provenance with the transformed messages. `SummarizingBudget` emits a
`CompactionRecord` with before/after message counts, summarized-message count,
replaced-summary count, a summary hash, and a short summary preview. The agent
surfaces that record as `EventContextCompacted` and records context compaction
metrics. Summary messages carry SDK-owned metadata that session stores may
persist for resume/debugging, while provider adapters intentionally omit that
metadata from wire requests.

`SummarizingBudget` marks its synthetic summary messages and replaces prior
active SDK summaries on subsequent compactions. This keeps the model-visible
history to one active summary instead of stacking summary messages across long
sessions.

`PreserveImportant` wraps any context policy and prepends explicit retention
groups that the wrapped policy would otherwise drop. Current retention signals
include loaded skill instructions, stored large-result handles, and tool errors.
Tool results are preserved with the assistant tool-use message that produced
them so provider transcripts remain structurally valid. This is opt-in because
preserved groups may exceed the wrapped policy's strict message or token
budget; hosts use it when preserving recovery state matters more than a hard
window target.

## Run Budgets

Run budgets are separate from context-window budgets. Context-window policies
decide how much transcript to send to the next model request. `Options.Budget`
decides whether the current run may continue at all.

The `budget` package defines a small `Governor` interface and a zero-value
disabled `Policy` implementation. Positive limits can cap turns, model calls,
tool calls, input tokens, output tokens, total tokens, and elapsed duration.
The agent loop checks the governor at stable lifecycle boundaries: turn start,
before model calls, after model usage is observed, before context-retry model
calls, and before executing a tool batch. A denial emits `EventError`, finishes
the run with `hook.StopReasonBudget`, and records a `memax.budget.exceeded`
metric with the current resource snapshot.

`Policy.MaxTurns` is intentionally separate from `Options.MaxTurns`.
`Options.MaxTurns` is the hard loop bound. `Policy.MaxTurns` is a
budget-governed limit that uses the same turn count but reports
`hook.StopReasonBudget` when exceeded. If both are set, the lower effective
limit stops the run.

Hosts can provide custom governors for tenant quotas, hosted billing systems,
or dynamic policies. The core package depends only on the provider-neutral
`budget.Snapshot` and `model.Usage` types.

## Observability

Tracing is optional and uses a small SDK-owned `telemetry.Tracer` interface so the core can be tested without a real exporter. Metrics are optional and use a matching SDK-owned `telemetry.Meter` interface with counter and value-recording methods. The `otel` package adapts both interfaces to OpenTelemetry. Current spans cover full query runs, turns, context policy application, model streaming, and individual tool executions. Metrics cover query starts/completions/errors, turn starts and durations, model stream starts/errors/durations, context compaction events, skill discovery/search/load operations, tool executions and durations, and hook errors. Spans and metrics carry stable attributes for session IDs, turn numbers, message counts, tool IDs, tool names, skill names, tool input/result byte counts, and tool policy flags.

Durable session stores should support:

- append-only JSONL transcript. Initial implementation exists.
- list and inspect sessions. Initial implementations exist.
- resume by ID. Initial `Options.SessionID` support exists.
- fork from message ID. Initial implementations exist.
- compact boundary records
- parent tool-use ID for subagent messages

## Autonomy Roadmap

High-end agent autonomy is mostly orchestration quality, not any single tool. The highest-leverage capabilities are:

- prompt and context assembly that teaches tool use clearly
- reliable tool result normalization
- safe parallel execution for read/search calls
- serial execution for mutating calls
- permission loops that the model can recover from
- compaction before context failure and retry after context failure
- subagents for bounded parallel investigation
- todo/task state that the model can update
- observability that explains why the agent made progress or got stuck
