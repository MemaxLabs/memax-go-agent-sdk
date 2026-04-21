# Architecture Plan

## Product Goal

Build a Go runtime that lets applications run highly autonomous agents while
keeping every operational capability pluggable. The SDK owns the loop, context,
session, tool scheduling, permissions, hooks, and observability. Applications
own concrete tools and decide what "read file", "write file", "run command",
"search", "read email", or "check calendar" actually means.

This is deliberately different from a CLI-first agent. The SDK must be embeddable in servers, CI jobs, developer tools, web apps, and local experiments without assuming stdin/stdout, a terminal UI, or real system access.

The target product is broader than a coding-agent SDK. Coding is the current
proving ground, but the same foundation is being shaped so it can later
support:

- coding agents with workspaces, patches, verification, and execution
- personal intelligence agents with docs, messaging, knowledge, and schedules
- managed cloud agents with tenancy, approvals, quotas, and remote execution

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

## Runtime Layers

The architecture separates three layers cleanly:

### 1. Runtime Kernel

The permanent core:

- provider-neutral model protocol
- turn loop and retries
- tool protocol and scheduling
- sessions and transcript durability
- context policies and compaction
- permissions, approvals, and hooks
- planner, tasks, and subagents
- memory injection and lifecycle proposals
- observability, budgets, and eval contracts

This layer should remain domain-neutral. That includes multi-tenant server
embedding: the kernel can carry an opaque tenant scope and call a host-owned
validator, but it should not hard-code any account, billing, or tenancy model.

### 2. Capability Adapters

Optional packages that let hosts expose real powers to the model:

- coding: workspace, patch, verify, command, managed sessions, sandbox backends
- personal intelligence: browser, docs, email, calendar, notes, messaging,
  knowledge
- managed cloud: remote execution, approval surfaces, tenancy-aware policy,
  durable jobs, quotas, and audit integrations

This layer defines what the agent can do, while keeping those capabilities
explicit and tool-mediated.

### 3. Opinionated Stacks

Reusable, batteries-included assemblies built on the same kernel and adapter
contracts. The near-term target stacks are:

- `stack/coding`
- `stack/personal`
- `stack/cloudmanaged`

