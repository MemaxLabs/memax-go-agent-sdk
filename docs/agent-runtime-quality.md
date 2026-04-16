# Agent Runtime Quality Plan

This document is the standing gap analysis for evolving Memax Agent SDK from a
solid orchestration SDK into a competitive agent runtime. The target is not to
copy any one product. The target is to match or exceed the maturity of leading
coding agents while preserving the SDK's core constraints: Go-native,
provider-neutral, filesystem-neutral, host-owned tools, and embeddable server
safety.

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

| Subsystem | Current State | Target |
| --- | --- | --- |
| Agent loop | Foundation | Competitive |
| Provider adapters | Foundation | Competitive |
| Tool contract | Competitive foundation | Leading |
| Tool execution scheduling | Foundation | Competitive |
| Permissions and hooks | Competitive foundation | Competitive |
| Sessions and resume | Competitive foundation | Competitive |
| Context management | Foundation | Competitive |
| Skills | Foundation | Competitive |
| Memory | Foundation | Competitive |
| Planner and tasks | Foundation | Competitive |
| Subagents | Foundation | Competitive |
| Workspace and checkpoints | Foundation | Competitive |
| Budgets and usage | Competitive foundation | Competitive |
| Evals | Strong foundation | Leading |
| Observability | Competitive foundation | Competitive |

`Tool contract` targets Leading only in a narrow sense: the interface should
stay minimal and host-owned while supporting production-grade lifecycle
behavior such as cancellation, streaming result visibility, result handles,
schema safety, telemetry, and policy composition. It does not mean broadening
the core SDK into a built-in filesystem, shell, browser, or operating
environment.

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

## Priority Execution Plan

### 1. Progressive Skill Disclosure

**Current state:** `skill.Selector` ranks skills. The SDK supports the original
direct-injection mode and an opt-in progressive mode where selected metadata is
shown in the prompt, full instructions are loaded through `load_skill`, and
supporting resources can be loaded through `read_skill_resource`. Progressive
metadata discovery has item-count and byte-budget defaults with eval coverage
for large catalogs and skill-search recovery when the initial metadata is
incomplete.

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

**Current state:** file tools and checkpoint metadata exist as optional toolkit
packages.

**Gap:** A serious coding agent needs a workspace abstraction with diffs,
patches, snapshots, restore, reviewable mutations, and sandbox boundaries.

**Target behavior:**

- Optional `workspace` package with virtual filesystem interfaces.
- Patch/diff primitives separate from OS filesystem assumptions.
- Checkpoints can snapshot and restore host-owned workspace state.
- File tools emit structured metadata for modified paths and checkpoint IDs.
- CI/server examples use workspace adapters instead of raw OS assumptions.

**Eval coverage:**

- Edit failure rolls back via checkpoint.
- Workspace diff is available after a run.
- Read-only policies prevent mutation.
- Symlink/path containment tests cover OS-backed adapters.

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

**Current state:** planner policies and task-derived plans exist.

**Gap:** The SDK lacks explicit policies for when to plan, update, delegate,
verify, ask the user, or stop.

**Target behavior:**

- Planner can expose actions: create task, update task, block, delegate, verify.
- Plan state is tied to tool results and checkpoint/memory evidence.
- Subagents can receive scoped plan steps and report structured progress.
- Verification is a first-class phase for coding workflows.

**Eval coverage:**

- Multi-step task updates plan after each tool result.
- Blocked plan asks for user input rather than hallucinating.
- Delegation happens only for scoped subtasks.
- Verification failure triggers repair.

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

**Current state:** deterministic eval harness and baseline scenarios exist.

**Gap:** Most evals test feature plumbing, not long-horizon intelligence.

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

1. Commit provider URL semantic cleanup separately from strategy documents.
2. Commit AGENTS and runtime-quality documentation separately from provider
   behavior changes.
3. Add optional skill resource loading for progressive disclosure.
4. Add context-stack evals that include loaded skills and result handles.
5. Add larger-catalog budget tests for progressive skill prompts.
6. Begin workspace abstraction design only after skill progressive disclosure is
   stable, because the same resource-loading pattern should inform workspace
   resource handling.

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
