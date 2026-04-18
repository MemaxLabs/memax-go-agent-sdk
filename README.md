# Memax Agent SDK

Memax Agent SDK is a Go-native autonomous agent runtime designed to grow into a
shared foundation for coding agents, personal intelligence agents, and managed
cloud agents. Today, the implementation is most mature on coding-agent
orchestration; the broader product shape is the direction the runtime is being
built toward. It is inspired by modern agent products and SDKs, but designed
around application-owned tools instead of hard-coded system tools.

The core SDK should not assume access to the real filesystem, shell, browser,
network, inbox, calendar, or OS permissions. Those capabilities are modeled as
tools, and the tool implementation decides whether it talks to real
infrastructure, a virtual filesystem, an in-memory workspace, a remote service,
or a test fake.

## Product Shape

The long-term product has three layers:

- **Runtime kernel**: turn loop, sessions, context policies, tool scheduling,
  hooks, permissions, planner/task state, memory, subagents, budgets, and
  observability.
- **Capability adapters**: optional workspace, command, verification, browser,
  doc, email, calendar, remote execution, and other host-owned integrations.
- **Opinionated stacks**: batteries-included configurations built on the same
  kernel and adapters, starting with coding workflows and later expanding to
  personal intelligence and managed cloud agents. Initial `stack/coding` and
  `stack/personal` packages now assemble domain-oriented tools, planner wiring,
  and common safety policies into reusable runtime profiles, with named
  workflow presets for each stack.

The SDK is intentionally built so the same kernel can eventually support:

- coding-agent experiences in the Claude Code / Codex class
- personal intelligence experiences in the OpenClaw / Hermes class
- managed cloud-agent products in the Claude Managed Agents class

Those are target stack shapes, not claims that the SDK already ships personal
or managed-cloud stacks at the same maturity as its coding-first runtime work.

## Current Status

This repository is embeddable today and is strongest at the neutral
runtime-kernel layer plus coding-oriented adapters. It is now expanding from a
coding-first focus into a broader agent-platform shape.

Implemented capabilities

Runtime kernel and orchestration primitives:

- provider-neutral model streaming interfaces
- typed tool registry and executor
- compiled JSON Schema validation before tool execution
- per-tool result size limits with truncation metadata
- host-owned storage for oversized tool results with preview handles
- tool and session lifecycle hooks
- structured permission policies with host approval callbacks
- in-memory and append-only JSONL session stores
- resumable and forkable sessions
- bounded subagent tool with parent/child session correlation
- task state tools for agent planning and progress tracking
- host-owned planner policies with deterministic prompt plan injection and task-state adapters
- opt-in tool selection and search for deferred tool loading
- agent identity profiles, deterministic prompt assembly, and local skill manifests
- project, user, and session memory injection through source-neutral prompt memory sources
- opt-in memory search/save/delete tools for host-owned durable memory backends
- final-result memory distillation candidates with optional host-controlled persistence
- structured final-output contracts with JSON Schema validation and retry
- provider-neutral model usage events and token telemetry
- opt-in run budget governors for turns, model calls, tool calls, tokens, and duration
- deterministic autonomy eval harness for scripted orchestration scenarios
- OpenAI Responses API model adapter
- Anthropic Messages API model adapter
- context-window policies for recent-message limiting, token budgets, and summarizing compaction
- optional OpenTelemetry tracing adapter
- first autonomous query loop skeleton

Coding-oriented adapters and toolkits:

- memory-backed, OS-backed, and `io/fs`-backed file tools for examples and tests
- checkpoint manager interfaces and checkpoint tools
- workspace, patch, diff, restore, verification, and command toolkits over
  host-owned backends
- initial `stack/coding` assembly for batteries-included coding workflows
- initial `stack/personal` assembly for durable-memory, metadata-first
  note/document tools, metadata-first messaging tools, metadata-first
  scheduling tools, task, approval, skill, and delegation-oriented personal
  workflows
- initial `stack/cloudmanaged` assembly for tenant-scoped managed-worker
  workflows with explicit tenant admission, per-session quota validation, and
  host-owned audit sinks
- initial SQLite-backed scheduling adapter for durable local calendar backends
- skill discovery tools