Each stack should package prompt defaults, tool bundles, policies, evals, and
embedding examples without introducing special-case logic into the kernel.
Initial `stack/coding` and `stack/personal` packages now exist. `stack/coding`
assembles workspace, command, verification, approval, task/planner, and policy
defaults into a reusable coding runtime profile with named workflow presets
(`safe_local`, `ci_repair`, `interactive_dev`). `stack/personal` assembles
durable memory, note/document tools, message-thread tools, task/planner,
approval, skill-disclosure, and scoped delegation defaults into
personal-intelligence presets
(`personal_assistant`, `research_partner`). `stack/coding` remains the first
stack expected to reach competitive maturity; the other stacks should reuse the
same kernel and adapter seams rather than fork the architecture.
Initial `stack/cloudmanaged` now exists as a deliberately narrow managed-worker
assembly: it layers tenant-scope enforcement and per-session quota validation
onto the kernel's explicit tenant seam without hard-coding billing, remote
execution, or policy-service semantics into the runtime. It now also provides
an audit subscriber plus reference memory and JSONL sinks so managed hosts can
persist the ordered event stream without parsing transcript text or wrapping
`Query` by hand. The stack now accepts tenant scope explicitly per run so one
assembled managed-worker stack can serve many tenants without rebuilding shared
registries, hooks, or validators. Managed audit observation now follows
delegated child-agent runs automatically through the same runtime seam, so
bounded subagent work does not disappear from a hosted audit trail.
Managed quotas now sit behind a host-owned `QuotaStore` seam: the reference
`MemoryQuotaStore` keeps the zero-config path for local or single-process
deployments, while distributed hosts can attach a shared backend without
rewriting tenant validation or session-end cleanup logic. The first shared
backend now exists as `stack/cloudmanaged/redistore`, which keeps the atomic
reserve contract on the Redis server side and applies TTL-backed cleanup as a
crash-safety net. A durable SQL path now also exists through
`stack/cloudmanaged/sqlitestore`, which keeps the same quota contract over
SQLite with `BEGIN IMMEDIATE` transactions plus an explicit prune helper for
stale sessions, and now also implements the durable `RunStore` seam so hosts
can persist managed-run lifecycle in the same database. Managed audit sinks can
now also be wrapped through an async
cloudmanaged sink adapter, so hosts can choose bounded buffered delivery
without changing the event-observer or sink interfaces underneath. Durable
managed background runs now also sit behind a host-owned `RunStore` seam in
`stack/cloudmanaged`: the reference `MemoryRunStore` keeps the initial
single-process path, while `StartRun`, `GetRun`, and `CancelRun` give hosts an
explicit queued/running/succeeded/failed/canceled lifecycle without coupling
job state to transcript parsing or ad hoc goroutine wrappers. Those lifecycle
transitions now emit explicit `run_state_changed` observer events, so the same
audit seam can cover tenant denials, delegated child work, and managed-run
state changes without a second notification channel. The same seam now also
supports explicit queued worker execution through `EnqueueRun`, `ExecuteRun`,
`FailStaleRuns`, and `WatchStaleRuns`: foundation remote execution stays
host-owned, worker death maps to explicit failed terminal state via heartbeat
timeout, mid-run tenant revocation is eval-backed on the queued-worker path,
and automatic resume is intentionally deferred until the runtime has real
checkpointed work to resume. Cross-process workers follow the same
tenant-validator-config model as in-process workers: the SDK intentionally does
not introduce signed worker-delegation tokens or key-management machinery, so
hosts coordinate remote claiming separately while worker-side execution still
flows through the existing tenant seam. An initial host-owned helper now also
exists through `stack/cloudmanaged/remote`, which keeps claim discovery and
reference HTTP polling outside the core stack while routing actual execution
through `ExecuteRun`; it also exposes a non-mutating readiness probe so claim
servers can report whether queued-run discovery is reachable without claiming
work. Cloudmanaged observability now has the same event-plus-
metrics split as the rest of the SDK: run lifecycle transitions and tenant
denials remain structured events for audit/debug ordering, while
`Config.Base.Meter` records aggregate managed-runtime metrics for run states
and durations, quota-store allow-on-error fallbacks, worker claims,
heartbeats, heartbeat errors, and stale-worker failures.
Each preset now has deterministic end-to-end eval coverage for its normal
workflow and its defining recovery or delegation path, so preset behavior is
part of the public contract rather than informal guidance. The navigable preset
contracts live in [coding-stack-presets.md](coding-stack-presets.md) and
[personal-stack-presets.md](personal-stack-presets.md), including default
policy posture, examples, and the specific eval scenario names that enforce the
surface. `stack/cloudmanaged` is earlier in maturity than the coding stack but
now has a foundation-complete managed-runtime surface: quota-denial coverage
locks the tenant-admission contract, audit subscribers persist managed event
streams, remote workers have eval-backed success/revocation/stale-failure
paths, and provider-neutral metrics expose the operational signals needed to
run it. Richer managed-host presets, additional durable backends, and deeper
remote-execution backends remain follow-on work.

## Package Shape

### Runtime Kernel Packages

- `memaxagent`: public query/session convenience API.
- `model`: provider-neutral messages, tool-use blocks, streamed events, and model client interface.
- `tool`: registry, tool definition contract, decoder helpers, and executor.
- `hook`: lifecycle hooks for host policy, audit, and observability.
- `identity`: reusable agent identity profiles for role, mission, tone, autonomy, and constraints.
- `tenant`: host-owned tenant scope and admission contracts for multi-tenant
  embedding, quota routing, and cloud-managed policy.
- `permission`: reusable permission checkers and policy composition.
- `prompt`: deterministic system prompt assembly from named parts, identity, tools, skills, and host guidance.
- `session`: session persistence interface plus in-memory and append-only JSONL implementations.
- `session/sqlitestore`: optional SQLite-backed session store for embedded durable agents.
- `skill`: local skill manifests, loaders, and relevance selection.
- `memory`: prompt-visible host memory loading, mutation contracts, and
  distillation proposals.
- `notes`: host-owned note and lightweight document search/read/write
  contracts for personal-intelligence adapters.
- `messaging`: host-owned message-thread search/read/send contracts for
  personal-intelligence adapters.
- `scheduling`: host-owned calendar and scheduling search/read/create/
  reschedule/cancel contracts for personal-intelligence adapters.
- `scheduling/sqlitestore`: optional SQLite-backed scheduling store for
  embedded durable agents and local calendar-style backends.
- `scheduling/caldavclient`: focused CalDAV protocol client with XML REPORT
  handling, VEVENT parsing, and ETag-aware PUT/DELETE support.
