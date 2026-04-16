# Memax Agent SDK

Memax Agent SDK is a Go-native agent orchestration library inspired by modern autonomous coding agents and agent SDKs, but designed around application-owned tools instead of hard-coded system tools.

The core SDK should not assume access to the real filesystem, shell, browser, network, or OS permissions. Those capabilities are modeled as tools, and the tool implementation decides whether it talks to real infrastructure, a virtual filesystem, an in-memory workspace, a remote service, or a test fake.

## Current Status

This repository is production-embeddable and moving through DX polish.

Implemented foundation:

- provider-neutral model streaming interfaces
- typed tool registry and executor
- compiled JSON Schema validation before tool execution
- per-tool result size limits with truncation metadata
- host-owned storage for oversized tool results with preview handles
- tool and session lifecycle hooks
- structured permission policies with host approval callbacks
- in-memory and append-only JSONL session stores
- resumable and forkable sessions
- checkpoint manager interfaces and checkpoint tools
- memory-backed, OS-backed, and `io/fs`-backed file tools for examples and tests
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
- skill discovery tools
- OpenAI Responses API model adapter
- Anthropic Messages API model adapter
- context-window policies for recent-message limiting, token budgets, and summarizing compaction
- optional OpenTelemetry tracing adapter
- first autonomous query loop skeleton

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
go run ./examples/ci_embedding
go run ./examples/skills_identity
go run ./examples/eval_scenarios
```

`session_resume` shows how to continue a durable transcript by passing `Options.SessionID`. `advanced_stack` composes task state, checkpointing, context budgeting, tool search, and memory-backed file tools in one run. `ci_embedding` shows a bounded, read-only agent run shaped for CI jobs. `skills_identity` shows how an agent profile and relevant skills become deterministic prompt guidance. `eval_scenarios` runs the deterministic autonomy scenario suite and exits non-zero on failure.

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
`toolkit/skilltools`:

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
        }},
    }),
})
```

Planner policies receive the active session ID, parent session ID, identity,
messages, and recent user-query text. The default prompt builder injects the
returned plan as the named `memax.plan` prompt part.

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
    ),
})
```

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
