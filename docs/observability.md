# Observability Contract

Memax emits a typed event stream from `Query` and records SDK-owned telemetry
through `telemetry.Tracer` and `telemetry.Meter`. Events are the primary
application contract: they are ordered, transcript-aware, and suitable for UI,
audit logs, deterministic evals, and host-side cost or policy rollups.

## Event Ordering

Every run starts with `EventSessionStarted` after the session is created or
resumed. Each model turn then follows this order when the relevant feature is
configured:

1. `EventContextApplied` and `EventContextCompacted`, if a context policy runs
   or produces compaction provenance.
2. Budget check before the model call. A denial emits `EventError` and no
   `EventModelRequest` for that denied call.
3. `EventModelRequest`.
4. `EventSkillDiscovery`, when progressive skill metadata is included in the
   prompt. This is emitted per prompt build, so a context retry can emit more
   than one discovery event for the same turn.
5. Provider stream events: `EventAssistant`, `EventToolUseStart`,
   `EventToolUseDelta`, `EventToolUse`, and `EventUsage`.
6. `EventToolResult` for each executable tool call. Skill-related tool results
   can be followed by `EventSkillSearch`, `EventSkillLoaded`, or
   `EventSkillResourceLoaded`. Workspace-related tool results can be followed
   by `EventWorkspacePatch`, `EventWorkspaceDiff`,
   `EventWorkspaceCheckpoint`, or `EventWorkspaceRestore`. Approval tool
   results can be followed by `EventApprovalRequested` and either
   `EventApprovalGranted` or `EventApprovalDenied`. A later tool result that
   consumes an approval grant can be followed by `EventApprovalConsumed`.
   Tenant-denied tool results can be followed by `EventTenantDenied`.
   Command tool results can be followed by `EventCommandFinished`,
   `EventCommandStarted`, `EventCommandInput`, `EventCommandOutput`, or
   `EventCommandStopped`.
7. If the assistant returns a final answer, before-final hooks can deny
   finalization. A denial appends a user repair prompt and starts the next turn;
   no `EventResult` or terminal `EventError` is emitted for that denial unless
   the configured final-denial retry budget is exhausted.
8. If the final answer is accepted, optional `EventMemoryCandidates` after
   successful distillation and before `EventResult`.
9. Optional non-terminal `EventMemoryCandidateHandlerError` if the opt-in
   candidate handler fails.
10. `EventResult` for successful completion, or `EventError` for terminal
    failures.

When startup fails before `Query` can return an event channel, only `QueryAsync`
can emit structured startup denials. In that case a tenant denial at session
start is emitted as `EventTenantDenied` before the terminal `EventError`.

Tool-use lifecycle events are paired by `ToolUse.ID`. The complete
`EventToolUse` remains the executable contract; `EventToolUseStart` and
`EventToolUseDelta` expose provider streaming progress before full JSON input is
available. Read-only, concurrency-safe tools may start early only after the
complete `EventToolUse` is available and after normal validation, hooks,
permissions, budgets, result limiting, and telemetry boundaries.

If a provider stream fails after an early tool starts, the SDK emits a
cancellation `EventToolResult` before `EventError` so observers do not see an
orphaned `EventToolUse`.

## Event Payloads

`Event.Kind` determines which payload pointer is populated:

- `EventAssistant`: `Message`
- `EventToolUseStart`, `EventToolUseDelta`, `EventToolUse`: `ToolUse`; delta
  events also set `ToolUseDelta`
- `EventToolResult`: `ToolResult`
- `EventUsage`: `Usage`
- `EventContextApplied`: `Context`
- `EventContextCompacted`: `Compaction`
- `EventMemoryCandidates`: `Memory`
- `EventSkillDiscovery`, `EventSkillSearch`, `EventSkillLoaded`,
  `EventSkillResourceLoaded`: `Skill`
- `EventWorkspacePatch`, `EventWorkspaceDiff`, `EventWorkspaceCheckpoint`,
  `EventWorkspaceRestore`: `Workspace`
- `EventApprovalRequested`, `EventApprovalGranted`, `EventApprovalDenied`,
  `EventApprovalConsumed`: `Approval`