- `scheduling/caldavstore`: optional CalDAV-backed scheduling store that maps
  remote calendar objects onto the `scheduling` contracts with metadata-first
  search and optimistic concurrency via ETags.
- `scheduling/googlecalendarclient`: focused Google Calendar REST client with
  events list/get/insert/update/delete support, request timeout helpers, and
  conditional modification via `If-Match`.
- `scheduling/googlecalendarstore`: optional Google Calendar-backed scheduling
  store that maps JSON event resources onto the `scheduling` contracts with
  metadata-first search and bounded retry on update conflicts.
- `planner`: host-owned plan and task-source contracts for strategy injection.
- `budget`: provider-neutral run-budget contracts and policies.
- `output`: provider-neutral structured final-output contracts.
- `resultstore`: host-owned storage for oversized tool results.
- `checkpoint`: checkpoint metadata, manager interface, and in-memory checkpoint manager.
- `contextwindow`: deterministic message-window policies used before model requests.
- `telemetry`: minimal SDK tracing and metrics interfaces used by core packages.

### Capability Adapter Packages

- `providers/openai`: optional Responses API adapter for hosted model streaming and function calls. Supports constructor options, default hosted endpoints, OpenAI-style `OPENAI_BASE_URL` API-version bases such as `/v1`, and explicit full-endpoint overrides.
- `providers/anthropic`: optional Messages API adapter for hosted model streaming and tool-use blocks. Supports constructor options, default hosted endpoints, Anthropic-style `ANTHROPIC_BASE_URL` service roots without `/v1`, and explicit full-endpoint overrides.

Provider base URL semantics intentionally follow each provider ecosystem rather
than a single SDK-wide rule: OpenAI `BaseURL` is the API-version base and
Anthropic `BaseURL` is the service root. Use the full `Endpoint` option when a
gateway needs a nonstandard route.
- `workspace`: optional source-neutral workspace state, guarded patch, diff,
  checkpoint, and restore contracts for coding-agent toolkits.
- `otel`: OpenTelemetry adapter for SDK tracing and metrics.
- `toolkit/filetools`: optional memory-backed file tools that demonstrate the tool contract without requiring real filesystem access.
- `toolkit/checkpointtools`: optional checkpoint tools over a checkpoint manager.
- `toolkit/toolsearch`: optional search tool for discovering deferred tool specs.
- `toolkit/subagents`: optional delegation tool for bounded child agents with parent/child session correlation.
- `toolkit/tasktools`: optional task-state tools for planning, progress tracking, and resumable work summaries.
- `toolkit/skilltools`: optional skill discovery tools over `skill.Source`.
- `toolkit/notetools`: optional metadata-first note/document search, full-note
  read, and note mutation tools over `notes` contracts.
- `toolkit/messagetools`: optional metadata-first message-thread search,
  full-thread read, and outbound send tools over `messaging` contracts.
- `toolkit/scheduletools`: optional metadata-first schedule-event search,
  full-event read, and calendar mutation tools over `scheduling` contracts.
- `toolkit/workspacetools`: optional workspace read/list/patch/diff/checkpoint/restore tools over `workspace.Store`.
- `toolkit/commandtools`: optional command execution tools over a host-owned
  runner. The reference OS runner launches argv directly without an implicit
  shell and applies cwd containment, timeouts, and output caps.
- `toolkit/approvaltools`: optional host approval request tool over an
  application-owned approver.
- `toolkit/agentpolicy`: optional hook-based policy presets for common agent
  safety workflows.

### Planned Stack Packages

- `stack/coding`: batteries-included coding workflow assembly.
- `stack/personal`: batteries-included personal intelligence workflow assembly.
- `stack/cloudmanaged`: multi-tenant managed-agent assembly.

Expected near-term packages and expansions:

- production workspace adapters for git-backed, database-backed, and remote
  sandbox-backed workspaces.
- deeper stack packages that assemble the neutral runtime into coding,
  personal intelligence, and managed cloud defaults once the underlying
  primitives are stable enough. Coding is first; broader stacks follow once the
  shared primitives are genuinely ready.

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

