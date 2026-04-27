// Package mcpbridge adapts Model Context Protocol servers into normal Memax
// tools.
//
// The package owns provider-neutral MCP protocol plumbing: server startup,
// JSON-RPC request/response exchange, tools/list discovery, and tools/call
// execution. Product surfaces such as CLI config commands, status rendering,
// credentials, and user-facing approval prompts should live above this package.
//
// This initial bridge supports stdio MCP servers. It intentionally exposes MCP
// tools through the existing tool.Tool interface so tenant validation,
// permissions, hooks, result truncation, telemetry, and context cancellation
// remain centralized in the SDK executor.
package mcpbridge
