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
- Add checkpoint interfaces for virtual workspaces. Initial checkpoint manager and tools exist.
- Add resumable/forkable durable sessions. Initial resume/list/get/fork support exists.
- Add retry-after-context-failure and external result storage.
- Add optional metrics for turns, model calls, tools, hooks, and compaction. Initial SDK-owned meter interface and OpenTelemetry adapter exist.
- Add performance benchmarks for long sessions and high tool concurrency. Initial benchmark coverage exists for context windows, tool selection, concurrent tool execution, and memory sessions.

## Phase 4: DX Polish

- Provide clear examples for server embedding, CI embedding, and local CLI embedding. Initial deterministic examples cover memory tools, session resume, CI embedding, advanced toolkit composition, and HTTP server embedding. Live OpenAI and Anthropic examples are available behind explicit environment variables.
- Publish stable API docs.
- Add golden tests for event streams and transcript compatibility. Initial golden coverage protects the public query event sequence for a tool-using run.
- Add adapters for common virtual filesystem and memory store implementations. Initial file workspace adapters cover in-memory maps, root-confined host directories, and read-only `io/fs.FS` sources. Initial durable session adapters cover memory, JSONL, and SQLite.

## Phase 5: Intelligence Layer

The SDK has strong orchestration, but top-tier agent behavior also depends on
how the model is briefed. This phase makes prompt and skill assembly explicit,
testable, and reusable instead of leaving every embedder to hand-write a large
system prompt.

- Add agent identity profiles. Initial `identity.Identity` support exists with a default Memax-native profile plus configurable role, mission, tone, autonomy level, and constraints.
- Add deterministic prompt assembly. Initial `prompt.Builder` support exists and produces named prompt parts, a stable hash, identity guidance, tool-use guidance, selected skills, and host prompt text.
- Add local and remote skill manifests. Initial `skill.LoadDir`, `skill.LoadFS`, `skill.StaticSource`, `skill.SourceFunc`, `skill.MultiSource`, `skill.CachedSource`, `skill.HTTPSource`, `Options.SkillSource`, and relevance selection exist for `SKILL.md` directories and source-neutral skill loading.
- Add server-friendly async wrappers. Initial `QueryAsync`, `skill.TimeoutSource`, `skill.PrefetchSource`, and `tool.WithTimeout` support exists.
- Add prompt snapshots and golden tests. Initial prompt golden tests cover identity, tools, skills, provider profiles, and host prompt composition.
- Add skill discovery tools. Initial `toolkit/skilltools` search tool exists, exposing skills through the normal tool layer.
- Add skill-scoped hooks and permissions. Initial `skill.PolicySource` support exists for host accept/deny/rewrite policy over loaded skills.
- Add agent identity propagation for subagents. Child agents can already receive full `Options`; future examples should define dedicated reviewer, explorer, implementer, and verifier identities.
- Add provider-specific prompt profiles. Initial `prompt.ProfileOpenAI` and `prompt.ProfileAnthropic` guidance exists.
- Add project/user memory injection. Initial `memory.Source`, `Options.MemorySource`, explicit `Options.Memories`, relevance selection, and named prompt-part injection exist for persistent project rules, user preferences, session notes, and organization context.
- Add reactive context-failure recovery. Initial `Options.ContextRetry` support retries once when providers return a recognized context-window error.
- Add external large-result storage. Very large tool outputs should be stored by a host-provided blob/result store and returned to the model as handles plus previews.
- Add structured output contracts. Hosts should be able to request final answers in typed JSON schemas and receive validation/retry behavior.
- Add cost and token accounting. Provider adapters should surface usage when available, and the SDK should emit usage metrics and events.
- Add autonomy eval harness. Build deterministic and live evals for planning, tool recovery, subagent delegation, session resume, compaction quality, and final-answer correctness.

## Phase 6: Ecosystem and Hardening

- Add more durable stores and workspace adapters, starting with production SQLite examples, object-store checkpoint managers, and git-backed workspace checkpoints.
- Add MCP/tool bridge examples while keeping the core tool contract provider-neutral.
- Add release automation, API compatibility checks, and generated reference docs.
- Add security hardening guides for filesystem adapters, approval flows, credentials, and multi-tenant server embedding.
