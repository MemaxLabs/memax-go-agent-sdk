# Personal Stack Presets

`stack/personal` exposes two named workflow presets. This document is the
navigation layer for that public surface: what each preset is for, which
governance rules are enabled by default, where runnable examples live, and
which deterministic eval scenarios lock the behavior down.

## Shared baseline

Both presets start from the same default governance baseline via
`personal.DefaultPolicies()`:

- `RequireMemoryApproval`
- `SingleUseApprovals`
- `InputBoundApprovals`

Important:

- durable-memory approval only applies when the host exposes mutable memory
  tools (`save_memory` and/or `delete_memory`)
- delegation approval is opt-in and stays off unless the host enables it
- approval gates still require an `approvaltools.Approver`; presets do not
  invent one implicitly

## Preset Contract

| Preset | Intended workflow | Policies enabled by default | Preset-specific posture | Example | Eval scenarios |
| --- | --- | --- | --- | --- | --- |
| `personal_assistant` | careful personal assistance with durable recall, explicit task tracking, and cautious memory writes | `RequireMemoryApproval`, `SingleUseApprovals`, `InputBoundApprovals` | `MaxTurns=28`, `MaxToolConcurrency=4`, balanced personal-assistant identity, progressive skill disclosure, prompt guidance to recall context before writing new durable memory | [`examples/personal_stack`](../examples/personal_stack/main.go) | `personal_preset_personal_assistant`, `personal_preset_personal_assistant_memory_approval_recovery` |
| `research_partner` | longer-horizon personal research, synthesis, and scoped delegation | `RequireMemoryApproval`, `SingleUseApprovals`, `InputBoundApprovals` | `MaxTurns=36`, `MaxToolConcurrency=6`, higher-autonomy research identity, progressive skill disclosure, prompt guidance to separate working notes from durable memory and use scoped delegation when available | none yet | `personal_preset_research_partner` |

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
cfg.Tasks = taskStore
cfg.Approval.Approver = approver

stack, err := personal.New(cfg)
```

Common sources of confusion:

- enabling mutable memory tools without an approver under the default preset
  posture will fail stack construction, because durable-memory approval is on by
  default
- `research_partner` does not automatically expose delegation; the host still
  has to attach a `subagents.Config`
- progressive skill disclosure remains harmless when no skill source is
  attached; the preset only defines the default posture when one exists
