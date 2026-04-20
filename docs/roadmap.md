# Roadmap

## North Star

Build a general autonomous agent runtime, not a narrow coding-agent SDK.

The destination is a single Go-native foundation strong enough to support:

- coding agents in the Claude Code / Codex class
- personal intelligence agents in the OpenClaw / Hermes class
- managed cloud-agent products in the Claude Managed Agents class

That requires three cleanly separated layers:

- a neutral **runtime kernel**
- optional **capability adapters**
- opinionated **stacks/presets** for specific workflows

The roadmap therefore hardens the kernel first, pushes demanding coding-agent
capabilities into explicit adapters, and then turns those primitives into
reusable out-of-the-box stacks.

Coding remains the first domain where the SDK is expected to become
competitive. Personal intelligence and managed cloud-agent stacks are part of
the intended product shape, but they should be described as follow-on stacks
built from the same kernel and adapter seams rather than as domains already at
parity today.

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
- Add adapters for common virtual filesystem and memory store implementations. Initial file workspace adapters cover in-memory maps, root-confined host directories, git-backed checkpoint persistence over real repositories, and read-only `io/fs.FS` sources. Initial durable session adapters cover memory, JSONL, and SQLite. Initial source-neutral `workspace.Store` support covers guarded patches, unified diffs with dry-run previews, conflict diagnostics, diffs, checkpoints, restores, toolkit tools, host-owned verification tools, eval coverage over in-memory and verified workspaces, a root-confined `workspace.OSStore` for real directories, and a `workspace.GitStore` that persists checkpoints, restore baselines, and diff roots through git refs.

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
- Add planner policies. Initial `planner.Policy`, `planner.Plan`, `planner.TaskSource`, verification hints, `Options.Planner`, named `memax.plan` prompt injection, task-state adapter support through `toolkit/tasktools`, durable SQLite task-ledger storage, opt-in verification-to-task and subagent-to-task progress updates, and planner-guided eval coverage exist for host-owned strategy, verification, delegation, and progress context.
- Add project/user memory injection. Initial `memory.Source`, `Options.MemorySource`, explicit `Options.Memories`, relevance selection, and named prompt-part injection exist for persistent project rules, user preferences, session notes, and organization context. Initial `memory.Writer`, `memory.Deleter`, in-memory memory store, and `toolkit/memorytools` search/save/delete tools exist for opt-in agent memory mutation. Initial `memory.Distiller`, `Options.MemoryDistiller`, `EventMemoryCandidates`, and `Options.MemoryCandidateHandler` support exists for post-result memory candidate proposals, host approval, and optional persistence without automatic writes by default.
- Add reactive context-failure recovery. Initial `Options.ContextRetry` support retries once when providers return a recognized context-window error.
- Add external large-result storage. Initial `resultstore.Store`, `Options.ResultStore`, in-memory result storage, truncation preview handles, transcript metadata, and fallback-on-store-error behavior exist for oversized tool outputs.
- Add structured output contracts. Initial `output.Contract`, `Options.Output`, prompt contract injection, JSON Schema validation, final-answer repair retry, and retry-exhaustion errors exist.
- Add cost and token accounting. Initial provider-neutral `model.Usage`, stream usage events, `EventUsage`, final-result usage aggregation, token meter counters, and OpenAI/Anthropic usage mapping exist. Cost calculation remains future host/provider policy.
- Add run budget governors. Initial `budget.Governor`, zero-value-disabled `budget.Policy`, `Options.Budget`, budget stop reason, and agent-loop enforcement exist for turn, model-call, tool-call, token, and elapsed-duration limits.
- Add agent policy presets. Initial `toolkit/agentpolicy` hook preset exists
  for checkpoint-before-patch recovery and model-mediated rollback guidance
  after failed verification, plus bounded verify-before-final gating after
  workspace mutations and explicit approval-before-tool gating with optional
  single-use and input-bound grants. Command policy presets add argv-prefix
  allow/deny rules, exact-input approval for selected commands, and
  verify-before-final gates after matching commands, without hard-coding policy
  into the core loop.
- Add streaming tool execution. Initial provider tool-use lifecycle events
  (`tool_use_start`, `tool_use_delta`, complete `tool_use`) exist for OpenAI
  and Anthropic streams, and the agent loop can start read-only,
  concurrency-safe tools before trailing assistant text finishes while
  preserving durable transcript order. Eval coverage exists for safe overlap,
  mutating-tool ordering, permission-denial recovery, stream-failure cleanup,
  and cancellation.
