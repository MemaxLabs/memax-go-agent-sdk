package verifytools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

func TestVerifyToolReturnsFailedResultAsToolError(t *testing.T) {
	verify := NewTool(Config{
		Verifier: VerifierFunc(func(_ context.Context, req Request) (Result, error) {
			if req.Name != "test" || req.Target != "./..." {
				t.Fatalf("request = %#v, want decoded check", req)
			}
			return Result{
				Name:   req.Name,
				Passed: false,
				Output: "go test failed",
				Diagnostics: []Diagnostic{{
					Path:     "main.go",
					Line:     12,
					Column:   3,
					Severity: "error",
					Message:  "undefined symbol",
				}},
			}, nil
		}),
	})
	result, err := verify.Execute(context.Background(), tool.Call{Use: model.ToolUse{
		ID:    "verify-1",
		Name:  ToolName,
		Input: json.RawMessage(`{"name":"test","target":"./..."}`),
	}})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("result = %#v, want failed verification as tool error", result)
	}
	for _, want := range []string{"verification test failed", "main.go:12:3", "undefined symbol", "go test failed"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("content = %q, want %q", result.Content, want)
		}
	}
	if result.Metadata[model.MetadataVerificationOperation] != "verify" ||
		result.Metadata[model.MetadataVerificationName] != "test" ||
		result.Metadata[model.MetadataVerificationPassed] != false ||
		result.Metadata[model.MetadataVerificationDiagnostics] != 1 {
		t.Fatalf("metadata = %#v, want verification metadata", result.Metadata)
	}
	paths, ok := result.Metadata[model.MetadataVerificationPaths].([]string)
	if !ok || len(paths) != 1 || paths[0] != "main.go" {
		t.Fatalf("paths metadata = %#v, want main.go", result.Metadata[model.MetadataVerificationPaths])
	}
}

func TestVerifyToolDefaultsAndMayMutate(t *testing.T) {
	verify := NewTool(Config{
		MayMutate: true,
		Verifier: VerifierFunc(func(_ context.Context, req Request) (Result, error) {
			if req.Name != defaultName {
				t.Fatalf("request name = %q, want default", req.Name)
			}
			return Result{Passed: true}, nil
		}),
	})
	spec := verify.Spec()
	if spec.ReadOnly || !spec.Destructive {
		t.Fatalf("spec = %#v, want mutating verification gated as destructive", spec)
	}
	result, err := verify.Execute(context.Background(), tool.Call{Use: model.ToolUse{ID: "verify-1", Name: ToolName}})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.IsError || !strings.Contains(result.Content, "verification default passed") {
		t.Fatalf("result = %#v, want passing default verification", result)
	}
}