`stack/coding` now exposes named presets so hosts can start from a workflow
profile and then attach their own backends:

```go
cfg := coding.CIRepair()
cfg.Workspace = workspaceStore
cfg.Verifier.Verifier = verifier
cfg.Command.Runner = runner
stack, err := coding.New(cfg)
```

Preset intent is locked down by deterministic eval coverage. See
[docs/coding-stack-presets.md](docs/coding-stack-presets.md) for the full
preset contract, default policy posture, runnable examples, and the
authoritative eval scenario names.

| Preset | Intended workflow | Eval-backed recovery coverage |
| --- | --- | --- |
| `safe_local` | cautious local editing with checkpointing and verification | `coding_preset_safe_local`, `coding_preset_safe_local_rollback_recovery` |
| `ci_repair` | reproducible repair loops with longer command budgets | `coding_preset_ci_repair`, `coding_preset_ci_repair_approval_recovery` |
| `interactive_dev` | long-lived sessions such as watchers and dev servers | `coding_preset_interactive_dev`, `coding_preset_interactive_dev_session_cleanup` |

The current implementation is strongest on coding-agent orchestration because
that is the most demanding initial domain. The architecture is being hardened
so those same runtime primitives can later power personal intelligence and
managed cloud-agent stacks without forking the core.

`stack/personal` now exposes named presets so hosts can start from a
personal-intelligence workflow profile and then attach only the host-owned
backends they need:

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

Preset intent is locked down by deterministic eval coverage. See
[docs/personal-stack-presets.md](docs/personal-stack-presets.md) for the full
preset contract, default policy posture, and authoritative eval scenario names.

| Preset | Intended workflow | Eval-backed coverage |
| --- | --- | --- |
| `personal_assistant` | careful personal assistance with durable recall plus approval-gated memory, note, message, and schedule writes | `personal_preset_personal_assistant`, `personal_preset_personal_assistant_memory_approval_recovery`, `personal_preset_personal_assistant_note_recall`, `personal_preset_personal_assistant_message_recall`, `personal_preset_personal_assistant_message_approval_recovery`, `personal_preset_personal_assistant_schedule_recall`, `personal_preset_personal_assistant_schedule_approval_recovery` |
| `research_partner` | longer-horizon personal research and scoped delegation | `personal_preset_research_partner` |

## Try It

Run the deterministic memory-workspace example:

```sh
go run ./examples/memory_tools
```

It uses a scripted model and in-memory `list_files`, `read_file`, and `write_file` tools, so it does not require network access or model-provider credentials.

The same file tools can run over different workspace implementations:

```go
memory := filetools.NewMemoryFS(map[string]string{"README.md": "hello"})
disk, err := filetools.NewOSFS(
    ".",
    filetools.WithSymlinkContainment(true),
    filetools.WithMaxReadBytes(512*1024),
    filetools.WithMaxListEntries(5000),
)
readonly, err := filetools.NewReadOnlyFS(embedFS)
```

Additional deterministic examples:

```sh
go run ./examples/session_resume
go run ./examples/advanced_stack
go run ./examples/coding_stack
go run ./examples/personal_stack
go run ./examples/personal_notes_stack
go run ./examples/personal_messages_stack
go run ./examples/personal_schedule_stack
go run ./examples/ci_embedding
go run ./examples/skills_identity
go run ./examples/eval_scenarios
```

`session_resume` shows how to continue a durable transcript by passing `Options.SessionID`. `advanced_stack` composes task state, checkpointing, context budgeting, tool search, and memory-backed file tools in one run. `coding_stack` now demonstrates a `ci_repair` workflow that hits an approval gate, requests approval explicitly, retries the patch, reruns the check, and verifies before completion. `personal_stack` demonstrates a `personal_assistant` workflow where recalled durable memory changes the saved follow-up preference, approval gates the durable write, and the saved memory is then recalled through the normal tool layer. `personal_notes_stack` demonstrates the note-first variant: search metadata, read the relevant note, request approval for a new reusable note, and save content that reflects the recalled note style. `personal_messages_stack` demonstrates the thread-first messaging variant: search thread metadata, read the relevant conversation, request approval for an outbound reply, and send content that reflects the recalled guidance. `personal_schedule_stack` demonstrates the schedule-first variant: search event metadata, read the relevant event, request approval for a reschedule, and change the calendar state only after the recalled event constraints are visible in the transcript. `ci_embedding` shows a bounded, read-only agent run shaped for CI jobs. `skills_identity` shows how an agent profile and relevant skills become deterministic prompt guidance. `eval_scenarios` runs the deterministic autonomy scenario suite and exits non-zero on failure.