- Add autonomy eval harness. Initial `agenteval` runner, scripted model, result capture, expected-error assertions, reusable assertions, and `agenteval/scenarios` package exist for deterministic tool recovery, structured-output repair, memory search/save, memory distillation candidates, memory candidate handler persistence, session resume, context retry, subagent delegation, subagent scoped plan progress, planner-guided tool use, planner verification repair, planner/task progress from verification, a composed planner/workspace/managed-command/verification repair loop, planner/task-state updates, progressive skill disclosure, httptest-backed provider usage mapping, provider tool-use round trips, permission/hook denial recovery, checkpoint-before-patch policy recovery, rollback-policy recovery, verify-before-final policy recovery, approval-policy recovery and denial fallback, command approval and command verify-before-final policy recovery, finalization-policy exhaustion, large-result storage recovery, budget-stop enforcement, and deferred tool discovery. Live evals remain future work.
- Add managed command sessions. Initial `toolkit/commandtools` session tools
  (`start_command`, `write_command_input`, `resize_command_terminal`,
  `read_command_output`, `stop_command`, `list_commands`), command session
  cleanup hooks, scripted managed sessions, command session event coverage,
  buffered-read and interactive-write repair eval coverage, PTY-backed terminal
  session support, explicit terminal geometry and live resize support, Unix PTY
  plus Windows ConPTY support, Unix process-group stop/timeout cleanup for
  descendants, and a shared commandtools/sessiontest conformance harness now
  exist over host-owned lifecycle interfaces. Windows ConPTY is
  cross-compiled, unit-tested at the orchestration layer, and awaiting runtime
  coverage in a Windows CI lane for end-to-end validation on the target OS.
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
- Add more durable stores and workspace adapters, starting with production SQLite examples, object-store checkpoint managers, fuller remote sandbox backends on top of the initial `sandbox` adapters, and richer patch approval events.
- Add MCP/tool bridge examples while keeping the core tool contract provider-neutral.
- Add release automation, API compatibility checks, and generated reference docs.
- Add security hardening guides for filesystem adapters, approval flows, credentials, and multi-tenant server embedding.
- Initial tenant scope and admission seams now exist through `tenant.Scope`,
  `Options.Tenant`, and `Options.TenantValidator`, covering session start,
  model-request, and tool-use boundaries. Initial `stack/cloudmanaged`
  quota wiring and adversarial coverage now exist through the
  `managed_worker` preset, and initial host-owned audit sinks now exist through
  the cloudmanaged audit subscriber plus memory/JSONL sinks. Initial
  host-owned quota state now sits behind a `QuotaStore` seam with the
  reference `MemoryQuotaStore`, and the first shared backend now exists through
  `stack/cloudmanaged/redistore`. An initial durable SQL backend now also
  exists through `stack/cloudmanaged/sqlitestore`, which now also implements
  the durable `RunStore` seam for embedded managed-run lifecycle. Initial async audit sink
  wrapping now exists for buffered non-inline audit delivery. Initial durable
  managed background runs now also sit behind a host-owned `RunStore` seam
  through `stack/cloudmanaged`, with reference in-memory tracking plus explicit
  start/get/cancel helpers for queued/running/succeeded/failed/canceled
  lifecycle, and those transitions now emit explicit lifecycle observer events.
  Initial queued worker execution now also exists through
  `EnqueueRun`, `ExecuteRun`, `FailStaleRuns`, and `WatchStaleRuns`, with worker
  heartbeats, explicit stale-run failure handling, and deterministic revocation
  coverage for mid-run tenant denial on queued workers. Remote workers are
  expected to run with the same tenant-validator configuration as the
  enqueueing side rather than a signed worker-token subsystem in the SDK, and
  an initial host-owned helper now exists through `stack/cloudmanaged/remote`
  for claim discovery, non-mutating readiness probes, and reference HTTP
  polling over the same `ExecuteRun` path. A runnable
  `examples/cloudmanaged_remote_stack` example now demonstrates
  the default in-process path plus split server/worker modes over a shared
  SQLite run database. Provider-neutral cloudmanaged metrics now cover managed
  run lifecycle, queue/run durations, tenant denials, quota fallback,
  worker claims, heartbeats, heartbeat errors, and stale failures. A runnable
  `examples/cloudmanaged_observability_stack` fixture now demonstrates audit
  records and low-cardinality metrics across tenant denial plus the remote
  worker path. Next steps are richer cloudmanaged presets, additional
  distributed quota backends, richer durable run backends, and fuller
  remote-execution backends built on top of the same tenant seam.

