# Agent Runtime Quality Plan

This document is the standing gap analysis for evolving Memax Agent SDK from a
solid orchestration SDK into a competitive autonomous agent runtime. The target
is not to copy any one product. The target is to match or exceed the maturity
of leading agent runtimes while preserving the SDK's core constraints:
Go-native, provider-neutral, filesystem-neutral, host-owned tools, and
embeddable server safety.

Coding agents remain the first proving ground because they demand the strongest
combination of planning, workspace mutation, execution, verification, policy,
and long-horizon repair. But the architecture should also be strong enough to
power coding-agent stacks, personal intelligence stacks, and managed
cloud-agent stacks on the same foundation.

Before changing any subsystem, compare against:

- `.reference/ts-source-code`
- `.reference/codex`

Use those references to understand mature behavior, invariants, lifecycle
boundaries, failure modes, prompts, evals, and ergonomics. Do not copy source
code or implementation text.

## Maturity Labels

- **Foundation**: the SDK has the right extension point or minimal behavior, but
  the subsystem is not yet competitive with top agents.
- **Competitive**: behavior is robust, eval-backed, composable, and comparable
  to leading coding agents.
- **Leading**: behavior adds a clear advantage without weakening safety,
  provider neutrality, host ownership, or DX.

## Current Maturity Map

| Subsystem                 | Current State          | Target      |
| ------------------------- | ---------------------- | ----------- |
| Agent loop                | Foundation             | Competitive |
| Provider adapters         | Foundation             | Competitive |
| Tool contract             | Competitive foundation | Leading     |
| Tool execution scheduling | Foundation             | Competitive |
| Permissions and hooks     | Competitive foundation | Competitive |
| Sessions and resume       | Competitive foundation | Competitive |
| Context management        | Foundation             | Competitive |
| Skills                    | Foundation             | Competitive |
| Memory                    | Foundation             | Competitive |
| Planner and tasks         | Foundation             | Competitive |
| Subagents                 | Foundation             | Competitive |
| Workspace and checkpoints | Competitive foundation | Competitive |
| Budgets and usage         | Competitive foundation | Competitive |
| Evals                     | Strong foundation      | Leading     |
| Observability             | Competitive foundation | Competitive |
| Domain stacks/presets     | Foundation             | Competitive |

`Tool contract` targets Leading only in a narrow sense: the interface should
stay minimal and host-owned while supporting production-grade lifecycle
behavior such as cancellation, streaming result visibility, result handles,
schema safety, telemetry, and policy composition. It does not mean broadening
the core SDK into a built-in filesystem, shell, browser, or operating
environment.

`Evals` can legitimately target Leading before every runtime subsystem does.
The eval layer is the quality gate that catches regressions while other
subsystems are still moving from Foundation to Competitive. Strong evals do not
imply that the agent loop itself has already reached the same maturity.

## Core Principles

1. **Progressive disclosure over prompt stuffing.**
   Large capability sets, skills, resources, memories, and tool outputs should
   expose lightweight metadata first and load full content only when needed.

2. **Everything operational goes through tools.**
   Reading, writing, shelling out, browsing, asking users, loading resources,
   mutating memory, and inspecting workspaces must be explicit tool-mediated
   capabilities owned by the host.

3. **Durable transcript semantics.**
   Recovery paths, validation repair, tool errors, memory proposals, plan
   updates, and context transformations must be visible in events and/or
   transcript state where debugging and replay require them.

4. **Context has provenance.**
   Prompt content should have named parts, stable hashes, source metadata, and
   clear lifecycle rules. Hidden prompt changes are regressions.

5. **Eval every intelligence claim.**
   If the SDK claims recovery, planning, delegation, memory, skill use, context
   pressure handling, or budget-aware autonomy, there must be a deterministic
   eval that can fail when the behavior regresses.

