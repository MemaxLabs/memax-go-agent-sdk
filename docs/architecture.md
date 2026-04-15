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
- `providers/openai`: optional Responses API adapter for hosted model streaming and function calls.
- `providers/anthropic`: optional Messages API adapter for hosted model streaming and tool-use blocks.
- `session`: session persistence interface plus in-memory and append-only JSONL implementations.
- `session/sqlitestore`: optional SQLite-backed session store for embedded durable agents.
- `skill`: local skill manifests, loaders, and relevance selection.
- `checkpoint`: checkpoint metadata, manager interface, and in-memory checkpoint manager.
- `contextwindow`: deterministic message-window policies used before model requests.
- `telemetry`: minimal SDK tracing and metrics interfaces used by core packages.
- `otel`: OpenTelemetry adapter for SDK tracing and metrics.
- `toolkit/filetools`: optional memory-backed file tools that demonstrate the tool contract without requiring real filesystem access.
- `toolkit/checkpointtools`: optional checkpoint tools over a checkpoint manager.
- `toolkit/toolsearch`: optional search tool for discovering deferred tool specs.
- `toolkit/subagents`: optional delegation tool for bounded child agents with parent/child session correlation.
- `toolkit/tasktools`: optional task-state tools for planning, progress tracking, and resumable work summaries.
- `toolkit/skilltools`: optional skill discovery tools over `skill.Source`.

Expected near-term packages:

- `workspace`: optional virtual filesystem and checkpoint abstractions.

## Core Loop

The target loop is:

1. Create or resume a session.
2. Normalize user input into session messages.
3. Select active tool specs, build system prompt, user context, active skills, and model request.
4. Stream model events to the caller.
5. Collect assistant text and tool-use blocks.
6. Validate each tool input.
7. Run hook and permission checks.
8. Execute tools with safe concurrency.
9. Append tool results to the session.
10. Continue until the model returns no tool calls, a stop condition fires, or a configured limit is reached.

The current scaffold implements the minimal version of that loop with JSON Schema validation before permission checks and execution. Future work should add streaming tool execution, hook phases, compaction, structured output enforcement, subagents, resumable durable sessions, and richer cancellation semantics.

## Tool Layer

Tools expose:

- model-facing metadata: name, description, JSON input schema, search hint
- execution policy: read-only, destructive, concurrency-safe, result limits, defer/always-load hints
- handler: application code that receives JSON input and returns a tool result

This keeps the core neutral. A `Read` tool can read the host filesystem, a memory-backed tree, a database record, a Git blob, or a browser sandbox. The orchestrator should not know which one is in use.

Tool input schemas are compiled when tools are registered. Model-emitted inputs are validated before permission checks and before handlers run, and validation failures are returned as tool-result errors so the model can recover in the next turn.

Tools can set `MaxResultBytes` to cap the content returned to the model. Truncated results preserve UTF-8 boundaries and carry metadata for original and returned byte counts.

Large registries can opt into `tool.SearchSelector` through `Options.ToolSelector`. The selector always keeps `AlwaysLoad` tools, defers unmatched `ShouldDefer` tools, ranks matches by transcript text against names, descriptions, and search hints, and sends only selected specs to the model. The optional `toolkit/toolsearch` package exposes a `search_tools` tool with `AlwaysLoad` set, so an agent can discover deferred tools and cause matching specs to be loaded on a later turn through normal transcript context.

The optional `toolkit/filetools` package provides `list_files`, `read_file`, and `write_file` tools over a `FileSystem` interface. It includes `MemoryFS` for deterministic tests and examples, `OSFS` for root-confined host directories, and `ReadOnlyFS` for standard `io/fs.FS` implementations such as embedded or map-backed filesystems. `OSFS` supports optional symlink containment, read-size limits, list-entry limits, and file mode configuration. It is a DX reference, not a privileged core capability.

Server embedders can wrap tools with `tool.WithTimeout` to bound individual
tool calls. The wrapper returns when the timeout expires even if the wrapped
tool ignores context cancellation, although the ignored work may continue in its
own goroutine until it returns. Tool implementations should still honor
`context.Context` for cleanup.

The optional `toolkit/tasktools` package provides `list_tasks`, `upsert_task`, and `delete_task` over a `Store` interface plus a concurrency-safe memory store. Task state is deliberately tool-owned state rather than implicit model memory; hosts can persist it in a database, scope it to a workspace, or discard it for short-lived runs.

The optional `toolkit/checkpointtools` package provides `create_checkpoint`, `list_checkpoints`, `restore_checkpoint`, and `delete_checkpoint` over the `checkpoint.Manager` interface. The SDK's in-memory manager stores checkpoint metadata and is useful for tests; production managers should connect these operations to a virtual workspace, filesystem snapshot service, database branch, or remote sandbox. Checkpoints are not stored inside session transcripts, but checkpoint records carry session and parent-session IDs for correlation.

Before-tool hooks run after validation and before permission checks. They can deny execution with a model-visible reason. After-tool hooks observe completed results; observer failures are attached to result metadata and do not convert successful tool output into a model-visible failure.

Session lifecycle hooks cover session start/end, user prompt submission, stop events, and context-window application. User prompt hooks may rewrite or deny the prompt before it is persisted. Session start/end, stop, and context-applied hooks are observational; their errors are surfaced as agent errors at stable lifecycle boundaries.

## Permissions