## Phase 7: Opinionated Stacks

- Initial `stack/coding` assembly now exists with workspace, command,
  verification, managed session, approval, checkpointing, and planner/task
  wiring, plus named workflow presets (`safe_local`, `ci_repair`,
  `interactive_dev`). Initial deterministic eval coverage now exercises all
  three presets end to end, with adversarial rollback recovery coverage for
  `safe_local`, approval-recovery coverage for `ci_repair`, and managed-session
  cleanup coverage for `interactive_dev`. Next steps are broader reference
  embeddings and more adversarial preset suites tuned for competitive coding
  workflows.
- Initial `stack/personal` assembly now exists with durable-memory,
  metadata-first notes/docs tools, metadata-first messaging tools,
  metadata-first scheduling tools, task, approval, progressive-skill, and
  scoped-delegation wiring, plus named workflow presets
  (`personal_assistant`, `research_partner`). Initial deterministic eval
  coverage now exercises durable-memory approval recovery, note-first recall,
  message-thread recall, message approval recovery, inbox triage with reply
  recovery plus follow-up creation, inbox send backend-failure surfacing after
  approval, JMAP-backed inbox reply workflows over a real remote adapter,
  week-ahead planning across memory, notes, inbox, and calendar metadata,
  durable week-ahead task-ledger continuity across two runs including a
  SQLite-reopened resume path, proactive scheduled task-ledger maintenance,
  schedule recall,
  schedule-approval
  recovery, and scoped delegation with parent-visible task progress. An
  initial durable `scheduling/sqlitestore` adapter now provides a
  concrete local calendar backend with metadata-first search semantics, and
  initial `scheduling/caldavclient` plus `scheduling/caldavstore` packages and
  `scheduling/googlecalendarclient` plus `scheduling/googlecalendarstore`
  packages now provide parallel remote calendar adapter seams over both XML and
  JSON backends, each preserving metadata-first search and optimistic
  concurrency for event mutations. Initial `messaging/jmapclient` plus
  `messaging/jmapstore` packages now provide the first real remote inbox
  adapter for the personal stack, preserving metadata-first thread discovery
  over JMAP mail instead of hidden full-message prompt injection. Initial
  host-owned proactive scheduling now also exists through
  `stack/personal` `ScheduledRunStore`, `PeriodicTrigger`, `StartScheduledRun`,
  `FireScheduledTriggers`, `WatchScheduledTriggers`, and named
  `ScheduledWorkflowRegistry` firing. Scheduled runs now emit
  `run_state_changed` observer events, with eval-backed lifecycle and
  idempotency coverage for daily briefing plus idempotency coverage for inbox
  triage and task-ledger maintenance occurrences. Hosts can also reconcile stale
  queued or running scheduled occurrences through `FailStaleScheduledRuns` and
  `WatchStaleScheduledRuns` when their store implements the optional stale
  reconciliation interface, and can mirror scheduled-run lifecycle into a
  host-owned notification outbox through `NewScheduledRunNotifier`; the
  personal SQLite store now persists both scheduled runs and notification
  records for restart-safe lookback. Notification stores can opt in to
  `ScheduledRunNotificationDeliveryStore` for at-least-once claim/ack delivery
  with retry times and expired-lease reclaim, and to the dead-letter extension
  for max-attempt terminal state when poison notifications need manual
  recovery; stores can also expose the recovery extension to requeue inspected
  failed or dead-lettered notifications without resetting attempt history.
  Notification delivery transitions now emit structured observer events after
  durable store updates, so audit sinks can follow claim, delivered, failed,
  dead-lettered, and requeued state changes without polling-only
  reconstruction.
  `GetScheduledRunNotificationStats` adds a store-backed health snapshot for
  current pending, leased, claimable, delivered, failed, dead-lettered, attempt,
  and delivery-lag state. `NewScheduledRunNotificationMetrics` and
  `RecordScheduledRunNotificationStats` turn those ordered transition and
  snapshot signals into provider-neutral telemetry meters.
  `stack/personal/webhook` now ships the first host delivery adapter: a signed
  HTTP handler with idempotency headers and typed status errors. Next steps are
  additional delivery adapters for external personal systems plus more
  adversarial preset suites for privacy-sensitive and long-horizon personal
  workflows.
- Add `stack/cloudmanaged` for multi-tenant, server-embedded, remote-execution
  products with durable jobs, tenancy-aware policy, quotas, audit hooks, and
  managed-agent DX.
- Publish examples and reference embeddings that show how stacks are composed
  from the neutral runtime rather than implemented as forks.
