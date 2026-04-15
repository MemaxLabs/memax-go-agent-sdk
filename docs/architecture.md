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
- `permission`: reusable permission checkers and policy composition.
- `providers/openai`: optional Responses API adapter for hosted model streaming and function calls.
- `session`: session persistence interface plus in-memory and append-only JSONL implementations.
- `contextwindow`: deterministic message-window policies used before model requests.
- `toolkit/filetools`: optional memory-backed file tools that demonstrate the tool contract without requiring real filesystem access.

Expected near-term packages:

- `contextwindow`: token budgeting, result truncation, compaction, and summary injection.
- `workspace`: optional virtual filesystem and checkpoint abstractions.
- `subagent`: bounded worker agents with parent/child event correlation.
- `otel`: OpenTelemetry spans and metrics around turns, model calls, tools, hooks, and compaction.

## Core Loop

The target loop is:

1. Create or resume a session.
2. Normalize user input into session messages.
3. Build system prompt, user context, active tool specs, and model request.
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

The optional `toolkit/filetools` package provides `list_files`, `read_file`, and `write_file` tools over a `FileSystem` interface plus a `MemoryFS` implementation. It is a DX reference, not a privileged core capability.

Before-tool hooks run after validation and before permission checks. They can deny execution with a model-visible reason. After-tool hooks observe completed results; observer failures are attached to result metadata and do not convert successful tool output into a model-visible failure.

## Permissions

Permission checks run before execution and receive the raw tool use plus the tool spec. The first-class policy modes should include:

- allow all
- read-only
- explicit allow/deny matchers
- ask host application
- hook-controlled allow/deny/update
- non-interactive auto-deny for tools requiring user input

The policy engine should eventually return structured reasons so model-visible errors, audit logs, and telemetry all agree.

Hooks complement permissions. Permissions answer "may this run?" while hooks let host applications add policy, audit, tracing, and future input rewriting without changing tool implementations.

## Sessions

Sessions persist the conversation trajectory: user messages, assistant messages, tool uses, tool results, compact boundaries, and metadata. They must not silently persist workspace state. Checkpoints and virtual filesystem snapshots should be separate services referenced from session metadata.

The SDK includes an in-memory store for tests and short-lived agents, plus an append-only JSONL store for durable transcripts. The JSONL store validates session IDs before path construction and reports corrupt transcript lines with line numbers.

## Context Window

Context-window policies transform session messages before each model request without mutating the durable session transcript. The initial `RecentMessages` policy keeps a bounded suffix and drops leading orphan tool-result messages after trimming. This is not summarizing compaction; it is a deterministic pressure valve and an interface for future compaction strategies.

Durable session stores should support:

- append-only JSONL transcript. Initial implementation exists.
- list and inspect sessions
- resume by ID
- fork from message ID
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