To try the embeddable HTTP shape:

```sh
go run ./examples/server_embedding
curl -s localhost:8080/query -d '{"prompt":"inspect workspace"}'
```

For a live-provider HTTP server, set an explicit provider and model:

```sh
AGENT_PROVIDER=openai OPENAI_API_KEY=... OPENAI_MODEL=... go run ./examples/server_live
AGENT_PROVIDER=anthropic ANTHROPIC_API_KEY=... ANTHROPIC_MODEL=... go run ./examples/server_live
```

To use the OpenAI adapter:

```go
client := openai.NewFromEnv("",
    openai.WithBaseURL("https://gateway.example.com/v1"),
    openai.WithTimeout(60*time.Second),
    openai.WithMaxOutputTokens(4096),
)
events, err := memaxagent.Query(ctx, "Inspect the workspace.", memaxagent.Options{
    Model: client,
    Tools: registry,
})
```

Set `OPENAI_BASE_URL` or use `openai.WithBaseURL` to route OpenAI requests
through a gateway or compatible endpoint. Following OpenAI ecosystem
conventions, this is the API-version base URL, so it normally includes `/v1`;
the adapter sends requests to `BaseURL + "/responses"`. Use
`openai.WithEndpoint` only when you need to override the full Responses API
endpoint directly; `Endpoint` takes precedence over `BaseURL`.
`openai.WithTimeout` applies a request-scoped timeout and can be combined with
`openai.WithHTTPClient` when you need a custom transport.

To use the Anthropic adapter:

```go
client := anthropic.NewFromEnv("",
    anthropic.WithBaseURL("https://gateway.example.com"),
    anthropic.WithTimeout(60*time.Second),
    anthropic.WithMaxTokens(4096),
)
events, err := memaxagent.Query(ctx, "Inspect the workspace.", memaxagent.Options{
    Model: client,
    Tools: registry,
})
```

Set `ANTHROPIC_BASE_URL` or use `anthropic.WithBaseURL` to route Anthropic
requests through a gateway or compatible endpoint. Following Anthropic
ecosystem conventions, this is the service root URL, so it normally does not
include `/v1`; the adapter sends requests to `BaseURL + "/v1/messages"`. Use
`anthropic.WithEndpoint` only when you need to override the full Messages API
endpoint directly; `Endpoint` takes precedence over `BaseURL`.
`anthropic.WithTimeout` applies a request-scoped timeout and can be combined
with `anthropic.WithHTTPClient` when you need a custom transport.

Runnable live-provider examples are available behind explicit environment variables:

```sh
OPENAI_API_KEY=... OPENAI_MODEL=... go run ./examples/live_openai
ANTHROPIC_API_KEY=... ANTHROPIC_MODEL=... go run ./examples/live_anthropic
```

To emit OpenTelemetry spans, import `github.com/MemaxLabs/memax-go-agent-sdk/otel` as `sdkotel`:

```go
events, err := memaxagent.Query(ctx, "Inspect the workspace.", memaxagent.Options{
    Model:  client,
    Tools:  registry,
    Tracer: sdkotel.NewTracer("my-agent-service"),
    Meter:  sdkotel.NewMeter("my-agent-service"),
})
```

When providers report token usage, `Query` emits `EventUsage` events and
attaches aggregate usage to the final `EventResult`:

```go
for event := range events {
    switch event.Kind {
    case memaxagent.EventUsage:
        log.Printf("usage: input=%d output=%d", event.Usage.InputTokens, event.Usage.OutputTokens)
    case memaxagent.EventResult:
        if event.Usage != nil {
            log.Printf("total tokens: %d", event.Usage.TotalTokens)
        }
    }
}
```

The full event ordering and telemetry contract is documented in
[docs/observability.md](docs/observability.md), including progressive skill
events, memory candidate events, context compaction provenance, streaming
tool-use lifecycle events, and budget-denial behavior.

