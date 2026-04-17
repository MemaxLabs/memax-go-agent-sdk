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
`EventCommandInput`, `EventCommandOutput`, and `EventCommandStopped` with a
command session ID, status, optional PID, whether the session is PTY-backed,
next output sequence, stdin byte count, returned chunk count, and dropped
buffer accounting. Command
stdout/stderr remain in the paired `EventToolResult`, preserving the normal
transcript-visible tool contract while giving hosts structured process status.

Approval events are metadata-derived from `request_approval` results and from
policy metadata attached to later tool results. Request events expose the action,
decision, reason, optional input hash, and optional structured review summary
such as title, risk, paths, and change counts. Consumed events expose the action,
whether the grant was single-use, and whether it was input-bound. This keeps
approval UI and audit logs out of generic tool-result parsing while preserving
the transcript-visible approval contract.

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
  `memax.command.input`, `memax.command.output`, `memax.command.stopped`,
  `memax.command.duration_ms`
- `memax.approval.requests`, `memax.approval.grants`,
  `memax.approval.denials`, `memax.approval.consumed`

Telemetry complements events; it should not be the only source of application
state. Use events for ordered behavior and spans/metrics for aggregate
monitoring.

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
- `testdata/golden/command_session_event_stream.json` covers managed command
  session start, PTY-backed interactive stdin write, and stop ordering.

When adding a new event kind or changing event order, update the docs and golden
files in the same change.
