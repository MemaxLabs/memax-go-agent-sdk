# Roadmap

## Phase 0: Foundation

- Establish public package shape.
- Keep core filesystem-neutral.
- Add model, tool, permission, and session interfaces.
- Add executor tests for ordering and permission denial.

## Phase 1: Useful Local Agent

- Add hosted model provider adapters behind `model.Client`. OpenAI Responses API and Anthropic Messages API adapters exist with constructor options, default endpoints, environment-driven base URL overrides, explicit endpoint overrides, and timeout helpers.
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

- Add subagent tool with parent/child session correlation. Initial bounded worker tool, scoped plan handoff, and opt-in task progress return exist.
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
- Add golden tests for event streams and transcript compatibility. Golden coverage protects the public query event sequence for a tool-using run, richer observability events, budget-denial ordering, and workspace lifecycle events.
- Add adapters for common virtual filesystem and memory store implementations. Initial file workspace adapters cover in-memory maps, root-confined host directories, and read-only `io/fs.FS` sources. Initial durable session adapters cover memory, JSONL, and SQLite. Initial source-neutral `workspace.Store` support covers guarded patches, unified diffs with dry-run previews, conflict diagnostics, diffs, checkpoints, restores, toolkit tools, host-owned verification tools, eval coverage over in-memory and verified workspaces, and a root-confined `workspace.OSStore` for real directories.

## Phase 5: Intelligence Layer

The SDK has strong orchestration, but top-tier agent behavior also depends on
how the model is briefed. This phase makes prompt and skill assembly explicit,
testable, and reusable instead of leaving every embedder to hand-write a large
system prompt.

- Add agent identity profiles. Initial `identity.Identity` support exists with a default Memax-native profile plus configurable role, mission, tone, autonomy level, and constraints.
- Add deterministic prompt assembly. Initial `prompt.Builder` support exists and produces named prompt parts, a stable hash, identity guidance, tool-use guidance, selected skills, and host prompt text.
- Add local and remote skill manifests. Initial `skill.LoadDir`, `skill.LoadFS`, `skill.StaticSource`, `skill.SourceFunc`, `skill.MultiSource`, `skill.CachedSource`, `skill.HTTPSource`, `Options.SkillSource`, relevance selection, and opt-in progressive disclosure through `Options.SkillDisclosure`, item-count and byte-bounded metadata discovery, metadata-first `toolkit/skilltools` recovery search, `load_skill`, `skill.ResourceRef`, `Options.SkillResourceSource`, `read_skill_resource`, and skill visibility events/metrics exist for `SKILL.md` directories and source-neutral skill/resource loading.
- Add server-friendly async wrappers. Initial `QueryAsync`, `skill.TimeoutSource`, `skill.PrefetchSource`, and `tool.WithTimeout` support exists.
- Add prompt snapshots and golden tests. Initial prompt golden tests cover identity, tools, skills, provider profiles, and host prompt composition.
- Add skill discovery tools. Initial `toolkit/skilltools` search tool exists, exposing skills through the normal tool layer.
- Add skill-scoped hooks and permissions. Initial `skill.PolicySource` support exists for host accept/deny/rewrite policy over loaded skills.
- Add agent identity propagation for subagents. Child agents can already receive full `Options`; future examples should define dedicated reviewer, explorer, implementer, and verifier identities.
- Add provider-specific prompt profiles. Initial `prompt.ProfileOpenAI` and `prompt.ProfileAnthropic` guidance exists.
- Add planner policies. Initial `planner.Policy`, `planner.Plan`, `planner.TaskSource`, verification hints, `Options.Planner`, named `memax.plan` prompt injection, task-state adapter support through `toolkit/tasktools`, opt-in verification-to-task and subagent-to-task progress updates, and planner-guided eval coverage exist for host-owned strategy, verification, delegation, and progress context.
- Add project/user memory injection. Initial `memory.Source`, `Options.MemorySource`, explicit `Options.Memories`, relevance selection, and named prompt-part injection exist for persistent project rules, user preferences, session notes, and organization context. Initial `memory.Writer`, `memory.Deleter`, in-memory memory store, and `toolkit/memorytools` search/save/delete tools exist for opt-in agent memory mutation. Initial `memory.Distiller`, `Options.MemoryDistiller`, `EventMemoryCandidates`, and `Options.MemoryCandidateHandler` support exists for post-result memory candidate proposals, host approval, and optional persistence without automatic writes by default.
- Add reactive context-failure recovery. Initial `Options.ContextRetry` support retries once when providers return a recognized context-window error.
- Add external large-result storage. Initial `resultstore.Store`, `Options.ResultStore`, in-memory result storage, truncation preview handles, transcript metadata, and fallback-on-store-error behavior exist for oversized tool outputs.
- Add structured output contracts. Initial `output.Contract`, `Options.Output`, prompt contract injection, JSON Schema validation, final-answer repair retry, and retry-exhaustion errors exist.
- Add cost and token accounting. Initial provider-neutral `model.Usage`, stream usage events, `EventUsage`, final-result usage aggregation, token meter counters, and OpenAI/Anthropic usage mapping exist. Cost calculation remains future host/provider policy.
- Add run budget governors. Initial `budget.Governor`, zero-value-disabled `budget.Policy`, `Options.Budget`, budget stop reason, and agent-loop enforcement exist for turn, model-call, tool-call, token, and elapsed-duration limits.
- Add agent policy presets. Initial `toolkit/agentpolicy` hook preset exists
  for checkpoint-before-patch recovery and model-mediated rollback guidance
  after failed verification without hard-coding policy into the core loop.