To persist sessions in SQLite, use `session/sqlitestore` with any `database/sql` SQLite driver:

```go
db, err := sql.Open("sqlite", "file:memax.db")
sessions, err := sqlitestore.New(ctx, db)
```

To preserve full oversized tool results outside the model transcript, configure
`Options.ResultStore`. The model receives a bounded preview plus handle metadata:

```go
largeResults := resultstore.NewMemoryStore()
events, err := memaxagent.Query(ctx, "Inspect the large report.", memaxagent.Options{
    Model:       client,
    Tools:       registry,
    ResultStore: largeResults,
})
```

For coding-agent style edits, use the optional `workspace` package and
`toolkit/workspacetools`. The core SDK still does not assume real filesystem
access; hosts provide a `workspace.Store` backed by memory, git, a database, or
a remote sandbox, then register only the tools they want the model to use:

```go
ws := workspace.NewMemoryStore(map[string]string{
    "README.md": "hello",
})
workspaceTools, err := workspacetools.NewTools(ws)
registry := tool.NewRegistry(workspaceTools...)
```

For a real root-confined directory, use `workspace.NewOSStore`. It uses
forward-slash workspace paths at the SDK boundary, contains symlinks by default,
and keeps checkpoints as in-memory snapshots for the lifetime of the store:

```go
ws, err := workspace.NewOSStore("/path/to/repo")
```

The standard workspace tools support read/list, guarded atomic patches,
standard unified diffs, dry-run patch previews, diffs, checkpoints, and restore
through the normal tool, permission, hook, budget, and event pipeline. Unified
diff failures include nearby current content so the model can repair stale
patches instead of guessing. Hosts that need approval-style gating can use
`workspacetools.NewApplyPatchToolWithReview` to inspect a validated
`workspace.PatchSummary` before mutation. Use individual
`workspacetools.New*Tool` constructors when a host wants to expose only a subset
of capabilities. Workspace paths use forward-slash, workspace-relative syntax;
OS-backed adapters should translate and contain paths at the adapter boundary.
Patch, diff, checkpoint, and restore tools emit dedicated workspace lifecycle
events derived from result metadata.

For verification loops, use the optional `toolkit/verifytools` package. It
defines a small host-owned `Verifier` interface so applications can expose
tests, typechecks, lint, policy checks, or remote CI validators without giving
the core SDK shell access. Failed verification returns a model-visible tool
error with diagnostics, so the agent can repair, re-run verification, or restore
a checkpoint through normal tools.

For general test/build/lint commands, use the optional `toolkit/commandtools`
package. It exposes `run_command` over a host-owned `Runner`; the core SDK
still does not execute shell commands itself. The reference `OSRunner` launches
argv directly, applies cwd containment when rooted, enforces timeouts, and caps
stdout/stderr. `OSRunner` is not a sandbox and does not filter commands or
arguments; hosts that need an allowlist, container, network policy, or process
sandbox should wrap or replace it. It does not inherit `os.Environ()` by
default because environment variables often contain secrets:

```go
runner, err := commandtools.NewOSRunner("/path/to/repo")
commandTool := commandtools.NewTool(commandtools.Config{
    Runner:    runner,
    MayMutate: false, // set true for generators, formatters, or scripts that write
})
registry.Register(commandTool)
```

Command results are normal tool results with exit code, timeout, duration, and
output metadata. Non-zero exits are model-visible tool errors, enabling the
agent to patch, rerun, or ask for approval through the normal loop.
Use `toolkit/agentpolicy` to add argv-prefix allowlists, denylists,
input-bound approvals, or verify-before-final gates for selected commands.

