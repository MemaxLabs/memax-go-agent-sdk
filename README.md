# Memax Agent SDK

Memax Agent SDK is a Go-native agent orchestration library inspired by modern autonomous coding agents and agent SDKs, but designed around application-owned tools instead of hard-coded system tools.

The core SDK should not assume access to the real filesystem, shell, browser, network, or OS permissions. Those capabilities are modeled as tools, and the tool implementation decides whether it talks to real infrastructure, a virtual filesystem, an in-memory workspace, a remote service, or a test fake.

## Current Status

This repository is production-embeddable and moving into advanced autonomy work.

Implemented foundation:

- provider-neutral model streaming interfaces
- typed tool registry and executor
- compiled JSON Schema validation before tool execution
- per-tool result size limits with truncation metadata
- tool and session lifecycle hooks
- structured permission policies with host approval callbacks
- in-memory and append-only JSONL session stores
- resumable and forkable sessions
- checkpoint manager interfaces and checkpoint tools
- memory-backed file tools for examples and tests
- bounded subagent tool with parent/child session correlation
- task state tools for agent planning and progress tracking
- opt-in tool selection and search for deferred tool loading
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

To use the OpenAI adapter:

```go
client := openai.NewFromEnv("gpt-5")
events, err := memaxagent.Query(ctx, "Inspect the workspace.", memaxagent.Options{
    Model: client,
    Tools: registry,
})
```

To use the Anthropic adapter:

```go
client := anthropic.NewFromEnv("your-anthropic-model")
events, err := memaxagent.Query(ctx, "Inspect the workspace.", memaxagent.Options{
    Model: client,
    Tools: registry,
})
```

To emit OpenTelemetry spans, import `github.com/MemaxLabs/memax-go-agent-sdk/otel` as `sdkotel`:

```go
events, err := memaxagent.Query(ctx, "Inspect the workspace.", memaxagent.Options{
    Model:  client,
    Tools:  registry,
    Tracer: sdkotel.NewTracer("my-agent-service"),
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

Next implementation work is tracked in [docs/roadmap.md](docs/roadmap.md).