- `EventTenantDenied`: `Tenant`
- `EventCommandFinished`, `EventCommandStarted`, `EventCommandInput`, `EventCommandOutput`,
  `EventCommandStopped`: `Command`
- `EventResult`: `Result` and optional aggregate `Usage`
- `EventError` and `EventMemoryCandidateHandlerError`: `Err`

Skill events share `SkillEvent` with action-specific fields:

- `discovery`: `SelectedSkills`, `Selected`, `Omitted`, `PromptBytes`,
  `MetadataOnly`
- `search`: `Query`, `Matches`, `MetadataOnly`
- `load`: `SkillName`
- `resource_load`: `SkillName`, `ResourceName`

Memory candidates are proposals. The SDK emits them before the final result but
does not persist them unless `Options.MemoryCandidateHandler` is configured.
Handler failures are non-terminal and are surfaced as
`EventMemoryCandidateHandlerError`.

Workspace events are derived from tool-result metadata rather than direct core
imports of the `workspace` package. This keeps the core provider- and
workspace-neutral while giving hosts first-class audit events for patch, diff,
checkpoint, and restore operations. Patch and diff events include compact
summary fields for total changes, added files, modified files, deleted files,
byte delta, and affected paths.

Verification events are also metadata-derived. `workspace_verify` and custom
verification tools can report a host-owned check name, pass/fail status,
diagnostic count, and affected paths. Failed verification should be a tool error
result, not a terminal agent error, so the model can repair and retry or restore
a checkpoint.

Command events are metadata-derived from `run_command` and compatible custom
command tools. `EventCommandFinished` carries argv, cwd, exit code, timeout
status, duration, retained output byte counts, and truncation status.
Managed-session command tools can additionally emit `EventCommandStarted`,
`EventCommandInput`, `EventCommandOutput`, `EventCommandResized`, and
`EventCommandStopped` with a command session ID, status, optional PID, whether
the session is PTY-backed, terminal cols/rows, next output sequence, stdin byte
count, returned chunk count, and dropped buffer accounting. Command
stdout/stderr remain in the paired `EventToolResult`, preserving the normal
transcript-visible tool contract while giving hosts structured process status.
Terminal geometry metadata is emitted only for PTY-backed sessions; non-TTY
command sessions omit cols/rows.

Approval events are metadata-derived from `request_approval` results and from
policy metadata attached to later tool results. Request events expose the action,
decision, reason, optional input hash, and optional structured review summary
such as title, risk, paths, and change counts. Consumed events expose the action,
whether the grant was single-use, and whether it was input-bound. This keeps
approval UI and audit logs out of generic tool-result parsing while preserving
the transcript-visible approval contract.

Tenant denial events are emitted from explicit tenant-validation seams rather
than generic string parsing. They include the denied boundary (`session_start`,
`model_request`, or `tool_use`), the opaque tenant and subject identifiers, the
string-typed tenant attributes, and the host-visible denial reason.

## Metrics And Spans

The core loop records stable counters and histograms for query lifecycle, turn
lifecycle, model streams, model usage, budget denials, hooks, memory
distillation, skill discovery, and skill tool activity. Tool execution records
its own spans and counters through the executor. All telemetry APIs are
provider-neutral; the `otel` package adapts them to OpenTelemetry.

Important metric names include:

- `memax.query.started`, `memax.query.completed`, `memax.query.errors`
- `memax.turn.started`, `memax.turn.duration_ms`
- `memax.model.stream.started`, `memax.model.stream.duration_ms`,
  `memax.model.stream.errors`
- `memax.model.input_tokens`, `memax.model.output_tokens`,
  `memax.model.total_tokens`
- `memax.budget.exceeded`
- `memax.final.denials`
- `memax.memory.candidates`, `memax.memory.candidate_handler.errors`
- `memax.skill.discovery`, `memax.skill.search`, `memax.skill.loaded`,
  `memax.skill.resource_loaded`
- `memax.workspace.patch`, `memax.workspace.diff`,
  `memax.workspace.checkpoint`, `memax.workspace.restore`
- `memax.verification.run`
- `memax.command.finished`, `memax.command.started`,
  `memax.command.input`, `memax.command.output`, `memax.command.resized`,
  `memax.command.stopped`, `memax.command.duration_ms`