For longer-lived commands such as dev servers or watch jobs, the same package
also provides `start_command`, `write_command_input`,
`resize_command_terminal`, `read_command_output`, `stop_command`, and
`list_commands` over host-owned session interfaces. This keeps background
process lifecycle explicit and transcript-visible instead of hiding it behind a
single opaque shell tool. `commandtools.OSSessionManager` is the reference
local adapter: it launches argv directly, applies rooted cwd resolution,
retains bounded stdout/stderr chunks with drop accounting, keeps stdin open for
interactive writes, and supports start, write, resize, read, stop, list, and cleanup
over real local processes. When `start_command` sets `tty: true`, the adapter
starts a PTY-backed terminal session and returns `pty` output chunks instead of
pretending terminal-native output is plain stdout. `cols` and `rows` set the
initial geometry, and `resize_command_terminal` updates it later for shells,
REPLs, pagers, and TUIs that care about terminal width and height. Like
`OSRunner`, it is not a
sandbox and does not filter executables, arguments, or system access.
On Unix the PTY path uses native pseudo terminals; on Windows it uses ConPTY
when the host OS exposes the required APIs. Graceful stop is best-effort and
platform dependent; on Unix it attempts an interrupt before forcing
termination, while some Windows processes fall back to forced termination
immediately.
`commandtools.ScriptedSessionManager` remains available for deterministic tests
and evals. `commandtools.SessionCleanupOptions(...)` installs `SessionEnded`
cleanup hooks so session-owned commands do not outlive the parent agent run;
hosts using managed sessions should install it by default. `commandtools.NewSessionTools(...)`
builds the standard tool set for hosts that implement the full managed-session
surface:

```go
manager, err := commandtools.NewOSSessionManager("/path/to/repo")
sessionTools, err := commandtools.NewSessionTools(manager)
registry := tool.NewRegistry(sessionTools...)
runner := hook.NewRunner(commandtools.SessionCleanupOptions(manager)...)
```

To require a machine-readable final answer, configure `Options.Output` with a
JSON Schema. The default prompt builder includes the contract, and `Query`
validates the final answer. If validation fails, the SDK appends a repair prompt
and retries once by default:

```go
events, err := memaxagent.Query(ctx, "Summarize the deployment risk.", memaxagent.Options{
    Model: client,
    Output: output.Contract{
        Schema: map[string]any{
            "type":     "object",
            "required": []any{"risk", "summary"},
            "properties": map[string]any{
                "risk":    map[string]any{"type": "string", "enum": []any{"low", "medium", "high"}},
                "summary": map[string]any{"type": "string"},
            },
            "additionalProperties": false,
        },
    },
})
```

To configure agent identity and skills:

```go
events, err := memaxagent.Query(ctx, "Review the migration plan.", memaxagent.Options{
    Model: client,
    Identity: identity.Identity{
        Name:    "Migration Reviewer",
        Role:    "database change reviewer",
        Mission: "identify correctness, rollback, and operational risks",
    },
    Skills: []skill.Skill{{
        Name:        "database-review",
        Description: "Review schema and data migration plans.",
        WhenToUse:   "The task involves SQL, migrations, indexes, or rollback plans.",
        Content:     "Check lock behavior, rollback path, data safety, and observability.",
    }},
})
```

Skills can come from the filesystem, embedded `fs.FS` values, HTTP endpoints,
databases, or any custom `skill.Source`. Local `SKILL.md` directories can be
loaded up front or exposed through `Options.SkillSource`:

```go
skills, err := skill.LoadDir(ctx, ".agents/skills")
events, err := memaxagent.Query(ctx, "Review the migration plan.", memaxagent.Options{
    Model:       client,
    SkillSource: skill.StaticSource(skills),
})
```

For larger skill catalogs, use progressive disclosure so the prompt contains
metadata only and the model loads full instructions through the transcript:

```go
events, err := memaxagent.Query(ctx, "Review the migration plan.", memaxagent.Options{
    Model:           client,
    SkillSource:     skill.StaticSource(skills),
    SkillDisclosure: skill.DisclosureProgressive,
})
```

In progressive mode, the SDK automatically exposes a read-only `load_skill`
tool. The tool returns the selected skill body as a normal tool result, so skill
use is visible in events and durable session history instead of being hidden
prompt state. Progressive discovery and skill tool activity also emit dedicated
events: `EventSkillDiscovery`, `EventSkillSearch`, `EventSkillLoaded`, and
`EventSkillResourceLoaded`.

Other source adapters are available:

