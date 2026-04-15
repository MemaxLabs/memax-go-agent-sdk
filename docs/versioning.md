# Versioning and Compatibility

Memax Agent SDK uses semantic versioning.

## Before v1.0.0

- Minor versions may include API changes while the SDK is still stabilizing.
- Breaking changes must be documented in `CHANGELOG.md`.
- Public examples should be updated in the same change as any API adjustment.
- Core contracts should stay stable unless the replacement is clearly better:
  `model.Client`, `model.Stream`, `tool.Tool`, `session.Store`,
  `permission.Checker`, `hook.Runner`, `contextwindow.Policy`, and
  `telemetry.Tracer`/`telemetry.Meter`.

## v1.0.0 and Later

The following are treated as public API and should not break in minor or patch
releases:

- exported package paths
- exported type, function, method, field, and constant names
- interface method sets
- JSONL transcript compatibility
- query event ordering for documented event streams
- provider-neutral model message semantics
- tool execution and permission behavior

Breaking changes after `v1.0.0` require a major version bump.

## Compatibility Tests

The SDK should keep golden tests for public event streams and transcript formats.
When intentional behavior changes occur, update the golden files in the same
commit and document the compatibility impact in `CHANGELOG.md`.

## Release Checklist

1. Run `go test ./...`.
2. Run `go vet ./...`.
3. Run `go test -race ./...`.
4. Run deterministic examples:
   `memory_tools`, `session_resume`, `advanced_stack`, and `ci_embedding`.
5. Update `CHANGELOG.md`.
6. Tag the release with `git tag vX.Y.Z`.
7. Push the tag after CI passes.