- `memax.approval.requests`, `memax.approval.grants`,
  `memax.approval.denials`, `memax.approval.consumed`
- `memax.tenant.denials`

Telemetry complements events; it should not be the only source of application
state. Use events for ordered behavior and spans/metrics for aggregate
monitoring.

For managed-host products, `stack/cloudmanaged` now exposes a host-owned audit
subscriber over the same event stream plus reference memory and JSONL sinks.
This keeps audit persistence ordered with the emitted events while leaving sink
durability, buffering, and replication policy under host control. Because the
runtime now carries event observation through delegated child-agent runs,
managed audit trails can cover parent and child sessions without special-case
subagent plumbing in application code. Cloud-managed quota enforcement is
admission-time accounting rather than billing-accurate usage accounting, so a
reserved model or tool slot is not automatically released if the later action
aborts. Quota-store failures are treated as denials by default, which keeps the
managed stack fail-closed unless a host deliberately wraps the store or
validator with a different policy. The reference `MemoryQuotaStore` is
single-process and keys only on session ID; multi-tenant or multi-replica
deployments should attach a scope-aware shared store such as
`stack/cloudmanaged/redistore`. Hosts that prefer `database/sql` durability can
also use `stack/cloudmanaged/sqlitestore`, which preserves the same reservation
contract and exposes explicit stale-session pruning instead of Redis-style TTL
expiry. Audit sinks can now also be wrapped with the
async cloudmanaged sink adapter when hosts want bounded buffered persistence
instead of synchronous inline writes on the event-emission path. Async sink
error handlers should remain fast and non-blocking; the drop-oldest overflow
path reports pressure inline with the caller's write path, while sink-write
failures are reported from the background worker. Overflow notifications use a
detached context rather than the dropped record's original tracing scope, so
hosts that need trace correlation should instrument the wrapped sink directly.
Durable managed runs now emit explicit `run_state_changed` observer events as
they move through queued, running, succeeded, failed, or canceled lifecycle,
so audit sinks and dashboards can follow transitions without polling-only
state reconstruction.

Personal proactive scheduled runs use the same `run_state_changed` observer
event when a deterministic occurrence moves through queued, running,
succeeded, or failed lifecycle. The event includes the scheduled run ID,
trigger name, occurrence timestamp, prompt, status, terminal result, and
terminal error when one exists, so hosts can build proactive-workflow audit
trails without polling the scheduled-run store as their only source of truth.
These events are emitted after the scheduled-run store accepts the
corresponding durable transition; if a store write fails, that transition is
not synthesized.
When hosts reconcile orphaned personal scheduled runs with
`FailStaleScheduledRuns` or `WatchStaleScheduledRuns`, each stale queued or
running occurrence that is marked failed emits the same event after the store
accepts the durable failure update.
`stack/personal.NewScheduledRunNotifier` is a host-owned observer adapter over
these events. It writes idempotent run/status notifications to a configured
outbox store and supports `done_only`, `state_changes`, and `silent` policies,
leaving the actual delivery channel and buffering policy under host control.
Notification records include the scheduled prompt plus terminal result or error
text, so production outbox backends are responsible for any redaction policy
needed before email, push, chat, or inbox delivery.

## Regression Coverage

The public event contract is protected by golden tests:

- `testdata/golden/basic_event_stream.json` covers the minimal tool-use loop.
- `testdata/golden/observability_event_stream.json` covers compaction
  provenance, progressive skill discovery, streaming tool-use deltas, skill
  search/load/resource events, usage, memory candidates, and final usage
  aggregation.
- `testdata/golden/budget_denial_event_stream.json` covers budget-denial
  ordering and error emission.
- `testdata/golden/workspace_event_stream.json` covers workspace checkpoint,
  patch, diff, and restore event ordering.
- `testdata/golden/verification_event_stream.json` covers failed verification
  as a tool error plus verification event ordering.
- `testdata/golden/tenant_denial_event_stream.json` covers tenant-denied tool
  execution ordering and structured tenant denial payloads.
- `testdata/golden/command_session_event_stream.json` covers managed command
  session start, PTY-backed interactive stdin write, resize, and stop ordering.

When adding a new event kind or changing event order, update the docs and golden
files in the same change.