```go
embeddedSkills, err := skill.LoadFS(ctx, embedFS, "skills")
source := &skill.PrefetchSource{
    Source: skill.MultiSource{
        skill.StaticSource(embeddedSkills),
        skill.TimeoutSource{
            Source:  skill.HTTPSource{URL: "https://example.com/skills.json"},
            Timeout: 2 * time.Second,
        },
        skill.SourceFunc(loadSkillsFromDatabase),
    },
    TTL:            5 * time.Minute,
    RefreshTimeout: 2 * time.Second,
}
```

To let the model discover skills through the normal tool layer, register
`toolkit/skilltools`. Search results are metadata-only by default; use
`load_skill` for the full instructions.

```go
searchSkills, err := skilltools.NewSearchTool(skilltools.Config{
    Source: skill.StaticSource(skills),
})
registry := tool.NewRegistry(searchSkills)
```

The supported `SKILL.md` metadata subset and source formats are documented in
[docs/skills.md](docs/skills.md).

To inject durable host context, pass explicit memories or a custom
`memory.Source`. Sources are loaded once per `Query` run and receive the active
session ID, parent session ID, identity, current model-visible messages, and
bounded recent user-message query text:

```go
events, err := memaxagent.Query(ctx, "Review the billing change.", memaxagent.Options{
    Model: client,
    Memories: []memory.Memory{{
        Name:    "billing-rules",
        Scope:   memory.ScopeProject,
        Content: "Billing changes require audit logging and rollback notes.",
    }},
    MemorySource: memory.SourceFunc(func(ctx context.Context, req memory.Request) ([]memory.Memory, error) {
        return loadRelevantMemories(ctx, req.SessionID, req.Query)
    }),
})
```

To let the model explicitly search or request updates to host-owned durable
memory, register `toolkit/memorytools` against a backend that implements the
capabilities you want to expose:

```go
memories := memory.NewMemoryStore(nil)
memoryTools, err := memorytools.NewTools(memorytools.Config{
    Source:  memories,
    Writer:  memories,
    Deleter: memories,
})
registry := tool.NewRegistry(memoryTools...)
```

Cloud memory systems can implement `memory.Source`, `memory.Writer`, and
`memory.Deleter` directly. Only registered tools are available to the model, so
hosts can expose search-only memory, save-with-approval memory, or full
read/write/delete memory through normal tool permissions.

To propose durable memories from completed work without automatically writing
anything, configure a `memory.Distiller`. Distillation runs only after a valid
final answer and emits `EventMemoryCandidates` before `EventResult`. Hosts can
also opt into a `MemoryCandidateHandler` to approve, filter, or persist those
candidates after the event is emitted. Handler failures are reported as
non-terminal `EventMemoryCandidateHandlerError` events so the final answer still
reaches the caller:

```go
store := memory.NewMemoryStore(nil)
events, err := memaxagent.Query(ctx, "Finish the migration review.", memaxagent.Options{
    Model: client,
    MemoryDistiller: memory.RuleDistiller{{
        WhenResultContains: "rollback",
        WhenPlanContains:   "migration",
        Memory: memory.Memory{
            Name:    "migration-rollback",
            Scope:   memory.ScopeProject,
            Content: "Migration reviews require rollback notes.",
        },
        Reason:     "completed review established rollback requirement",
        Confidence: 0.9,
    }},
    MemoryCandidateHandler: memory.WriterHandler{
        Writer:        store,
        MinConfidence: 0.8,
        Scopes:        []memory.Scope{memory.ScopeProject},
    },
})
```

To expose bounded worker agents as a tool, import `github.com/MemaxLabs/memax-go-agent-sdk/toolkit/subagents` and register the returned tool:

```go
delegate, err := subagents.NewTool(subagents.Config{
    Agents: []subagents.Agent{{
        Name:        "investigator",
        Description: "Investigates a focused question in a child session.",
        Options: memaxagent.Options{
            Model:    client,
            Sessions: sessions,
            MaxTurns: 8,
        },
    }},
})
```

Subagent delegation can be scoped to host task state. Configure
`PlanSource: tasktools.SubagentPlanner(tasks, ...)` so a call with `task_id`
gives the child only that task's plan step, evidence, and verification hints.
Configure `ResultHandler: tasktools.NewSubagentProgressHandler(tasks)` to
record successful child results as task progress. Both hooks are opt-in; child
runs remain normal bounded `Query` calls through the `run_subagent` tool.

