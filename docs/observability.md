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
   `EventWorkspaceCheckpoint`, or `EventWorkspaceRestore`.
7. If the assistant returns a final answer, optional `EventMemoryCandidates`
   after successful distillation and before `EventResult`.
8. Optional non-terminal `EventMemoryCandidateHandlerError` if the opt-in
   candidate handler fails.
9. `EventResult` for successful completion, or `EventError` for terminal
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
checkpoint, and restore operations.

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
- `memax.memory.candidates`, `memax.memory.candidate_handler.errors`
- `memax.skill.discovery`, `memax.skill.search`, `memax.skill.loaded`,
  `memax.skill.resource_loaded`
- `memax.workspace.patch`, `memax.workspace.diff`,
  `memax.workspace.checkpoint`, `memax.workspace.restore`

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

When adding a new event kind or changing event order, update the docs and golden
files in the same change.