6. **Provider ecosystems are authoritative for provider adapters.**
   OpenAI options should follow OpenAI SDK conventions. Anthropic options should
   follow Anthropic SDK conventions. Full endpoint overrides remain available
   for gateways with nonstandard routes.

   This intentionally means provider base URL semantics are not identical:
   OpenAI `BaseURL` is the API-version base such as
   `https://api.openai.com/v1`, while Anthropic `BaseURL` is the service root
   such as `https://api.anthropic.com`. The full `Endpoint` option exists for
   custom gateway paths that do not follow those ecosystem conventions.

7. **General kernel, domain adapters, opinionated stacks.**
   The runtime kernel should stay domain-neutral. Domain-specific capability
   bundles belong in adapters and stack packages, not in the core loop.

## Priority Execution Plan

### 0. Runtime Shape and Product Layering

**Current state:** the SDK has a strong neutral kernel and a growing set of
optional coding-oriented capability adapters. It does not yet clearly package
those pieces into broader runtime product layers.

**Gap:** the docs and architecture need to hold two truths at once: the runtime
is being shaped into a general foundation, and current maturity is still
coding-first. The failure mode is either underselling the broader shape or
overclaiming parity that the product has not earned yet.

**Target behavior:**

- Runtime kernel remains neutral and provider/tool mediated.
- Capability adapters remain optional packages owned by the host.
- Opinionated stacks become the place for domain defaults and batteries.
- Coding remains the first competitive stack, but not the only target shape.
- Public docs describe personal intelligence and managed cloud as intentional
  follow-on stacks built from the same kernel, not as domains already at the
  same maturity as coding.

**Eval coverage:**

- Not a behavior slice itself, but every stack-specific intelligence claim must
  still land with deterministic evals in the relevant domain.

### 1. Progressive Skill Disclosure

**Current state:** `skill.Selector` ranks skills. The SDK supports the original
direct-injection mode and an opt-in progressive mode where selected metadata is
shown in the prompt, full instructions are loaded through `load_skill`, and
supporting resources can be loaded through `read_skill_resource`. Progressive
metadata discovery has item-count and byte-budget defaults with eval coverage
for large catalogs and skill-search recovery when the initial metadata is
incomplete. Skill discovery, search, instruction load, and resource load now
emit dedicated events and counters.

**Gap:** Leading agents use skill metadata for discovery, then load full
instructions and resources on demand. The reference does this through a Skill
tool and filesystem-backed progressive disclosure.

**Target behavior:**

- Prompt includes skill metadata only: name, description, when-to-use, tags.
  Initial support exists, including default-bounded discovery for large catalogs
  by item count and prompt bytes.
- The model explicitly invokes `load_skill` to load full instructions. Initial
  support exists.
- Optional `read_skill_resource` loads host-owned supporting resources. Initial
  support exists.
- Full content enters the transcript as a tool result, not hidden prompt state.
  Initial support exists.
- Skill content can be cached per run/session with event visibility. Initial
  per-run loading exists through the skill loader.
- Omitted catalog entries remain reachable through explicit skill search.
  Initial eval coverage exists for metadata-only search-to-load recovery.
- Skill discovery, search, load, and resource-load operations are auditable.
  Initial event and metric coverage exists.
- Existing direct injection remains as a backward-compatible mode.

**Current API plus likely resource extension:**

```go
Options{
    SkillSource:         source,
    SkillResourceSource: resources,
    SkillDisclosure:     skill.DisclosureProgressive,
}

type ContentSource interface {
    SkillContent(context.Context, ContentRequest) (Content, error)
}

type ResourceSource interface {
    SkillResource(context.Context, ResourceRequest) (Resource, error)
}
```

**Eval coverage:**

- Metadata appears, full content does not. Initial coverage exists.
- Model invokes the right skill from metadata. Initial coverage exists.
- Loaded skill content is returned as a tool result and persists in transcript.
  Initial coverage exists.
- Resources are loaded only when requested. Initial coverage exists.
- Context retry preserves invoked skill content.
- Large skill catalogs stay within prompt budget.

### 2. Context Stack Hardening

**Current state:** recent-message, token-budget, summarizing policies, and
reactive context retry exist.

