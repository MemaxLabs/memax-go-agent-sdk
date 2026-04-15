# Roadmap

## Phase 0: Foundation

- Establish public package shape.
- Keep core filesystem-neutral.
- Add model, tool, permission, and session interfaces.
- Add executor tests for ordering and permission denial.

## Phase 1: Useful Local Agent

- Add hosted model provider adapters behind `model.Client`. OpenAI Responses API and Anthropic Messages API adapters exist.
- Add SDK examples for custom in-memory file tools. Initial `list_files`, `read_file`, and `write_file` toolkit plus runnable example exist.
- Add JSON schema validation before tool execution. Done in the initial Phase 1 slice.
- Add durable JSONL session store. Done in the initial Phase 1 slice.
- Add integration tests with a fake model stream that calls tools across multiple turns. Initial coverage exists.

## Phase 2: Production Orchestration

- Add hook engine. Tool-use and session lifecycle hooks exist.
- Add structured permission modes and host approval callbacks. Ordered allow/deny/ask rules, matchers, and approval callbacks exist.
- Add context budgeting and automatic compaction. Recent-message, token-budget, and summarizing-budget policies exist.
- Add tool result truncation. Per-tool truncation exists.
- Add OpenTelemetry tracing. Query, turn, context, model-stream, and tool-execution spans exist.

## Phase 3: Advanced Autonomy

- Add subagent tool with parent/child session correlation. Initial bounded worker tool exists.
- Add todo/task state tools. Initial task toolkit exists.
- Add tool search and deferred tool loading for large registries. Initial selector and search toolkit exist.
- Add checkpoint interfaces for virtual workspaces.
- Add resumable/forkable durable sessions. Initial resume/list/get/fork support exists.
- Add retry-after-context-failure and external result storage.
- Add optional metrics for turns, model calls, tools, hooks, and compaction.
- Add performance benchmarks for long sessions and high tool concurrency.

## Phase 4: DX Polish

- Provide clear examples for server embedding, CI embedding, and local CLI embedding.
- Publish stable API docs.
- Add golden tests for event streams and transcript compatibility.
- Add adapters for common virtual filesystem and memory store implementations.
