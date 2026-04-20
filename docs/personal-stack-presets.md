# Personal Stack Presets

`stack/personal` exposes two named workflow presets. This document is the
navigation layer for that public surface: what each preset is for, which
governance rules are enabled by default, where runnable examples live, and
which deterministic eval scenarios lock the behavior down.

## Shared baseline

Both presets start from the same default governance baseline via
`personal.DefaultPolicies()`:

- `RequireMemoryApproval`
- `RequireNoteApproval`
- `RequireMessageApproval`
- `RequireScheduleCreateApproval`
- `RequireScheduleRescheduleApproval`
- `RequireScheduleCancelApproval`
- `SingleUseApprovals`
- `InputBoundApprovals`

Important:

- durable-memory approval only applies when the host exposes mutable memory
  tools (`save_memory` and/or `delete_memory`)
- note approval only applies when the host exposes mutable note tools
  (`save_note` and/or `delete_note`)
- message approval only applies when the host exposes outbound messaging
  tools (`send_message`)
- schedule approval only applies when the host exposes calendar mutation tools
  (`create_schedule_event`, `reschedule_schedule_event`, and/or
  `cancel_schedule_event`)
- delegation approval is opt-in and stays off unless the host enables it
- approval gates still require an `approvaltools.Approver`; presets do not
  invent one implicitly

## Preset Contract

| Preset | Intended workflow | Policies enabled by default | Preset-specific posture | Example | Eval scenarios |
| --- | --- | --- | --- | --- | --- |
| `personal_assistant` | careful personal assistance with durable recall, explicit task tracking, cautious memory/note/message/schedule writes, and host-owned proactive scheduled runs | `RequireMemoryApproval`, `RequireNoteApproval`, `RequireMessageApproval`, `RequireScheduleCreateApproval`, `RequireScheduleRescheduleApproval`, `RequireScheduleCancelApproval`, `SingleUseApprovals`, `InputBoundApprovals` | `MaxTurns=28`, `MaxToolConcurrency=4`, balanced personal-assistant identity, progressive skill disclosure, prompt guidance to recall durable context first and search note, message, and schedule metadata before loading or mutating | [`examples/personal_stack`](../examples/personal_stack/main.go), [`examples/personal_notes_stack`](../examples/personal_notes_stack/main.go), [`examples/personal_messages_stack`](../examples/personal_messages_stack/main.go), [`examples/personal_inbox_stack`](../examples/personal_inbox_stack/main.go), [`examples/personal_schedule_stack`](../examples/personal_schedule_stack/main.go), [`examples/personal_briefing_stack`](../examples/personal_briefing_stack/main.go), [`examples/personal_week_ahead_stack`](../examples/personal_week_ahead_stack/main.go), [`examples/personal_task_ledger_stack`](../examples/personal_task_ledger_stack/main.go), [`examples/personal_scheduled_task_ledger_stack`](../examples/personal_scheduled_task_ledger_stack/main.go), [`examples/personal_notification_delivery_stack`](../examples/personal_notification_delivery_stack/main.go), [`examples/personal_proactive_stack`](../examples/personal_proactive_stack/main.go), [`examples/personal_scheduled_inbox_stack`](../examples/personal_scheduled_inbox_stack/main.go), [`examples/personal_jmap_scheduled_inbox_stack`](../examples/personal_jmap_scheduled_inbox_stack/main.go) | `personal_preset_personal_assistant`, `personal_preset_personal_assistant_memory_approval_recovery`, `personal_preset_personal_assistant_note_recall`, `personal_preset_personal_assistant_message_recall`, `personal_preset_personal_assistant_message_approval_recovery`, `personal_preset_personal_assistant_inbox_triage_reply_followup`, `personal_preset_personal_assistant_inbox_send_backend_failure`, `personal_preset_personal_assistant_jmap_inbox_reply`, `personal_preset_personal_assistant_schedule_recall`, `personal_preset_personal_assistant_schedule_approval_recovery`, `personal_preset_personal_assistant_schedule_conflict_recovery`, `personal_preset_personal_assistant_daily_briefing`, `personal_preset_personal_assistant_week_ahead_planning`, `personal_preset_personal_assistant_week_ahead_task_ledger`, `personal_preset_personal_assistant_week_ahead_task_ledger_sqlite`, `personal_preset_personal_assistant_scheduled_daily_briefing`, `personal_preset_personal_assistant_scheduled_daily_briefing_notification`, `personal_preset_personal_assistant_scheduled_notification_delivery_retry`, `personal_preset_personal_assistant_scheduled_run_stale_reconciliation`, `personal_preset_personal_assistant_scheduled_inbox_triage`, `personal_preset_personal_assistant_scheduled_inbox_triage_jmap`, `personal_preset_personal_assistant_scheduled_task_ledger_maintenance` |
| `research_partner` | longer-horizon personal research, synthesis, and scoped delegation | `RequireMemoryApproval`, `RequireNoteApproval`, `RequireMessageApproval`, `RequireScheduleCreateApproval`, `RequireScheduleRescheduleApproval`, `RequireScheduleCancelApproval`, `SingleUseApprovals`, `InputBoundApprovals` | `MaxTurns=36`, `MaxToolConcurrency=6`, higher-autonomy research identity, progressive skill disclosure, prompt guidance to separate working notes from durable memory and search note/message/schedule metadata before loading larger items or changing calendar state | none yet | `personal_preset_research_partner` |

## Reading the table

- **Policies enabled by default** means the preset turns those hooks on even if
  the host does nothing else.
