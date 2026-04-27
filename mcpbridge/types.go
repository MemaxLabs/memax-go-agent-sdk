package mcpbridge

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	defaultProtocolVersion = "2024-11-05"
	toolNamePrefix         = "mcp"
)

var toolNameSanitizer = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// ServerConfig configures a single MCP server.
type ServerConfig struct {
	// Name is the stable local server name used in model-facing tool names.
	Name string
	// Command is the executable for stdio MCP servers.
	Command string
	// Args are passed to Command.
	Args []string
	// Env adds or overrides environment variables for the server process.
	Env map[string]string
	// CWD sets the server process working directory.
	CWD string
	// SupportsParallelToolCalls marks every discovered server tool as eligible
	// for concurrent execution. Leave false unless the server's tools are known
	// to be safe for parallel calls.
	SupportsParallelToolCalls bool
	// ProtocolVersion is sent in initialize. Empty uses a conservative default.
	ProtocolVersion string
}

// Validate checks whether cfg can start a stdio MCP server.
func (cfg ServerConfig) Validate() error {
	if normalizeName(cfg.Name) == "" {
		return fmt.Errorf("mcp server name is required")
	}
	if strings.TrimSpace(cfg.Command) == "" {
		return fmt.Errorf("mcp server %q command is required", cfg.Name)
	}
	return nil
}

func (cfg ServerConfig) protocolVersion() string {
	if version := strings.TrimSpace(cfg.ProtocolVersion); version != "" {
		return version
	}
	return defaultProtocolVersion
}

func normalizeName(name string) string {
	name = strings.TrimSpace(name)
	name = toolNameSanitizer.ReplaceAllString(name, "_")
	name = strings.Trim(name, "_-")
	return name
}

func modelFacingToolName(serverName, remoteName string) string {
	serverName = normalizeName(serverName)
	remoteName = normalizeName(remoteName)
	if serverName == "" || remoteName == "" {
		return ""
	}
	return toolNamePrefix + "__" + serverName + "__" + remoteName
}