The optional `toolkit/tasktools` package provides `list_tasks`, `upsert_task`, and `delete_task` over a `Store` interface plus a concurrency-safe memory store and `toolkit/tasktools/sqlitestore` for durable SQLite-backed task ledgers. Task state is deliberately tool-owned state rather than implicit model memory; hosts can persist it in a database, scope it to a workspace, or discard it for short-lived runs. Task mutation results carry both human-readable task fields and the standard `task_id`, `task_status`, and `task_evidence` metadata keys used by verification and subagent progress adapters, so host observers can track durable task changes without parsing result text.

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
The optional `sandbox` package keeps those more isolated backends coherent
across subsystems: hosts can adapt related sandbox-backed workspace, command,
managed-session, and cleanup backends without teaching the core loop about
transport, container, or VM details.
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
through normal transcript-visible tool calls. Verification requests include the
active session ID so opt-in policies can correlate failures with prior
checkpoint, task, or approval state without coupling verification to the core
agent loop.

The optional `toolkit/commandtools` package provides `run_command` over a
host-owned `Runner`. The tool accepts an argv vector rather than a shell string,
so the model, approval layer, and audit logs all refer to the exact executable
and arguments. `commandtools.OSRunner` is a reference local adapter with
root-confined cwd resolution, direct `os/exec` launch, timeout enforcement, and
bounded stdout/stderr capture; it is not an OS sandbox, does not filter
executables or arguments, and should be installed only by hosts that explicitly
want local process execution. Hosts that need command allowlists, containers,
network policy, or OS sandboxing should wrap or replace the runner. OSRunner
does not inherit the host environment by default because environment variables
often contain credentials. `ScriptedRunner` supports deterministic evals.
Command results carry exit code, timeout, duration, retained output byte counts,
truncation status, and argv metadata, which drive `EventCommandFinished` and
`memax.command.*` metrics.

The same package also supports managed command sessions for longer-lived work
such as dev servers, watchers, or background checks. `start_command`,
`write_command_input`, `resize_command_terminal`, `read_command_output`,
`wait_command_output`, `stop_command`, and `list_commands` sit on top of
host-owned `Starter`, `Writer`, `Resizer`, `Reader`, `Waiter`, `Stopper`, and
`Lister` interfaces. Session tools remain argv-only,
transcript-visible, and metadata-driven. They do not introduce hidden shell
state into the core loop. `commandtools.SessionCleanupOptions`
adapts a `Cleaner` into a `SessionEnded` hook so host-managed processes can be
cleaned up when the parent agent session finishes. `commandtools.OSSessionManager`
is the reference local adapter for real managed processes: rooted cwd
resolution, bounded buffered output with drop accounting, interactive stdin
writes with optional short post-write waits, optional PTY-backed terminal
sessions for shells and REPLs, explicit terminal geometry at start and resize
time, natural-exit and stop tracking, Unix process-group termination for
ordinary descendant cleanup, and session-scoped cleanup over local `os/exec`
processes.
Unix PTY sessions use native pseudo terminals; Windows TTY sessions use ConPTY
when the operating system exposes the required console APIs.
`OSSessionManager` is not a sandbox and does not constrain filesystem, network,
or process access beyond cwd resolution; hosts that need stronger isolation
must wrap or replace it. Graceful stop is best-effort and platform dependent:
Unix hosts usually get an interrupt-before-kill sequence against the session's
process group, while job-control children that move into different process
groups can still require host sandbox cleanup and Windows may fall back to
forced termination of only the top-level process immediately.
`ScriptedSessionManager` continues to provide deterministic managed sessions for
evals. The `commandtools/sessiontest` package defines the shared conformance
contract that OS, scripted, sandbox, and future remote session adapters can run
without depending on one another's implementation details.
For hosts that need command-session output to outlive a live manager,
`CommandTranscriptStore` persists command-session snapshots and ordered output
chunks separately from `session.Store`. This mirrors the SDK boundary between
conversation persistence and tool-owned state: a resumed agent can inspect
durable command output through explicit tools or host UI, but the kernel still
does not hide background shell state inside the conversation store.
`toolkit/commandtools/sqlitestore` is the embedded durable adapter for that
seam, following the same SQLite transaction discipline used elsewhere in the
SDK so transcript inspection survives manager restarts without changing the
live command-session tool contract.
`toolkit/commandtools.OSSessionManager` can now attach one of these stores as
an optional durable backend, streaming snapshots and ordered output chunks into
the store while a process runs and falling back to persisted transcripts for
read/list inspection after manager restart. Those persisted records reflect the
last durable session snapshot, not proof that a process is still live after a
manager restart; hosts that need liveness must keep a live manager or apply
their own stale-session sweep. `OSSessionManager.SweepPersistedRunningCommands`
is the built-in reconciliation helper for that host-owned sweep and marks
unclaimed persisted `running` records as `orphaned`. Live-only operations
(`write_command_input`, `resize_command_terminal`, and `stop_command`) return
`ErrCommandSessionNotRunning` when only transcript state remains.
Swept `orphaned` records keep prior durable output but use sweep time as
`finished_at`; they do not infer a process `exit_code`.
The `sandbox` package complements these local adapters by adapting host-owned
sandbox-backed command/session backends into the same commandtool interfaces
plus hook cleanup, making remote or container-backed execution an adapter
concern rather than a special case in orchestration.