Permission checks run before execution and receive the raw tool use plus the tool spec. The permission package includes simple `AllowAll`, `ReadOnly`, and function-backed checkers plus a structured `Policy` for ordered rules. Rules can allow, deny, or ask a host application for approval. Matchers cover exact tool names, tool-name glob patterns, read-only/destructive tool metadata, top-level string fields in JSON tool input, and boolean composition with `All`, `AnyOf`, and `Not`.

If no structured rule matches, `Policy` denies by default unless an explicit default decision is configured. This keeps production policies conservative while preserving `AllowAll` as the SDK's default option for simple embedding.

Hooks complement permissions. Permissions answer "may this run?" while hooks let host applications add policy, audit, tracing, and future input rewriting without changing tool implementations.

## Prompt, Identity, and Skills

The prompt layer is a first-class part of the orchestration contract. Applications
can keep using raw `SystemPrompt` and `AppendSystemPrompt` fields for full
control. When an identity, skills, or a custom prompt builder are configured,
the SDK builds a deterministic system prompt from named parts and passes that
assembled prompt to the provider adapter.

`identity.Identity` captures stable agent behavior without requiring callers to
copy a long prompt: name, role, mission, tone, autonomy level, and constraints.
The default identity is deliberately tool-bounded: it tells the model to operate
only through host-provided tools and to prefer observable progress.

`prompt.Builder` receives the identity, selected model-visible tools, session
messages, configured skills, and host prompt text. The default builder emits:

- core Memax runtime instructions
- identity and constraints
- tool-use guidance based on active tool count
- relevant skills
- host system and append-system prompt text

The builder returns named prompt parts and a stable hash so embedders can log,
test, snapshot, and compare prompt changes. This keeps prompt evolution visible
instead of hiding intelligence changes inside provider adapters.

`skill.Source` is the source-neutral loading contract for instruction bundles.
Built-in helpers cover static slices, function-backed sources, merged sources,
cached sources, timeout-bounded sources, stale-while-revalidate prefetch
sources, HTTP JSON endpoints, host filesystem directories, and standard `fs.FS`
implementations. `skill.LoadDir` and `skill.LoadFS` load `SKILL.md` manifests
with simple frontmatter fields for name, description, when-to-use guidance,
tags, and always-on behavior. Callers can pass explicit skills or a dynamic
`Options.SkillSource`. `skill.Selector` keeps always-on skills and ranks
relevant skills against the current prompt and transcript. The optional
`toolkit/skilltools` package exposes skill discovery through the normal tool
layer. A `search_skills` tool can list relevant instructions from a
`skill.Source`, while the prompt builder can inject selected skills as named
prompt parts. This keeps skills inspectable and governable by the same registry,
permission, hook, and telemetry machinery as every other capability.

## Sessions

Sessions persist the conversation trajectory: user messages, assistant messages, tool uses, tool results, compact boundaries, and metadata. They must not silently persist workspace state. Checkpoints and virtual filesystem snapshots should be separate services referenced from session metadata.

The SDK includes an in-memory store for tests and short-lived agents, plus an append-only JSONL store for durable transcripts. The JSONL store validates session IDs before path construction and reports corrupt transcript lines with line numbers.

Stores can optionally implement `CreateWithOptions` to preserve parent session IDs, `Get` and `List` to inspect existing sessions, and `Fork` to create a child transcript from a source session through a message ID. The built-in stores assign IDs to appended messages that do not already have one, while preserving caller-provided IDs. Helper functions in the `session` package use optional store interfaces when present and return clear unsupported-operation errors otherwise. `Query` resumes an existing transcript when `Options.SessionID` is set; otherwise it creates a new session. Events, model requests, and tool runtime values all carry parent session IDs so subagent and forked runs can be correlated without requiring a specific storage backend.

## Subagents

Subagents are exposed through `toolkit/subagents`, not as a privileged orchestration shortcut. The toolkit registers a normal tool that receives an agent name and prompt, creates a child `Query` run with bounded turns and runtime duration, and returns the child result as a tool result. Because it is still a tool, hosts can gate delegation through the same validation, permission, hook, tracing, and result-size controls used for every other capability.

Child runs set `ParentSessionID` to the calling tool runtime session. When the child uses a store that supports parent-aware creation, the transcript records that relationship. The tool result metadata also includes the parent session ID, child session ID, and selected worker name for audit trails and UI linking.

## Context Window

Context-window policies transform session messages before each model request without mutating the durable session transcript. `RecentMessages` keeps a bounded suffix. `TokenBudget` keeps the newest messages under a caller-defined estimate budget. Both drop leading orphan tool-result messages after trimming.

`SummarizingBudget` adds model-backed compaction behind the same `Policy` interface. It checks whether the full transcript fits, reserves part of the configured budget for a synthetic summary, asks a pluggable `Summarizer` to compact the older prefix, and prepends that summary to the newest structurally valid suffix. `ModelSummarizer` is the default model-client adapter; applications can provide their own summarizer for deterministic summaries, hosted summarization, cached summaries, or domain-specific compression.

## Observability

Tracing is optional and uses a small SDK-owned `telemetry.Tracer` interface so the core can be tested without a real exporter. Metrics are optional and use a matching SDK-owned `telemetry.Meter` interface with counter and value-recording methods. The `otel` package adapts both interfaces to OpenTelemetry. Current spans cover full query runs, turns, context policy application, model streaming, and individual tool executions. Metrics cover query starts/completions/errors, turn starts and durations, model stream starts/errors/durations, context compaction events, tool executions and durations, and hook errors. Spans and metrics carry stable attributes for session IDs, turn numbers, message counts, tool IDs, tool names, tool input/result byte counts, and tool policy flags.

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
