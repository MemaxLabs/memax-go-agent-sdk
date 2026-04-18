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
- `SingleUseApprovals`
- `InputBoundApprovals`

Important:

- durable-memory approval only applies when the host exposes mutable memory
  tools (`save_memory` and/or `delete_memory`)
- note approval only applies when the host exposes mutable note tools
  (`save_note` and/or `delete_note`)
- message approval only applies when the host exposes outbound messaging
  tools (`send_message`)
- delegation approval is opt-in and stays off unless the host enables it
- approval gates still require an `approvaltools.Approver`; presets do not
  invent one implicitly

## Preset Contract

| Preset | Intended workflow | Policies enabled by default | Preset-specific posture | Example | Eval scenarios |
| --- | --- | --- | --- | --- | --- |
| `personal_assistant` | careful personal assistance with durable recall, explicit task tracking, and cautious memory/note/message writes | `RequireMemoryApproval`, `RequireNoteApproval`, `RequireMessageApproval`, `SingleUseApprovals`, `InputBoundApprovals` | `MaxTurns=28`, `MaxToolConcurrency=4`, balanced personal-assistant identity, progressive skill disclosure, prompt guidance to recall durable context first and search note and message metadata before loading or sending | [`examples/personal_stack`](../examples/personal_stack/main.go), [`examples/personal_notes_stack`](../examples/personal_notes_stack/main.go), [`examples/personal_messages_stack`](../examples/personal_messages_stack/main.go) | `personal_preset_personal_assistant`, `personal_preset_personal_assistant_memory_approval_recovery`, `personal_preset_personal_assistant_note_recall`, `personal_preset_personal_assistant_message_recall`, `personal_preset_personal_assistant_message_approval_recovery` |
| `research_partner` | longer-horizon personal research, synthesis, and scoped delegation | `RequireMemoryApproval`, `RequireNoteApproval`, `RequireMessageApproval`, `SingleUseApprovals`, `InputBoundApprovals` | `MaxTurns=36`, `MaxToolConcurrency=6`, higher-autonomy research identity, progressive skill disclosure, prompt guidance to separate working notes from durable memory and search note/message metadata before loading larger items or drafting replies | none yet | `personal_preset_research_partner` |

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
- hosts that want mutable note tools without approvals must both attach the
  note `Writer` and/or `Deleter` and explicitly disable `RequireNoteApproval`
- hosts that want outbound messaging without approvals must both attach the
  message `Sender` and explicitly disable `RequireMessageApproval`
- `research_partner` does not automatically expose delegation; the host still
  has to attach a `subagents.Config`
- progressive skill disclosure remains harmless when no skill source is
  attached; the preset only defines the default posture when one exists
