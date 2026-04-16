package verifytools

import (
	"context"
	"fmt"
	"strings"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

const (
	// ToolName is the default tool name for host-owned workspace verification.
	ToolName    = "workspace_verify"
	defaultName = "default"
)

// Request describes one verification check requested by the model. Name is a
// host-defined check identifier such as "test", "lint", "typecheck", or
// "policy". Target is optional and can scope the check to a package, path, or
// other host-owned selector.
type Request struct {
	Name     string
	Target   string
	Metadata map[string]any
}

// Diagnostic is one actionable verification finding.
type Diagnostic struct {
	Path     string
	Line     int
	Column   int
	Severity string
	Message  string
}

// Result is returned by a Verifier. Failed results are surfaced to the model as
// tool error results, not caller errors, so the agent can repair and retry.
type Result struct {
	Name        string
	Passed      bool
	Output      string
	Diagnostics []Diagnostic
	Metadata    map[string]any
}

// Verifier is implemented by hosts that can run tests, typechecks, linters,
// policy checks, or other validation against application-owned state.
type Verifier interface {
	Verify(context.Context, Request) (Result, error)
}

// VerifierFunc adapts a function into a Verifier.
type VerifierFunc func(context.Context, Request) (Result, error)

func (f VerifierFunc) Verify(ctx context.Context, req Request) (Result, error) {
	if f == nil {
		return Result{}, fmt.Errorf("verifytools: verifier is required")
	}
	return f(ctx, req)
}

// Config configures NewTool.
type Config struct {
	Verifier        Verifier
	Name            string
	Description     string
	SearchHint      string
	MayMutate       bool
	ConcurrencySafe bool
	MaxResultBytes  int
}

// NewTool returns a verification tool over a host-owned Verifier. The tool is
// read-only by default because it models validation, but hosts that run checks
// with side effects such as build-cache writes can set MayMutate true and rely
// on normal permission policies.
func NewTool(config Config) tool.Tool {
	name := strings.TrimSpace(config.Name)
	if name == "" {
		name = ToolName
	}
	description := strings.TrimSpace(config.Description)
	if description == "" {
		description = "Run a host-owned verification check such as tests, typecheck, lint, or policy validation."
	}
	searchHint := strings.TrimSpace(config.SearchHint)
	if searchHint == "" {
		searchHint = "verify workspace test typecheck lint validation"
	}
	maxResultBytes := config.MaxResultBytes
	if maxResultBytes == 0 {
		maxResultBytes = 64 * 1024
	}
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:            name,
			Description:     description,
			SearchHint:      searchHint,
			ReadOnly:        !config.MayMutate,
			Destructive:     config.MayMutate,
			ConcurrencySafe: config.ConcurrencySafe,
			MaxResultBytes:  maxResultBytes,
			InputSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"name": map[string]any{
						"type":        "string",
						"description": "Host-defined verification check name, for example test, lint, typecheck, or policy.",
					},
					"target": map[string]any{
						"type":        "string",
						"description": "Optional host-defined target such as a package, file path, or workspace area.",
					},
					"metadata": map[string]any{
						"type":        "object",
						"description": "Optional host-defined verification metadata.",
					},
				},
			},
		},
		Handler: func(ctx context.Context, call tool.Call) (model.ToolResult, error) {
			if config.Verifier == nil {
				return model.ToolResult{}, fmt.Errorf("verifytools: verifier is required")
			}
			input, err := tool.DecodeInput[input](call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			req := Request{
				Name:     strings.TrimSpace(input.Name),
				Target:   strings.TrimSpace(input.Target),
				Metadata: model.CloneMetadata(input.Metadata),
			}
			if req.Name == "" {
				req.Name = defaultName
			}
			result, err := config.Verifier.Verify(ctx, req)
			if err != nil {
				return model.ToolResult{}, err
			}
			if strings.TrimSpace(result.Name) == "" {
				result.Name = req.Name
			}
			paths := diagnosticPaths(result.Diagnostics)
			metadata := model.CloneMetadata(result.Metadata)
			if metadata == nil {
				metadata = map[string]any{}
			}
			metadata[model.MetadataVerificationOperation] = "verify"
			metadata[model.MetadataVerificationName] = result.Name
			metadata[model.MetadataVerificationPassed] = result.Passed
			metadata[model.MetadataVerificationDiagnostics] = len(result.Diagnostics)
			metadata[model.MetadataVerificationPaths] = paths
			return model.ToolResult{
				Content:  formatResult(result),
				IsError:  !result.Passed,
				Metadata: metadata,
			}, nil
		},
	}
}

type input struct {
	Name     string         `json:"name"`
	Target   string         `json:"target"`
	Metadata map[string]any `json:"metadata"`
}

func formatResult(result Result) string {
	status := "failed"
	if result.Passed {
		status = "passed"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "verification %s %s", result.Name, status)
	if len(result.Diagnostics) > 0 {
		b.WriteString("\nDiagnostics:")
		for _, diagnostic := range result.Diagnostics {
			b.WriteString("\n- ")
			if diagnostic.Path != "" {
				b.WriteString(diagnostic.Path)
				if diagnostic.Line > 0 {
					fmt.Fprintf(&b, ":%d", diagnostic.Line)
					if diagnostic.Column > 0 {
						fmt.Fprintf(&b, ":%d", diagnostic.Column)
					}
				}
				b.WriteString(": ")
			}
			if diagnostic.Severity != "" {
				b.WriteString(diagnostic.Severity)
				b.WriteString(": ")
			}
			b.WriteString(diagnostic.Message)
		}
	}
	output := strings.TrimSpace(result.Output)
	if output != "" {
		b.WriteString("\nOutput:\n")
		b.WriteString(output)
	}
	return b.String()
}

func diagnosticPaths(diagnostics []Diagnostic) []string {
	seen := map[string]bool{}
	var paths []string
	for _, diagnostic := range diagnostics {
		path := strings.TrimSpace(diagnostic.Path)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		paths = append(paths, path)
	}
	return paths
}
