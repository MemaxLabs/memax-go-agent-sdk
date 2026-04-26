<p align="center">
  <a href="https://github.com/MemaxLabs/memax-go-agent-sdk">
    <img src="https://memax.app/images/memax-wordmark.svg" alt="Memax" width="320" />
  </a>
</p>

<h1 align="center">Memax Agent SDK</h1>

<p align="center">
  A Go SDK for autonomous agents with application-owned tools and host-controlled policy.
</p>

<p align="center">
  <a href="./LICENSE"><img alt="License: Apache-2.0" src="https://img.shields.io/badge/license-Apache%202.0-blue.svg"></a>
  <img alt="Go version" src="https://img.shields.io/badge/go-1.24+-00ADD8?logo=go&logoColor=white">
</p>

Memax Agent SDK is a Go SDK for building autonomous agents with
**application-owned tools**, **host-controlled policy**, and **provider-neutral**
orchestration.

It is designed for teams that want agent behavior without hard-coding access to
the local filesystem, shell, browser, network, inbox, calendar, or other OS
capabilities into the runtime. In Memax, those capabilities are exposed as
**tools** owned by the host application.

The result is an agent runtime that is easier to embed, test, secure, observe,
and adapt across different products and environments.

## Why Memax Agent SDK

Most agent systems couple orchestration tightly to a fixed environment. Memax is
built around a different model:

- **Tools are owned by the host application.** The SDK does not assume direct
  access to files, commands, web, or external services.
- **Policies stay outside the core runtime.** Approval, permissions, budgets,
  checkpoints, storage, and tool implementations remain application decisions.
- **Providers are replaceable.** Model integrations are exposed through
  provider-neutral interfaces, with support for multiple providers.
- **Sessions are durable and inspectable.** The runtime is designed for
  resumable work, event streaming, and host-side observability.
- **Stacks are composable.** You can build from the low-level runtime or start
  from higher-level presets for coding, personal-assistant, and managed-worker
  workflows.

## What the SDK includes

### Runtime kernel

The core runtime provides the orchestration primitives needed to run an agent in
an application-controlled environment:

- provider-neutral model streaming interfaces
- session lifecycle management and append-only persistence
- resumable and forkable sessions
- context-window policies, summarization, and compaction
- structured tool registry and execution
- JSON Schema validation for tool inputs and structured outputs
- permission hooks and approval callbacks
- task state, planner integration, and bounded subagents
- run budgets for turns, model calls, tool calls, tokens, and duration
- usage events, traces, and observability hooks

### Capability adapters and toolkits

The repository also includes host-owned toolkits and adapters for common agent
capabilities, including:

- workspace, patch, diff, restore, and checkpoint operations
- command execution and verification backends
- web search and fetch contracts
- memory and result storage integration
- scheduling, messaging, notes, and telemetry support

### Opinionated stacks

For faster adoption, the SDK includes reusable stacks built on the same runtime:

- **`stack/coding`** for coding-agent workflows
- **`stack/personal`** for personal-assistant and knowledge workflows
- **`stack/cloudmanaged`** for managed multi-tenant worker workflows

These stacks provide pre-wired policies, tools, and presets while preserving the
same host-owned execution model.

## Current status

Memax Agent SDK is usable today and strongest in **coding-agent orchestration**.
The broader platform shape already includes personal and managed-cloud stacks,
but those areas are still maturing.

If you are evaluating the project today, the safest framing is:

- the **runtime kernel is real and embeddable**
- the **coding stack is the most mature path**
- the **personal and cloud-managed stacks are active expansion areas**

## Key features

- Go-native autonomous agent runtime
- application-owned tool execution model
- OpenAI and Anthropic model adapters
- resumable sessions and append-only transcript storage
- structured permissions and approval callbacks
- bounded subagent execution with parent/child session correlation
- deterministic evaluation harness for orchestration scenarios
- coding workflow presets such as `safe_local`, `ci_repair`, and
  `interactive_dev`
- personal workflow presets such as `personal_assistant` and
  `research_partner`
