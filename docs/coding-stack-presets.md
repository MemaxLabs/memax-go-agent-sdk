# Coding Stack Presets

`stack/coding` exposes three named workflow presets. This document is the
navigation layer for that public surface: what each preset is for, which
governance rules are enabled by default, where the runnable example lives, and
which deterministic eval scenarios lock the behavior down.

## Shared baseline

All three presets start from the same default governance baseline via
`coding.DefaultPolicies()`:

- `RequireCheckpointBeforePatch`
- `RecommendRollbackOnFailedVerification`
- `RequireVerificationBeforeFinal`
- `SingleUseApprovals`
- `InputBoundApprovals`

Important: approval gates are not enabled by default. A host must still opt in
to `RequirePatchApproval` and/or command approval policies, and provide an
`approvaltools.Approver`, before the runtime starts denying those actions.

## Preset Contract

| Preset | Intended workflow | Policies enabled by default | Preset-specific posture | Example | Eval scenarios |
| --- | --- | --- | --- | --- | --- |
| `safe_local` | cautious local editing with explicit checkpointing and verification | `RequireCheckpointBeforePatch`, `RecommendRollbackOnFailedVerification`, `RequireVerificationBeforeFinal`, `SingleUseApprovals`, `InputBoundApprovals` | `MaxTurns=24`, `MaxToolConcurrency=4`, local-workspace guidance in the system prompt | none yet | `coding_preset_safe_local`, `coding_preset_safe_local_rollback_recovery` |
| `ci_repair` | reproducible repair loops for build/test failures | `RequireCheckpointBeforePatch`, `RecommendRollbackOnFailedVerification`, `RequireVerificationBeforeFinal`, `SingleUseApprovals`, `InputBoundApprovals` | `MaxTurns=32`, `MaxToolConcurrency=6`, `DefaultTimeout=10m`, `MaxTimeout=30m`, CI repair guidance in the system prompt | [`examples/coding_stack`](../examples/coding_stack/main.go) | `coding_preset_ci_repair`, `coding_preset_ci_repair_approval_recovery` |
| `interactive_dev` | long-lived sessions such as watchers, dev servers, and REPL-style tools | `RequireCheckpointBeforePatch`, `RecommendRollbackOnFailedVerification`, `RequireVerificationBeforeFinal`, `SingleUseApprovals`, `InputBoundApprovals` | `MaxTurns=40`, `MaxToolConcurrency=8`, managed-session guidance in the system prompt, session tool hints (`start_command`, `read_command_output`, `wait_command_output`, `write_command_input`, `stop_command`, `list_commands`, `resize_command_terminal`) when a session backend supports them, and explicit `resume_after_seq` cursor guidance for incremental output | [`examples/coding_wait_repair_stack`](../examples/coding_wait_repair_stack/main.go) | `coding_preset_interactive_dev`, `coding_preset_interactive_dev_wait_repair`, `coding_preset_interactive_dev_wait_cursor_repair`, `coding_preset_interactive_dev_session_cleanup` |

## Reading the table

- **Policies enabled by default** means the preset turns those hooks on even if
  the host does nothing else.
- **Preset-specific posture** covers the differentiators that are not expressed
  as binary policies: command budgets, concurrency, and prompt guidance.
- **Eval scenarios** are the authoritative behavior contract. If a preset's
  runtime behavior changes, the corresponding scenario list should change in the
  same commit.

## Host guidance

Use a preset as the starting posture, then attach only the host-owned backends
you actually want to expose:

```go
cfg := coding.CIRepair()
cfg.Workspace = workspaceStore
cfg.Command.Runner = runner
cfg.Verifier.Verifier = verifier
cfg.Approval.Approver = approver
cfg.Policies.RequirePatchApproval = true

stack, err := coding.New(cfg)
```

Common sources of confusion:

- A patch can still be denied under `safe_local` or `ci_repair` even when no
  approval policy is enabled, because checkpoint-before-patch is on by default.
- A final answer can still be rejected after a successful patch if verification
  has not cleared the preset's dirty state.
- `interactive_dev` does not implicitly clean up sessions. The model is still
  expected to stop them through the normal tool layer, and hosts should install
  the managed-session cleanup hook for session-end cleanup.
