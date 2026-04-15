# Memax Agent SDK

Memax Agent SDK is a Go-native agent orchestration library inspired by modern autonomous coding agents and agent SDKs, but designed around application-owned tools instead of hard-coded system tools.

The core SDK should not assume access to the real filesystem, shell, browser, network, or OS permissions. Those capabilities are modeled as tools, and the tool implementation decides whether it talks to real infrastructure, a virtual filesystem, an in-memory workspace, a remote service, or a test fake.

## Current Status

This repository is in the early foundation phase.

Implemented foundation:

- provider-neutral model streaming interfaces
- typed tool registry and executor
- compiled JSON Schema validation before tool execution
- per-tool result size limits with truncation metadata
- before/after tool lifecycle hooks
- permission checker seam
- in-memory and append-only JSONL session stores
- memory-backed file tools for examples and tests
- OpenAI Responses API model adapter
- context-window policy seam with recent-message limiting
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

Next implementation work is tracked in [docs/roadmap.md](docs/roadmap.md).