- managed-worker building blocks for queues, quotas, run stores, and remote
  workers
- optional OpenTelemetry integration

## Installation

Requires **Go 1.24+**.

Add the SDK to your project:

```sh
go get github.com/MemaxLabs/memax-go-agent-sdk@latest
```

Or clone the repository and build from source:

```sh
git clone https://github.com/MemaxLabs/memax-go-agent-sdk.git
cd memax-go-agent-sdk
go test ./...
```

## Real-world example

A production-facing example built on top of this SDK is **[Memax Code](https://github.com/MemaxLabs/memax-code)**,
the Memax coding-agent CLI. It uses the SDK as its runtime foundation while
adding CLI ergonomics, session UX, rendering, local configuration, and
product-focused defaults for coding workflows.

## Getting started

There are two common ways to adopt the SDK:

### 1. Start from a stack preset

If you want a batteries-included starting point, begin with one of the stack
packages and attach your own backends.

Example shape using the coding stack:

```go
cfg := coding.CIRepair()
cfg.Workspace = workspaceStore
cfg.Verifier.Verifier = verifier
cfg.Command.Runner = runner

stack, err := coding.New(cfg)
if err != nil {
    panic(err)
}
```

See the examples under [`examples/`](./examples) for end-to-end setup.

### 2. Embed the runtime directly

If you need full control, build on the core runtime and register only the tools,
policies, stores, and hooks your application wants to expose.

The root package centers on `Query` and `QueryAsync`, which drive a session,
stream model events, execute requested tools through the registered tool
registry, and continue until the run reaches a final result or stop condition.

## Repository guide

### Core packages

- `agent.go` — main runtime query loop
- `session/` — session storage and lifecycle
- `tool/` — tool registry and execution contracts
- `planner/` — planner integration and task state wiring
- `permission/` — permission policies and approval hooks
- `prompt/`, `identity/`, `skill/` — prompt assembly and agent identity support
- `model/`, `providers/` — provider-neutral model interfaces and adapters

### Stacks

- `stack/coding/` — coding-agent workflow assembly and presets
- `stack/personal/` — personal-assistant workflow assembly and presets
- `stack/cloudmanaged/` — managed multi-tenant worker assembly and stores

### Toolkits and adapters

- `workspace/`, `checkpoint/`, `toolkit/`, `web/`, `memory/`, `messaging/`,
  `notes/`, `scheduling/`, `telemetry/`

### Examples

Representative examples include:

- [`examples/coding_stack`](./examples/coding_stack)
- [`examples/coding_safe_local_stack`](./examples/coding_safe_local_stack)
- [`examples/personal_stack`](./examples/personal_stack)
- [`examples/cloudmanaged_remote_stack`](./examples/cloudmanaged_remote_stack)
- [`examples/server_embedding`](./examples/server_embedding)

## Documentation

Project documentation lives in [`docs/`](./docs):

- [Architecture](./docs/architecture.md)
- [Server integration](./docs/server.md)
- [Coding stack presets](./docs/coding-stack-presets.md)
- [Personal stack presets](./docs/personal-stack-presets.md)
- [Observability](./docs/observability.md)
- [Skills](./docs/skills.md)
- [Roadmap](./docs/roadmap.md)
- [Versioning](./docs/versioning.md)

## Development

Run the test suite:

```sh
go test ./...
```

Explore runnable examples:

```sh
go run ./examples/coding_stack
```

Some examples require provider credentials or host-owned backends to be
configured before they can run successfully.

## Design principles

Memax is intentionally designed around a few core principles:

- **host ownership over environment access**
- **clear separation between runtime and capabilities**
- **durable sessions and observable execution**
- **composable stacks instead of a single fixed product surface**
- **policy as configuration and integration, not hidden behavior**

## Open source status

This repository is being developed in the open as the foundation for a broader
agent platform. APIs and higher-level stacks will continue to evolve, but the
project is intended to be useful today for embedders who want a Go-native agent
runtime with explicit control over tools and policy.

## License

This project is licensed under the **Apache License 2.0**. See [LICENSE](./LICENSE).
