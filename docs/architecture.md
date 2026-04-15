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
- `permission`: reusable permission checkers and policy composition.
- `session`: session persistence interface plus in-memory implementation.

Expected near-term packages:

- `hook`: lifecycle callbacks for prompt submit, pre-tool-use, post-tool-use, compaction, stop, and session end.
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
7. Run permission and hook checks.
8. Execute tools with safe concurrency.
9. Append tool results to the session.
10. Continue until the model returns no tool calls, a stop condition fires, or a configured limit is reached.

The current scaffold implements the minimal version of that loop. Future work should add streaming tool execution, hook phases, compaction, structured output enforcement, subagents, resumable durable sessions, and richer cancellation semantics.

## Tool Layer

Tools expose:

- model-facing metadata: name, description, JSON input schema, search hint
- execution policy: read-only, destructive, concurrency-safe, defer/always-load hints
- handler: application code that receives JSON input and returns a tool result

This keeps the core neutral. A `Read` tool can read the host filesystem, a memory-backed tree, a database record, a Git blob, or a browser sandbox. The orchestrator should not know which one is in use.

## Permissions

Permission checks run before execution and receive the raw tool use plus the tool spec. The first-class policy modes should include:

- allow all
- read-only
- explicit allow/deny matchers
- ask host application
- hook-controlled allow/deny/update
- non-interactive auto-deny for tools requiring user input

The policy engine should eventually return structured reasons so model-visible errors, audit logs, and telemetry all agree.

## Sessions

Sessions persist the conversation trajectory: user messages, assistant messages, tool uses, tool results, compact boundaries, and metadata. They must not silently persist workspace state. Checkpoints and virtual filesystem snapshots should be separate services referenced from session metadata.

Durable session stores should support:

- append-only JSONL transcript
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