- **Preset-specific posture** covers identity, prompt guidance, skill
  disclosure, and run budgets that are not expressed as binary
  policies. Personal presets intentionally set a full `identity.Identity`
  because tone, mission, and autonomy shape personal-assistant behavior more
  directly than the coding stack's workflow-focused prompt overlays.
- **Eval scenarios** are the authoritative behavior contract. If a preset's
  runtime behavior changes, the corresponding scenario list should change in the
  same commit.

## Host guidance

Use a preset as the starting posture, then attach only the host-owned backends
you actually want to expose:

```go
cfg := personal.PersonalAssistant()
cfg.Memory = memorytools.Config{
    Source: memoryStore,
    Writer: memoryStore,
}
cfg.Notes = notetools.Config{
    Searcher: noteStore,
    Reader:   noteStore,
    Writer:   noteStore,
}
cfg.Messages = messagetools.Config{
    Searcher: messageStore,
    Reader:   messageStore,
    Sender:   messageStore,
}
cfg.Schedule = scheduletools.Config{
    Searcher:    scheduleStore,
    Reader:      scheduleStore,
    Rescheduler: scheduleStore,
}
cfg.Tasks = taskStore
cfg.Approval.Approver = approver

stack, err := personal.New(cfg)
```

Common sources of confusion:

- enabling mutable memory tools without an approver under the default preset
  posture will fail stack construction, because durable-memory approval is on by
  default
- enabling mutable note tools without an approver under the default preset
  posture will fail stack construction, because note approval is on by default
- enabling outbound messaging without an approver under the default preset
  posture will fail stack construction, because message approval is on by
  default
- enabling schedule create/reschedule/cancel tools without an approver under
  the default preset posture will fail stack construction, because schedule
  approvals are on by default
- hosts that want mutable note tools without approvals must both attach the
  note `Writer` and/or `Deleter` and explicitly disable `RequireNoteApproval`
- hosts that want outbound messaging without approvals must both attach the
  message `Sender` and explicitly disable `RequireMessageApproval`
- hosts that want schedule mutation tools without approvals must both attach
  the schedule `Creator`, `Rescheduler`, and/or `Canceller` and explicitly
  disable the corresponding `RequireSchedule*Approval` flags
- `research_partner` does not automatically expose delegation; the host still
  has to attach a `subagents.Config`
- progressive skill disclosure remains harmless when no skill source is
  attached; the preset only defines the default posture when one exists
- proactive workflows can persist trigger state through
  `stack/personal/sqlitestore` when hosts need scheduled runs to survive
  process restarts instead of using the in-memory reference store
- hosts that run multiple proactive workflows can register named
  `stack/personal` scheduled workflows and fire all or selected names through
  `FireScheduledWorkflows`; the registry is discoverable workflow
  configuration, while the scheduled-run store remains the durable idempotency
  boundary
- proactive scheduled runs emit `run_state_changed` observer events for
  queued, running, succeeded, and failed transitions, so hosts can audit or
  monitor personal workflow lifecycle without polling the scheduled-run store
  alone. Events are emitted only after the scheduled-run store accepts the
  corresponding durable transition. Stores that implement
  `ScheduledRunStoreWithStaleReconciliation` can be swept through
  `FailStaleScheduledRuns` or `WatchStaleScheduledRuns`; stale queued or
  running records become failed records and emit the same lifecycle event
  contract.
- `NewScheduledRunNotifier` converts those scheduled-run lifecycle events into
  a host-owned notification outbox. The default `done_only` policy mirrors only
  terminal completions, `state_changes` mirrors every transition, and `silent`
  leaves lifecycle observation enabled without user-facing delivery. The
  reference memory outbox is intentionally small; `stack/personal/sqlitestore`
  persists the same outbox records for restart-safe lookback. Production hosts
  can replace or drain it into email, push, chat, or durable inbox delivery.
  Stores that implement `ScheduledRunNotificationDeliveryStore` add an
  at-least-once delivery contract: workers claim pending notifications with a
  lease, mark successful attempts delivered, and mark transient failures with a
  retry time. `DrainScheduledRunNotifications` is the reference one-pass worker
  helper: it claims a bounded batch, invokes a host-owned delivery handler,
  records successes, and reschedules handler failures with configurable backoff.
  `WatchScheduledRunNotifications` runs the same drain helper immediately and on
  a ticker for long-running host delivery workers, and the drain result observer
  option lets hosts emit delivery metrics for successful drain passes. Hosts
  can opt into `WithScheduledRunNotificationMaxAttempts` to stop retrying
  poison notifications and mark them `dead_lettered` for manual recovery. Host
  channel failures become retryable or terminal outbox state; store claim/ack
  errors return to the worker because the durable state is uncertain.
  Notification records carry the scheduled prompt plus terminal result or error
  text; host-owned delivery backends should apply their own redaction policy
  before sending them to external channels. The
  `personal_notification_delivery_stack` example demonstrates this durable
  outbox with a transient channel failure, delayed retry, and successful ack.
- attaching `Tasks` gives personal workflows a durable task ledger. The
  planner reloads task state before every model request, so follow-ups created
  in one run through `upsert_task` can be visible to a later run before the
  model calls `list_tasks`. Use `toolkit/tasktools/sqlitestore` when that
  ledger needs to survive process restarts; the week-ahead task-ledger eval
  covers both in-memory continuity and a SQLite-backed resume with a fresh
  store instance. The scheduled task-ledger maintenance eval also covers a
  proactive trigger that lists persisted pending work before completing or
  blocking tasks, with the scheduled occurrence deduplicated by durable run
  state.
