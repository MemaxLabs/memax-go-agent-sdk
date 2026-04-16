# Server Embedding

The SDK is safe to embed in HTTP and WebSocket servers when each run is bounded
with contexts, timeouts, and concurrency limits.

Go does not have a single Node.js-style event loop thread. Each HTTP request is
served in its own goroutine. A blocking model call, filesystem call, database
query, or tool handler blocks that goroutine, not the whole process. Other HTTP
handlers and WebSocket pumps continue to run as long as the application does not
exhaust shared resources such as CPU, memory, file descriptors, database
connections, or outbound HTTP connections.

## Request Patterns

Use `Query` when synchronous startup is acceptable. `Query` creates or resumes
the session, runs prompt hooks, persists the user prompt, then returns an event
channel while the rest of the agent loop runs in a goroutine.

Use `QueryAsync` when even startup work should not block the caller goroutine.
Startup failures are emitted as `EventError` values on the returned channel.
If startup fails before a session is created, that error event has an empty
`SessionID`; multiplexed WebSocket/SSE servers should route that case through
their request-local state rather than session ID.

For request/response HTTP APIs, it is fine for the request handler to drain the
event channel. That blocks only that request goroutine. For WebSockets or SSE,
forward events to the socket as they arrive.

## Bounds To Set

- Set `Options.MaxRunDuration`.
- Set `Options.MaxTurns`.
- Set `Options.MaxToolConcurrency`.
- Use request-scoped contexts, for example `context.WithTimeout(r.Context(), ...)`.
- Use provider clients and HTTP clients with timeouts. The built-in OpenAI and
  Anthropic adapters provide `WithTimeout` and `WithHTTPClient` options.
- Use session stores with bounded connection pools.
- Wrap remote or slow skill sources with `skill.TimeoutSource`,
  `skill.CachedSource`, or `skill.PrefetchSource`.
- Wrap long-running tools with `tool.WithTimeout`.

## Skill Loading

Remote skill sources should not be loaded from scratch on every request in a
high-traffic server. Prefer one of these patterns:

```go
skills := &skill.PrefetchSource{
    Source: skill.HTTPSource{
        URL: "https://example.com/skills.json",
        Client: &http.Client{Timeout: 2 * time.Second},
    },
    TTL:            5 * time.Minute,
    RefreshTimeout: 2 * time.Second,
}

// Optional startup warmup.
_ = skills.Refresh(context.Background())
```

Once warm, `PrefetchSource` returns the last successful snapshot immediately.
When the snapshot expires, it starts one background refresh and returns stale
skills to request handlers until the refresh completes.

Use `CachedSource` when request handlers should wait for a fresh value at cache
miss or expiry. Use `PrefetchSource` when serving the last good snapshot is
better than making a request wait on remote skill IO.

## Tool Execution

The executor already runs concurrency-safe tool batches in goroutines, bounded
by `Options.MaxToolConcurrency`. Tools that are not concurrency-safe run
serially inside the agent run so state-mutating operations remain ordered.

`tool.WithTimeout` can bound a tool call:

```go
registry := tool.NewRegistry(
    tool.WithTimeout(filetools.NewReadTool(fs), 2*time.Second),
)
```

If a wrapped tool ignores context cancellation, the timeout wrapper returns to
the agent when the deadline expires, but the ignored work may continue in its
own goroutine until it returns. Tool implementations should still honor
`context.Context` for cleanup.

## Resource Isolation

For multi-tenant servers, consider adding host-level limits outside the SDK:

- a semaphore for total concurrent agent runs
- separate database pools for agent sessions and application traffic
- per-tenant tool registries and permission policies
- per-tenant context deadlines
- queueing for long-running background jobs
- streaming APIs for long-running foreground jobs
