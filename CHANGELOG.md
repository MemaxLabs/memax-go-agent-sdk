# Changelog

All notable changes to Memax Agent SDK will be documented in this file.

This project follows semantic versioning before and after the first stable
release. Until `v1.0.0`, minor versions may include API changes, but breaking
changes should still be called out here with migration notes.

## Unreleased

- Added Phase 4 embedding examples for local, CI, scripted server, and live
  provider server usage.
- Added golden event-stream compatibility coverage for a tool-using query run.
- Added root-confined `OSFS` and read-only `io/fs.FS` file workspace adapters.
- Added OSFS hardening options for symlink containment, read-size limits,
  list-entry limits, and file/directory modes.
- Added SDK-owned metrics interfaces with optional OpenTelemetry adaptation.
- Added SQLite-backed session store adapter.
- Added agent identity profiles, deterministic prompt assembly, local skill
  manifests, and skill discovery tools.
- Added source-neutral skill loaders for function-backed, merged, cached, HTTP,
  host filesystem, and standard `fs.FS` sources.
- Hardened cached skill loading to deduplicate concurrent refreshes.
- Added `QueryAsync`, `skill.TimeoutSource`, `skill.PrefetchSource`, tool
  timeout wrappers, and server embedding guidance for HTTP/WebSocket services.
- Added prompt golden tests, prompt/skill/server benchmarks, provider prompt
  profiles, skill policy filtering, and one-shot context-window retry support.
- Added CI workflow, versioning policy, and public API compatibility guidance.
