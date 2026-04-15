# Roadmap

## Phase 0: Foundation

- Establish public package shape.
- Keep core filesystem-neutral.
- Add model, tool, permission, and session interfaces.
- Add executor tests for ordering and permission denial.

## Phase 1: Useful Local Agent

- Add hosted model provider adapters behind `model.Client`. Initial OpenAI Responses API adapter exists.
- Add SDK examples for custom in-memory file tools. Initial `list_files`, `read_file`, and `write_file` toolkit plus runnable example exist.
- Add JSON schema validation before tool execution. Done in the initial Phase 1 slice.
- Add durable JSONL session store. Done in the initial Phase 1 slice.
- Add integration tests with a fake model stream that calls tools across multiple turns. Initial coverage exists.

## Phase 2: Production Orchestration

- Add hook engine: before/after tool-use hooks exist; session start/end, user prompt submit, stop, and compaction hooks remain.
- Add structured permission modes and host approval callbacks.
- Add context budgeting and automatic compaction. Deterministic recent-message and token-budget policies exist; summarizing compaction remains.
- Add tool result truncation and external result storage. Per-tool truncation exists; external result storage remains.
- Add OpenTelemetry spans and metrics.

## Phase 3: Advanced Autonomy

- Add subagent tool with parent/child session correlation.
- Add todo/task state tools.
- Add tool search and deferred tool loading for large registries.
- Add checkpoint interfaces for virtual workspaces.
- Add resumable/forkable durable sessions.
- Add performance benchmarks for long sessions and high tool concurrency.

## Phase 4: DX Polish

- Provide clear examples for server embedding, CI embedding, and local CLI embedding.
- Publish stable API docs.
- Add golden tests for event streams and transcript compatibility.
- Add adapters for common virtual filesystem and memory store implementations.