**Gap:** Leading agents maintain layered context: stable instructions, current
working set, tool results, summaries, memory, skill loads, resource handles, and
compaction provenance.

**Target behavior:**

- Named context bands with explicit retention rules.
- Summary messages carry source range/provenance metadata.
- Tool results can become handles rather than raw prompt text.
- Context policies preserve structural validity and important recovery state.
- Context retry can reselect tools, preserve loaded skills, and avoid repeated
  compaction failures.

**Eval coverage:**

- Long transcript compacts while preserving active task, recent tool errors, and
  loaded skill instructions. Initial loaded-skill preservation coverage exists.
- Summaries are not duplicated across retries. Initial compaction provenance and
  summary-replacement coverage exists.
- Result handles survive compaction. Initial policy-level coverage exists.
- Orphan tool results are never sent to providers.

### 3. Streaming Tool Execution

**Current state:** provider adapters expose tool-use start/delta/complete
events. The agent loop starts read-only, concurrency-safe tools as soon as the
complete validated tool call arrives, while keeping mutating tools serialized
and preserving existing transcript order.

**Gap:** Leading agents can begin safe tool execution as soon as complete tool
use blocks arrive while keeping mutating tools serialized.

**Target behavior:**

- Provider streams expose tool-use lifecycle information early enough for the
  agent loop to prepare execution while still validating complete inputs before
  running tools. Initial support exists.
- Read-only, concurrency-safe tools can start before the full assistant message
  finishes. Initial support exists.
- Mutating/destructive tools wait for safe ordering and policy checks.
- Events make early execution visible.
- Cancellation closes streams and in-flight safe tools cleanly.

**Eval coverage:**

- Safe tools overlap with streaming. Initial unit and eval coverage exists.
- Mutating tools preserve order. Initial eval coverage exists.
- Permission denial during streaming is model-visible. Initial eval coverage
  exists.
- Stream failure after early execution emits a paired cancellation result.
  Initial unit and eval coverage exists.
- Cancellation does not hang the event loop. Initial eval coverage exists.

### 4. Workspace and Checkpoint Model

**Current state:** file tools, source-neutral workspace contracts, workspace
tools, in-memory workspace state, root-confined OS-backed workspace state,
an initial git-backed checkpoint adapter for real repositories, guarded
structured patches, unified diffs, dry-run previews, patch review, diffs,
checkpoints, restore, host-owned verification tools, host-owned command
execution tools, initial managed command-session tools over host-owned session
interfaces, lifecycle events, model-visible rollback guidance after failed
verification, initial host-owned sandbox substrate adapters for workspace and
command backends, and eval coverage exist as optional packages.

**Gap:** A serious coding agent needs a workspace abstraction with diffs,
patches, snapshots, restore, reviewable mutations, and sandbox boundaries.

**Target behavior:**

- Optional `workspace` package with virtual filesystem interfaces.
- Patch/diff primitives separate from OS filesystem assumptions, including
  guarded structured operations, unified diff application, dry-run previews,
  actionable conflict diagnostics, compact patch summaries, and optional
  host review before mutation.
- Checkpoints can snapshot and restore host-owned workspace state. Initial
  git-backed checkpoint persistence now exists for real repositories through
  `workspace.GitStore`, while keeping git isolated to the adapter layer and
  preserving the same best-effort filesystem semantics as `workspace.OSStore`.
- Root-confined OS adapters contain symlinks by default while keeping the core
  workspace contract source-neutral.
- Verification is an explicit host-owned tool capability, not hidden shell
  access or an implicit SDK side effect.
- Command execution is an explicit host-owned runner capability with argv-only
  inputs, timeout/output caps, structured status metadata, and approval-summary
  support; the core SDK never gets implicit shell access.
- Longer-lived command sessions are explicit tools over host-owned lifecycle
  interfaces rather than hidden background shell state. Initial start/read/stop
  support exists, including a reference OS-backed managed-session adapter with
  rooted cwd resolution, bounded buffered output, explicit PTY terminal
  geometry, live resize, and session cleanup hooks.