To install common safety policies without hard-coding them into the runtime,
use `toolkit/agentpolicy`. For example, require a checkpoint before mutating
workspace patches:

```go
policy := agentpolicy.RequireCheckpointBeforePatch()
events, err := memaxagent.Query(ctx, "Patch README.md safely.", memaxagent.Options{
    Model: client,
    Tools: registry,
    Hooks: hook.NewRunner(policy.Options()...),
})
```

The first patch attempt is denied as a recoverable tool result until
`workspace_checkpoint` succeeds in the same session. Dry-run patch previews are
allowed.

For rollback guidance after failed verification, install
`agentpolicy.RecommendRollbackOnFailedVerification()` into hooks and wrap the
host verifier. The policy records successful `workspace_checkpoint` results and
adds model-visible restore guidance to failed `workspace_verify` results. It
does not restore automatically; rollback still happens through the normal
`workspace_restore` tool so the transcript, permissions, hooks, and events stay
observable.

To prevent premature final answers after workspace changes, install
`agentpolicy.RequireVerificationBeforeFinal()`. The policy tracks successful
mutating workspace patches and restores, denies finalization until a successful
`workspace_verify` result is observed in the same session, and appends the
denial as a normal user repair prompt so the model can recover by calling tools.
Use `Options.MaxFinalDenials` to cap these repair turns; zero uses the SDK
default and negative disables before-final retries.

Command execution can use the same policy surface. `agentpolicy.DenyCommands`
and `agentpolicy.AllowCommands` match argv prefixes, not shell text, so hosts
can block dangerous executables or expose a narrow command set:

```go
policy := agentpolicy.AllowCommands(
    agentpolicy.MatchCommandPrefix("go", "test"),
    agentpolicy.MatchCommandPrefix("go", "vet"),
)
```

For sensitive commands, combine `approvaltools.NewTool` with
`agentpolicy.RequireApprovalBeforeCommands(...)`. The command approval policy
denies matching `run_command` attempts until the model requests approval for
the command tool action. By default, a granted approval authorizes later
commands matching the configured argv prefixes for that session. With
`agentpolicy.WithCommandInputBoundApprovals()` and
`agentpolicy.WithCommandSingleUseApprovals()`, approval applies only to the
later matching JSON input and is consumed on first use.

Commands that mutate generated files can also require verification before the
final answer:

```go
policy := agentpolicy.RequireVerificationAfterCommands(
    agentpolicy.MatchCommandPrefix("go", "generate"),
)
```

After a matching command succeeds, finalization is denied until
`workspace_verify` succeeds in the same session.

For human or host approval flows, expose `approvaltools.NewTool` and combine it
with `agentpolicy.RequireApprovalBeforeTools(...)`. The policy denies configured
tools until the model calls `request_approval` for the tool name and the host
approver grants it. Denials and approvals are normal tool results, so the model
can either retry after approval or choose a safe fallback when approval is
denied.

The approver is the security boundary. `approvaltools.StaticApprover` is useful
for tests and trusted automation, but production hosts should connect the tool to
their own human review, policy service, or approval queue. By default approvals
are reusable for the named tool until the session ends. For stricter workflows,
use `agentpolicy.RequireApprovalBeforeToolsWithOptions` with
`agentpolicy.WithSingleUseApprovals()` and/or
`agentpolicy.WithInputBoundApprovals()`. Input-bound approval requires the model
to include the proposed `tool_input` in its `request_approval` call; the policy
then allows only a later tool call whose canonical input hash matches.
Approval requests, grant/denial decisions, and consumed grants emit typed
approval events plus `memax.approval.*` counters for audit and UI integration.
Use the optional `summary` field on `request_approval` to provide host-facing
review context such as title, risk, affected paths, change counts, and byte
delta. `workspacetools.ApprovalSummaryFromPatchInput` derives that summary from
a `workspace_apply_patch` input without reading workspace state.

To bound an agent run across model calls, tool calls, tokens, turns, and wall
time, set `Options.Budget`:

```go
events, err := memaxagent.Query(ctx, "Inspect the workspace.", memaxagent.Options{
    Model: client,
    Tools: registry,
    Budget: budget.Policy{
        MaxModelCalls: 8,
        MaxToolCalls:  32,
        MaxTotalTokens: 40_000,
        MaxDuration:   2 * time.Minute,
    },
})
```

Budgets are checked at stable lifecycle boundaries: before a model call, after
reported model usage, before a tool batch, and at turn start. Custom governors
can implement `budget.Governor` for tenant-specific quotas or hosted cost
systems.

To provide an inspectable host plan without giving the model hidden state, set
`Options.Planner`:

```go
events, err := memaxagent.Query(ctx, "Review the migration.", memaxagent.Options{
    Model: client,
    Tools: registry,
    Planner: planner.Static(planner.Plan{
        Goal:        "review migration safely",
        Constraints: []string{"inspect files before judging risk"},
        Steps: []planner.Step{{
            ID:        "step-1",
            Title:     "read migration file",
            Status:    planner.StatusInProgress,
            ToolHints: []string{"read_file"},
            VerificationHints: []string{
                "run workspace_verify test before final answer",
            },
        }},
    }),
})
```

Planner policies receive the active session ID, parent session ID, identity,
messages, and recent user-query text. The default prompt builder injects the
returned plan as the named `memax.plan` prompt part. Verification hints are
advisory plan context; the host must still expose verification as a normal tool
such as `workspace_verify`.

Existing task state can drive the same planner context. `tasktools.Planner`
adapts a task store into `planner.Policy`, so updates made through
`upsert_task` are reflected in the next model request:

```go
tasks := tasktools.NewMemoryStore([]tasktools.Task{{
    ID: "task-1", Title: "read migration", Status: tasktools.StatusInProgress,
}})
events, err := memaxagent.Query(ctx, "Continue the review.", memaxagent.Options{
    Model: client,
    Tools: tool.NewRegistry(tasktools.NewListTool(tasks), tasktools.NewUpsertTool(tasks)),
    Planner: tasktools.Planner(tasks,
        planner.WithTaskGoal("review migration safely"),
        planner.WithTaskToolHints(tasktools.ListToolName, tasktools.UpsertToolName),
        planner.WithTaskVerificationHints("run workspace_verify test before final answer"),
    ),
})
```

Verification can also feed task progress when the host opts in. Wrap a
`verifytools.Verifier` with `tasktools.NewVerificationProgressVerifier` and ask
the model to include `metadata.task_id` in the verification request. Passing
checks mark the task completed by default; failing checks keep it in progress
unless configured otherwise. The next planner turn reloads the task store and
shows the updated status, notes, and evidence.

To regression-test agent behavior without a live model, use `agenteval` with a
scripted model and assertions:

```go
report := agenteval.Runner{}.Run(ctx, agenteval.Case{
    Name:   "tool recovery",
    Prompt: "read the file",
    Options: memaxagent.Options{
        Model: agenteval.NewScriptedModel(
            []model.StreamEvent{{Kind: model.StreamToolUse, ToolUse: model.ToolUse{
                ID: "tool-1", Name: "read", Input: json.RawMessage(`{"path":"README.md"}`),
            }}},
            []model.StreamEvent{{Kind: model.StreamText, Text: "done"}},
        ),
        Tools: registry,
    },
    Assertions: []agenteval.Assertion{
        agenteval.ToolUsed("read"),
        agenteval.FinalEquals("done"),
    },
})
if err := report.Error(); err != nil {
    return err
}
```

The `agenteval/scenarios` package includes reusable deterministic cases for
tool recovery, structured output repair, memory search/save, memory
distillation candidates, session resume, context retry, subagent delegation,
planner-guided tool use, planner/task-state updates, provider usage mapping,
and provider tool-use round trips. It also covers governance recovery for permission
denials, hook denials, oversized tool results, budget stops, and deferred tool
discovery:

```go
report := agenteval.Runner{}.Run(ctx, scenarios.All()...)
```

Cases that intentionally stop with an agent error can set `AllowError: true`
and assert `Result.RunErr`, for example with `agenteval.RunErrorContains`.

Next implementation work is tracked in [docs/roadmap.md](docs/roadmap.md).
Server embedding guidance is available in [docs/server.md](docs/server.md).
