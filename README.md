# Memax Agent SDK

Memax Agent SDK is a Go-native agent orchestration library inspired by modern autonomous coding agents and agent SDKs, but designed around application-owned tools instead of hard-coded system tools.

The core SDK should not assume access to the real filesystem, shell, browser, network, or OS permissions. Those capabilities are modeled as tools, and the tool implementation decides whether it talks to real infrastructure, a virtual filesystem, an in-memory workspace, a remote service, or a test fake.

## Current Status

This repository is in the planning and scaffold phase.

Implemented foundation:

- provider-neutral model streaming interfaces
- typed tool registry and executor
- compiled JSON Schema validation before tool execution
- permission checker seam
- in-memory and append-only JSONL session stores
- first autonomous query loop skeleton

Next implementation work is tracked in [docs/roadmap.md](docs/roadmap.md).