The optional `toolkit/checkpointtools` package provides `create_checkpoint`, `list_checkpoints`, `restore_checkpoint`, and `delete_checkpoint` over the `checkpoint.Manager` interface. The SDK's in-memory manager stores checkpoint metadata and is useful for tests; production managers should connect these operations to a virtual workspace, filesystem snapshot service, database branch, or remote sandbox. Checkpoints are not stored inside session transcripts, but checkpoint records carry session and parent-session IDs for correlation.

Before-tool hooks run after validation and before permission checks. They can deny execution with a model-visible reason. After-tool hooks observe completed results; observer failures are attached to result metadata and do not convert successful tool output into a model-visible failure. Before-final hooks run when the model produces a no-tool assistant answer and before final output validation or result emission; a denial appends a normal user repair prompt so finalization gates remain transcript-visible and recoverable. `Options.MaxFinalDenials` bounds these repair attempts; zero uses the SDK default and negative disables before-final retries.

For multi-tenant hosts, `Options.Tenant` carries the run's opaque tenant scope
through model requests, tool runtime, and lifecycle hooks. `Options.TenantValidator`
lets the host enforce admission at three explicit boundaries: session start or
resume, outbound model request, and tool use after schema validation but before
hooks, permissions, or execution. This keeps tenancy policy transcript-visible
and host-controlled without baking any specific tenancy model into the kernel.

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

`notes` provides the analogous source-neutral seam for host-owned notes and
lightweight personal documents that should stay tool-mediated rather than
prompt-injected by default. `notes.Searcher` returns metadata-first results
suited for discovery, `notes.Reader` loads full content only when the model
explicitly asks for it, and `notes.Writer` / `notes.Deleter` remain optional
mutation capabilities. The optional `toolkit/notetools` package exposes
`search_notes`, `read_note`, `save_note`, and `delete_note`, keeping larger
personal knowledge artifacts progressive and transcript-visible instead of
turning note search into hidden prompt stuffing.

`messaging` provides the analogous seam for host-owned message threads and
conversation backends. `messaging.Searcher` returns metadata-first thread
results suited for discovery, `messaging.Reader` loads full thread content only
when the model explicitly asks for it, and `messaging.Sender` remains an
optional outbound mutation capability. The optional `toolkit/messagetools`
package exposes `search_message_threads`, `read_message_thread`, and
`send_message`, so recall and reply flows stay progressive, transcript-visible,
and approval-gated through normal tool and hook policy instead of hidden prompt
stuffing or direct transport access. Initial `messaging/jmapclient` and
`messaging/jmapstore` packages now provide a metadata-first remote inbox
adapter over JMAP mail, using thread-collapsed metadata search and explicit
full-thread reads instead of stuffing email bodies into prompt context.

`scheduling` provides the analogous seam for host-owned calendar and scheduling
backends. `scheduling.Searcher` returns metadata-first event results suited for
discovery, `scheduling.Reader` loads full event detail only when the model
explicitly asks for it, and `scheduling.Creator` / `Rescheduler` /
`Canceller` remain optional mutation capabilities. The optional
`toolkit/scheduletools` package exposes `search_schedule_events`,
`read_schedule_event`, `create_schedule_event`, `reschedule_schedule_event`,
and `cancel_schedule_event`, so calendar discovery and change flows remain
progressive, transcript-visible, and approval-gated through normal tool and
hook policy rather than hidden prompt stuffing or direct transport access. The
adapter contract is metadata-first on both sides: adapters should return only
metadata fields from `Searcher`, and the tool layer formats search results
without full descriptions as a defensive backstop.

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

## Policy Presets

Policy presets live in `toolkit/agentpolicy` and install through the existing
hook runner. They do not mutate the core agent loop or bypass tool permissions.
`RequireCheckpointBeforePatch` is the first preset: it denies
`workspace_apply_patch` until a successful `workspace_checkpoint` has been
observed in the same session, while allowing dry-run patch previews. The denial
is returned as a normal tool error so the model can recover by creating a
checkpoint and retrying the patch.