- More isolated execution can stay outside the core loop through adapter seams
  that let hosts wire related sandbox-backed workspace, one-shot command, and
  managed command-session toolkits together.
- Command governance is expressed as hook-based policy presets: argv-prefix
  allow/deny rules, exact-input approval for selected commands, and
  verify-before-final gates after successful mutating commands.
- File tools emit structured metadata for modified paths and checkpoint IDs.
- CI/server examples use workspace adapters instead of raw OS assumptions.

**Eval coverage:**

- Edit failure rolls back via checkpoint.
- Unified diff conflict returns recoverable diagnostics and the model repairs
  the patch.
- Reviewed patch denial prevents mutation and gives the model a recoverable
  tool error.
- Workspace diff is available after a run.
- Verification failure returns diagnostics, the model repairs, and verification
  passes. Initial coverage exists.
- Command failure returns process diagnostics, the model repairs workspace state,
  and the command passes on rerun. Initial coverage exists.
- Managed command session output can drive a repair loop across turns, and the
  model can either read buffered output or interact through stdin writes before
  stopping or exiting the session explicitly after success. PTY-backed starts
  now cover shells and REPLs that require terminal behavior instead of plain
  pipes, and dedicated PTY resize coverage exists so terminal geometry is part
  of the eval contract. Initial coverage exists.
- Command approval denial drives an exact-input `request_approval` call and a
  single-use approved retry. Initial coverage exists.
- Command verification policy denial prevents finalization after a matching
  command until host verification passes. Initial coverage exists.
- Verification failure can drive checkpoint restore, including opt-in policy
  guidance that recommends the latest session checkpoint without restoring
  hiddenly. Initial coverage exists.
- Read-only policies prevent mutation.
- Symlink/path containment tests cover OS-backed adapters. Initial coverage
  exists.

### 5. Memory Lifecycle Maturity

**Current state:** memory source injection, memory tools, distillation
candidates, and optional candidate handlers exist.

**Gap:** Leading memory systems need provenance, confidence, contradiction
handling, privacy scopes, decay, consolidation, and host approval flows. The
competitive layer is concrete and near-term; contradiction handling, decay, and
semantic consolidation are leading aspirations that require additional
retrieval, usage, and review infrastructure.

**Target behavior:**

- Memory records carry source, confidence, timestamps, and policy metadata.
- Retrieval supports metadata filters and query/result provenance.
- Distillation supports update/delete/merge proposals, not just new memories.
- Hosts can run approval workflows before writes.
- Memory search tools can page and explain why results matched.
- Leading memory policies can later add contradiction review, decay, and
  consolidation once provenance and approval workflows are established.

**Eval coverage:**

- Stale memory is corrected instead of duplicated.
- User-scoped memory does not leak into project/organization scope.
- Low-confidence candidates are not persisted by default handlers.
- Contradictory candidates produce review events.

### 6. Planner and Task Policy Stack

**Current state:** planner policies, task-derived plans, plan-visible
verification hints, scoped subagent plan handoff, opt-in progress updates from
verification or subagent results, standard task-result metadata, and
durable SQLite-backed task storage exist. Eval-backed personal task-ledger
continuity across two runs exists. Initial
hook-based agent policy presets exist for checkpoint-before-patch recovery and
rollback guidance after failed verification, a verify-before-final gate for
workspace mutations, and explicit host approval tools/policies for sensitive
tool use.

**Gap:** The SDK lacks explicit policies for when to plan, update, delegate,
verify, ask the user, or stop.

**Target behavior:**

- Planner can expose actions: create task, update task, block, delegate, verify.
- Plan state is tied to tool results and checkpoint/memory evidence.
- Subagents can receive scoped plan steps and report structured progress.
- Verification is a first-class phase for coding workflows, expressed through
  host-owned tools rather than hidden runtime execution.

**Eval coverage:**

- Multi-step task updates plan after each tool result.
- Personal week-ahead planning writes follow-ups into durable task state, a
  later run reloads those tasks through planner context, and completed work is
  updated without duplicating the ledger. A sibling scenario reopens the
  SQLite-backed task store before the resume turn so durable task continuity is
  covered across fresh store instances, not only in-memory stores.