- Add streaming tool execution. Initial provider tool-use lifecycle events
  (`tool_use_start`, `tool_use_delta`, complete `tool_use`) exist for OpenAI
  and Anthropic streams, and the agent loop can start read-only,
  concurrency-safe tools before trailing assistant text finishes while
  preserving durable transcript order. Eval coverage exists for safe overlap,
  mutating-tool ordering, permission-denial recovery, stream-failure cleanup,
  and cancellation.
- Add autonomy eval harness. Initial `agenteval` runner, scripted model, result capture, expected-error assertions, reusable assertions, and `agenteval/scenarios` package exist for deterministic tool recovery, structured-output repair, memory search/save, memory distillation candidates, memory candidate handler persistence, session resume, context retry, subagent delegation, subagent scoped plan progress, planner-guided tool use, planner verification repair, planner/task progress from verification, planner/task-state updates, progressive skill disclosure, httptest-backed provider usage mapping, provider tool-use round trips, permission/hook denial recovery, checkpoint-before-patch policy recovery, rollback-policy recovery, large-result storage recovery, budget-stop enforcement, and deferred tool discovery. Live evals remain future work.
- Add context retention hardening. Initial `contextwindow.PreserveImportant`
  support keeps loaded skills, stored result handles, and tool errors with
  structurally valid tool-use groups under aggressive trimming.
- Add context compaction provenance. Initial `contextwindow.PolicyWithResult`,
  `contextwindow.CompactionRecord`, summary metadata, summary replacement, and
  `EventContextCompacted` support exist for inspectable compaction behavior.
  Compaction records include stable counts, summary hashes, and bounded summary
  previews.

## Phase 6: Ecosystem and Hardening

- Maintain `docs/agent-runtime-quality.md` as the standing competitive-agent
  gap analysis. Each major subsystem should be compared against the local
  TypeScript and Codex references before implementation, then moved from
  Foundation to Competitive or Leading with eval coverage.
- Harden progressive skill disclosure. Initial metadata-only prompt discovery,
  explicit `load_skill`, transcript-visible loaded skill content,
  `read_skill_resource`, transcript-visible loaded resources, and eval coverage
  exist. Loaded-skill retention across aggressive trimming has initial
  context-policy and eval coverage. Next steps are larger catalog budget tests
  and resource adapters for common hosts.
- Add more durable stores and workspace adapters, starting with production SQLite examples, object-store checkpoint managers, git-backed workspace checkpoints, remote sandbox workspaces, and richer patch approval events.
- Add MCP/tool bridge examples while keeping the core tool contract provider-neutral.
- Add release automation, API compatibility checks, and generated reference docs.
- Add security hardening guides for filesystem adapters, approval flows, credentials, and multi-tenant server embedding.