`RecommendRollbackOnFailedVerification` records the latest successful
`workspace_checkpoint` per session through after-tool hooks and wraps a
`verifytools.Verifier`. When verification fails, the wrapped verifier adds
rollback metadata and a model-visible instruction to restore the checkpoint
before continuing. It intentionally does not restore by itself: rollback is a
normal `workspace_restore` tool call, so permissions, hooks, telemetry, and
session transcripts remain explicit.

`RequireVerificationBeforeFinal` uses after-tool hooks to mark a session dirty
after successful mutating workspace patches or restores, clears the dirty state
after a successful `workspace_verify`, and uses a before-final hook to deny
premature final answers. The denial is appended as a user repair prompt rather
than returned as a terminal error, so the model can run verification and then
finalize through the normal loop. If before-final denials exceed
`Options.MaxFinalDenials`, the run emits `EventError` and stops with
`hook.StopReasonPolicy`.

`RequireApprovalBeforeTools` uses before-tool hooks to deny configured tool
names until a successful `request_approval` result for that tool name is
observed in the same session. This mirrors production approval systems while
staying SDK-neutral: the approver is host-owned, and both granted and denied
decisions are normal transcript-visible tool results.

Approvals are scoped to tool names because the SDK does not assume a universal
capability taxonomy across host-defined tools. Hosts that rename tools or expose
custom wrappers must configure those names explicitly. The default grant is
session-scoped and reusable for the approved action. For higher-risk operations,
`WithSingleUseApprovals` consumes a grant on the next matching attempt, and
`WithInputBoundApprovals` requires the approval result to carry the canonical
hash of the proposed `tool_input`, allowing only an exact later input match.
Approval requests, grant/denial decisions, and consumed grants emit typed
approval events and `memax.approval.*` counters so hosts can build review UI and
audit logs without parsing generic tool-result text.
The approval request schema includes an optional structured summary with title,
description, risk, paths, change counts, and byte delta. Toolkit helpers such as
`workspacetools.ApprovalSummaryFromPatchInput` and
`commandtools.ApprovalSummaryFromRunInput` and
`commandtools.ApprovalSummaryFromStartInput` can derive summaries from
tool-specific inputs while keeping the core approval contract provider-,
workspace-, and command-runner-neutral.

Command governance presets reuse the same hook and approval primitives without
making `run_command` special in the core loop. `AllowCommands` and
`DenyCommands` match argv prefixes for `run_command` and return recoverable
tool errors on policy denial. Matching is by argv element, not by shell string,
because command execution is argv-only. `RequireApprovalBeforeCommands` gates
selected argv prefixes behind `request_approval`. By default, approval is
session-scoped for later commands matching the configured argv prefixes. With
input-bound and single-use command approval options, the approval result must
carry the canonical hash of the exact later `run_command` input and the grant
is consumed on first use.

`RequireVerificationAfterCommands` marks a session dirty after successful
matching commands and denies finalization until a successful verification result
is observed. This is intended for commands such as generators, formatters, or
dependency installers whose effects should be checked before the agent claims
completion. The policy does not run verification itself; the model must call a
host-owned verification tool, preserving transcript visibility, permissions,
hooks, telemetry, and host control.

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

## Telemetry and Durable Session Expectations

Tracing is optional and uses a small SDK-owned `telemetry.Tracer` interface so the core can be tested without a real exporter. Metrics are optional and use a matching SDK-owned `telemetry.Meter` interface with counter and value-recording methods. The `otel` package adapts both interfaces to OpenTelemetry. Current spans cover full query runs, turns, context policy application, model streaming, and individual tool executions. Metrics cover query starts/completions/errors, turn starts and durations, model stream starts/errors/durations, context compaction events, skill discovery/search/load operations, approval request/decision/consumption operations, command completion/duration, tool executions and durations, and hook errors. Spans and metrics carry stable attributes for session IDs, turn numbers, message counts, tool IDs, tool names, skill names, approval actions, tool input/result byte counts, and tool policy flags.

Durable session stores should support:

- append-only JSONL transcript. Initial implementation exists.
- list and inspect sessions. Initial implementations exist.
- resume by ID. Initial `Options.SessionID` support exists.
- fork from message ID. Initial implementations exist.
- compact boundary records
- parent tool-use ID for subagent messages