- Blocked plan asks for user input rather than hallucinating.
- Delegation happens only for scoped subtasks, and child results can return
  structured task progress.
- Verification hints guide the model to run host verification, verification
  outcomes update task progress when explicitly configured, and verification
  failure triggers repair.
- Checkpoint-before-patch policy denial drives checkpoint creation and retry
  through normal tool results.
- Rollback-on-failed-verification policy guidance drives explicit checkpoint
  restore through normal tool results.
- Verify-before-final policy denial prevents premature final answers after
  workspace mutation, drives verification through normal tool results, and
  stops with an expected error when the finalization denial budget is exhausted.
- Approval-before-tool policy denial drives explicit host approval requests and
  supports both approved retry and denied safe-fallback paths. Stricter
  single-use and input-bound approval modes cover exact-operation approval
  rather than only session-wide tool grants, with first-class approval events
  and metrics for audit/UI integration. Approval requests can carry structured
  host-facing summaries, including workspace patch summaries.
- Command policy presets add argv-prefix allow/deny rules, exact-input approval
  for selected commands, and verify-before-final gates for commands that mutate
  generated or dependency state.

### 7. Provider Fidelity and Compatibility

**Current state:** OpenAI and Anthropic adapters exist with streaming and usage
mapping.

**Gap:** Provider SDK conventions and edge cases must be exact.

**Target behavior:**

- OpenAI adapter follows OpenAI ecosystem conventions exactly.
- Anthropic adapter follows Anthropic ecosystem conventions exactly.
- Tests cover base URL semantics, endpoint overrides, stream deltas, tool-call
  fragments, usage deltas, provider errors, and cancellation.
- Provider-specific behavior stays outside core packages.

**Eval coverage:**

- HTTP fixture scenarios for every supported provider wire feature.
- Error payloads map to actionable SDK errors.
- Tool use round trips match provider wire contracts.

### 8. Eval Suite Upgrade

**Current state:** deterministic eval harness and baseline scenarios exist,
including an initial composed coding-loop scenario that ties planner context,
workspace mutation, managed command-session output, verification, and
finalization gating into one deterministic run.

**Gap:** Most evals still test feature plumbing; composed long-horizon
intelligence coverage is only starting to land.

**Target behavior:**

- Scenario suites for skills, context pressure, workspace edits, planner
  policy, memory lifecycle, subagent delegation, provider conformance, and
  budget-aware execution.
- CI example remains deterministic and fast.
- Live eval hooks exist but are clearly separate from unit CI.

**Eval coverage examples:**

- Skill progressive disclosure.
- Long coding task with failed edit and rollback.
- Misleading tool output recovery.
- Context compaction under pressure.
- Memory contradiction and correction.
- Delegated subagent with scoped tools.

## Immediate Next Milestones

1. Extend the initial sandbox adapters into fuller remote/container-backed
   execution backends, reusing the same source-neutral `workspace` contracts.
2. Add more workspace-oriented planner verification scenarios: patch,
   test/verify, repair, and checkpoint rollback across multiple files and
   longer horizons.
3. Add memory lifecycle proposals for update/delete/merge, not only new memory
   candidates.
4. Add provider-fidelity fixtures for error payloads, cancellation, and
   provider-specific tool edge cases.
5. Add more long-horizon eval scenarios that compose planner, workspace,
   skills, memory, budgets, and context compaction in one deterministic run.

## Definition Of Done For New Intelligence Features

A feature is not done until:

- Reference implementations were inspected.
- The Memax design either matches, adapts, or intentionally diverges with a
  documented rationale.
- The behavior is opt-in or backward compatible.
- Public APIs have doc comments.
- Prompt/event/session changes are covered by tests or golden snapshots.
- Deterministic eval scenarios prove the intended agent behavior.
- Performance risk on per-turn paths is bounded or benchmarked.
- Security and host-ownership implications are documented.
