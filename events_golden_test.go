package memaxagent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/budget"
	"github.com/MemaxLabs/memax-go-agent-sdk/contextwindow"
	"github.com/MemaxLabs/memax-go-agent-sdk/memory"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/skill"
	"github.com/MemaxLabs/memax-go-agent-sdk/tenant"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/commandtools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/skilltools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/verifytools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/workspacetools"
	"github.com/MemaxLabs/memax-go-agent-sdk/workspace"
)

func TestQueryEventStreamGolden(t *testing.T) {
	registry := tool.NewRegistry(tool.Definition{
		ToolSpec: model.ToolSpec{Name: "read", ReadOnly: true, ConcurrencySafe: true},
		Handler: func(context.Context, tool.Call) (model.ToolResult, error) {
			return model.ToolResult{Content: "content from README.md"}, nil
		},
	})
	events, err := Query(context.Background(), "read the file", Options{
		Model: &fakeModel{turns: [][]model.StreamEvent{
			{
				{
					Kind: model.StreamToolUse,
					ToolUse: model.ToolUse{
						ID:    "tool-1",
						Name:  "read",
						Input: json.RawMessage(`{"path":"README.md"}`),
					},
				},
			},
			{{Kind: model.StreamText, Text: "done"}},
		}},
		Tools: registry,
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}

	var got []goldenEvent
	for event := range events {
		if event.Kind == EventError {
			t.Fatalf("query error: %v", event.Err)
		}
		got = append(got, normalizeGoldenEvent(event))
	}

	data, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("marshal golden events: %v", err)
	}
	data = append(data, '\n')

	want, err := os.ReadFile("testdata/golden/basic_event_stream.json")
	if err != nil {
		t.Fatalf("read golden file: %v", err)
	}
	if strings.TrimSpace(string(data)) != strings.TrimSpace(string(want)) {
		t.Fatalf("event stream golden mismatch\n got:\n%s\nwant:\n%s", data, want)
	}
}

func TestQueryObservabilityEventStreamGolden(t *testing.T) {
	skills := skill.StaticSource{{
		Name:        "database-review",
		Description: "SQL migration review.",
		AlwaysOn:    true,
		Content:     "Check rollback safety before approving database changes.",
		Resources: []skill.ResourceRef{{
			Name:        "migration-checklist",
			Description: "Migration checklist.",
			Path:        "resources/migration-checklist.md",
		}},
	}}
	resources := skill.StaticResourceSource{{
		SkillName: "database-review",
		Name:      "migration-checklist",
		Path:      "resources/migration-checklist.md",
		Content:   "Confirm rollback plan and lock timeout.",
	}}
	searchTool, err := skilltools.NewSearchTool(skilltools.Config{Source: skills})
	if err != nil {
		t.Fatalf("NewSearchTool returned error: %v", err)
	}
	events, err := Query(context.Background(), "review SQL migration", Options{
		Model: &fakeModel{turns: [][]model.StreamEvent{
			{
				{
					Kind: model.StreamToolUseStart,
					ToolUse: model.ToolUse{
						ID:   "search-1",
						Name: "search_skills",
					},
				},
				{
					Kind:         model.StreamToolUseDelta,
					ToolUse:      model.ToolUse{ID: "search-1", Name: "search_skills"},
					ToolUseDelta: `{"query":"SQL`,
				},
				{
					Kind:         model.StreamToolUseDelta,
					ToolUse:      model.ToolUse{ID: "search-1", Name: "search_skills"},
					ToolUseDelta: ` migration","limit":1}`,
				},
				{
					Kind: model.StreamToolUse,
					ToolUse: model.ToolUse{
						ID:    "search-1",
						Name:  "search_skills",
						Input: json.RawMessage(`{"query":"SQL migration","limit":1}`),
					},
				},
			},
			{{
				Kind: model.StreamToolUse,
				ToolUse: model.ToolUse{
					ID:    "skill-1",
					Name:  skill.LoadToolName,
					Input: json.RawMessage(`{"name":"database-review"}`),
				},
			}},
			{{
				Kind: model.StreamToolUse,
				ToolUse: model.ToolUse{
					ID:    "resource-1",
					Name:  skill.ResourceToolName,
					Input: json.RawMessage(`{"skill_name":"database-review","resource":"migration-checklist"}`),
				},
			}},
			{
				{
					Kind: model.StreamUsage,
					Usage: &model.Usage{
						InputTokens:  7,
						OutputTokens: 5,
						TotalTokens:  12,
						Provider:     "scripted",
						Model:        "golden",
					},
				},
				{Kind: model.StreamText, Text: "rollback notes added"},
			},
		}},
		Tools:               tool.NewRegistry(searchTool),
		Context:             &oneShotCompactionPolicy{},
		SkillSource:         skills,
		SkillResourceSource: resources,
		SkillDisclosure:     skill.DisclosureProgressive,
		MemoryDistiller: memory.StaticDistiller{{
			Memory: memory.Memory{
				Name:    "migration-rollback",
				Scope:   memory.ScopeProject,
				Content: "Migration reviews require rollback notes.",
			},
			Reason:     "final answer confirmed rollback notes",
			Confidence: 0.9,
		}},
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}

	var got []goldenEvent
	for event := range events {
		if event.Kind == EventError {
			t.Fatalf("query error: %v", event.Err)
		}
		got = append(got, normalizeGoldenEvent(event))
	}

	data, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("marshal golden events: %v", err)
	}
	data = append(data, '\n')

	want, err := os.ReadFile("testdata/golden/observability_event_stream.json")
	if err != nil {
		t.Fatalf("read golden file: %v", err)
	}
	if strings.TrimSpace(string(data)) != strings.TrimSpace(string(want)) {
		t.Fatalf("observability event stream golden mismatch\n got:\n%s\nwant:\n%s", data, want)
	}
}

func TestQueryCommandSessionEventStreamGolden(t *testing.T) {
	manager := commandtools.NewScriptedSessionManager(commandtools.ScriptedCommand{
		ID:   "server-1",
		PID:  4242,
		TTY:  true,
		Cols: 100,
		Rows: 30,
		WritePages: []commandtools.ScriptedWritePage{{
			Page: commandtools.ScriptedOutputPage{
				Chunks: []commandtools.OutputChunk{{
					Seq:    1,
					Stream: "pty",
					Text:   "echo:hello\n",
				}},
				Running: true,
			},
		}},
		StopExitCode: intPtr(143),
	})
	events, err := Query(context.Background(), "start and stop the server", Options{
		Model: &fakeModel{turns: [][]model.StreamEvent{
			{{
				Kind: model.StreamToolUse,
				ToolUse: model.ToolUse{
					ID:    "start-1",
					Name:  commandtools.StartToolName,
					Input: json.RawMessage(`{"id":"server-1","command":["npm","run","dev"],"purpose":"start local dev server","tty":true,"cols":100,"rows":30}`),
				},
			}},
			{{
				Kind: model.StreamToolUse,
				ToolUse: model.ToolUse{
					ID:    "write-1",
					Name:  commandtools.WriteInputToolName,
					Input: json.RawMessage(`{"id":"server-1","input":"hello","append_newline":true}`),
				},
			}},
			{{
				Kind: model.StreamToolUse,
				ToolUse: model.ToolUse{
					ID:    "resize-1",
					Name:  commandtools.ResizeToolName,
					Input: json.RawMessage(`{"id":"server-1","cols":120,"rows":40}`),
				},
			}},
			{{
				Kind: model.StreamToolUse,
				ToolUse: model.ToolUse{
					ID:    "stop-1",
					Name:  commandtools.StopToolName,
					Input: json.RawMessage(`{"id":"server-1","force":true}`),
				},
			}},
			{{Kind: model.StreamText, Text: "server lifecycle complete"}},
		}},
		Tools: tool.NewRegistry(
			commandtools.NewStartTool(manager),
			commandtools.NewWriteInputTool(manager),
			commandtools.NewResizeTool(manager),
			commandtools.NewStopTool(manager),
		),
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}

	var got []goldenEvent
	for event := range events {
		if event.Kind == EventError {
			t.Fatalf("query error: %v", event.Err)
		}
		got = append(got, normalizeGoldenEvent(event))
	}

	data, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("marshal golden events: %v", err)
	}
	data = append(data, '\n')

	want, err := os.ReadFile("testdata/golden/command_session_event_stream.json")
	if err != nil {
		t.Fatalf("read golden file: %v", err)
	}
	if strings.TrimSpace(string(data)) != strings.TrimSpace(string(want)) {
		t.Fatalf("command session event stream golden mismatch\n got:\n%s\nwant:\n%s", data, want)
	}
}

func TestQueryBudgetDenialEventStreamGolden(t *testing.T) {
	registry := tool.NewRegistry(tool.Definition{
		ToolSpec: model.ToolSpec{Name: "read", ReadOnly: true, ConcurrencySafe: true},
		Handler: func(context.Context, tool.Call) (model.ToolResult, error) {
			return model.ToolResult{Content: "content"}, nil
		},
	})
	events, err := Query(context.Background(), "read once", Options{
		Model: &fakeModel{turns: [][]model.StreamEvent{
			{{
				Kind: model.StreamToolUse,
				ToolUse: model.ToolUse{
					ID:    "tool-1",
					Name:  "read",
					Input: json.RawMessage(`{}`),
				},
			}},
		}},
		Tools:  registry,
		Budget: budget.Policy{MaxModelCalls: 1},
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}

	var got []goldenEvent
	for event := range events {
		got = append(got, normalizeGoldenEvent(event))
	}

	data, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("marshal golden events: %v", err)
	}
	data = append(data, '\n')

	want, err := os.ReadFile("testdata/golden/budget_denial_event_stream.json")
	if err != nil {
		t.Fatalf("read golden file: %v", err)
	}
	if strings.TrimSpace(string(data)) != strings.TrimSpace(string(want)) {
		t.Fatalf("budget denial event stream golden mismatch\n got:\n%s\nwant:\n%s", data, want)
	}
}

func TestQueryWorkspaceEventStreamGolden(t *testing.T) {
	store := workspace.NewMemoryStore(map[string]string{"README.md": "version one"})
	tools, err := workspacetools.NewTools(store)
	if err != nil {
		t.Fatalf("NewTools returned error: %v", err)
	}
	events, err := Query(context.Background(), "patch and restore the workspace", Options{
		Model: &fakeModel{turns: [][]model.StreamEvent{
			{{
				Kind: model.StreamToolUse,
				ToolUse: model.ToolUse{
					ID:    "checkpoint-1",
					Name:  workspacetools.CheckpointToolName,
					Input: json.RawMessage(`{"label":"before patch"}`),
				},
			}},
			{{
				Kind: model.StreamToolUse,
				ToolUse: model.ToolUse{
					ID:   "patch-1",
					Name: workspacetools.ApplyPatchToolName,
					Input: json.RawMessage(`{"operations":[
						{"path":"README.md","old_content":"version one","new_content":"version two"}
					]}`),
				},
			}},
			{{
				Kind: model.StreamToolUse,
				ToolUse: model.ToolUse{
					ID:    "diff-1",
					Name:  workspacetools.DiffToolName,
					Input: json.RawMessage(`{}`),
				},
			}},
			{{
				Kind: model.StreamToolUse,
				ToolUse: model.ToolUse{
					ID:    "restore-1",
					Name:  workspacetools.RestoreToolName,
					Input: json.RawMessage(`{"id":"checkpoint-1"}`),
				},
			}},
			{{Kind: model.StreamText, Text: "workspace restored"}},
		}},
		Tools: tool.NewRegistry(tools...),
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}

	var got []goldenEvent
	for event := range events {
		if event.Kind == EventError {
			t.Fatalf("query error: %v", event.Err)
		}
		got = append(got, normalizeGoldenEvent(event))
	}

	data, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("marshal golden events: %v", err)
	}
	data = append(data, '\n')

	want, err := os.ReadFile("testdata/golden/workspace_event_stream.json")
	if err != nil {
		t.Fatalf("read golden file: %v", err)
	}
	if strings.TrimSpace(string(data)) != strings.TrimSpace(string(want)) {
		t.Fatalf("workspace event stream golden mismatch\n got:\n%s\nwant:\n%s", data, want)
	}
}

func TestQueryVerificationEventStreamGolden(t *testing.T) {
	verifyTool := verifytools.NewTool(verifytools.Config{
		Verifier: verifytools.VerifierFunc(func(_ context.Context, req verifytools.Request) (verifytools.Result, error) {
			return verifytools.Result{
				Name:   req.Name,
				Passed: false,
				Output: "README.md is not fixed",
				Diagnostics: []verifytools.Diagnostic{{
					Path:     "README.md",
					Severity: "error",
					Message:  "expected fixed content",
				}},
			}, nil
		}),
	})
	events, err := Query(context.Background(), "verify the workspace", Options{
		Model: &fakeModel{turns: [][]model.StreamEvent{
			{{
				Kind: model.StreamToolUse,
				ToolUse: model.ToolUse{
					ID:    "verify-1",
					Name:  verifytools.ToolName,
					Input: json.RawMessage(`{"name":"test","target":"README.md"}`),
				},
			}},
			{{Kind: model.StreamText, Text: "verification failed"}},
		}},
		Tools: tool.NewRegistry(verifyTool),
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}

	var got []goldenEvent
	for event := range events {
		if event.Kind == EventError {
			t.Fatalf("query error: %v", event.Err)
		}
		got = append(got, normalizeGoldenEvent(event))
	}

	data, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("marshal golden events: %v", err)
	}
	data = append(data, '\n')

	want, err := os.ReadFile("testdata/golden/verification_event_stream.json")
	if err != nil {
		t.Fatalf("read golden file: %v", err)
	}
	if strings.TrimSpace(string(data)) != strings.TrimSpace(string(want)) {
		t.Fatalf("verification event stream golden mismatch\n got:\n%s\nwant:\n%s", data, want)
	}
}

func TestQueryTenantDenialEventStreamGolden(t *testing.T) {
	registry := tool.NewRegistry(tool.Definition{
		ToolSpec: model.ToolSpec{Name: "read", ReadOnly: true},
		Handler: func(context.Context, tool.Call) (model.ToolResult, error) {
			return model.ToolResult{Content: "content"}, nil
		},
	})
	events, err := Query(context.Background(), "read the file", Options{
		Model: &fakeModel{turns: [][]model.StreamEvent{
			{{
				Kind: model.StreamToolUse,
				ToolUse: model.ToolUse{
					ID:    "tool-1",
					Name:  "read",
					Input: json.RawMessage(`{"path":"README.md"}`),
				},
			}},
			{{Kind: model.StreamText, Text: "recovered"}},
		}},
		Tools: registry,
		Tenant: tenant.Scope{
			ID:        "tenant-1",
			SubjectID: "user-1",
			Attributes: map[string]string{
				"region": "us",
			},
		},
		TenantValidator: tenant.ValidatorFunc(func(_ context.Context, req tenant.Request) error {
			if req.Boundary == tenant.BoundaryToolUse {
				return errors.New("tool not allowed for tenant")
			}
			return nil
		}),
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}

	var got []goldenEvent
	for event := range events {
		if event.Kind == EventError {
			t.Fatalf("query error: %v", event.Err)
		}
		got = append(got, normalizeGoldenEvent(event))
	}

	data, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("marshal golden events: %v", err)
	}
	data = append(data, '\n')

	want, err := os.ReadFile("testdata/golden/tenant_denial_event_stream.json")
	if err != nil {
		t.Fatalf("read golden file: %v", err)
	}
	if strings.TrimSpace(string(data)) != strings.TrimSpace(string(want)) {
		t.Fatalf("tenant denial event stream golden mismatch\n got:\n%s\nwant:\n%s", data, want)
	}
}

type goldenEvent struct {
	Kind                      EventKind         `json:"kind"`
	Turn                      int               `json:"turn,omitempty"`
	Text                      string            `json:"text,omitempty"`
	ToolID                    string            `json:"tool_id,omitempty"`
	ToolName                  string            `json:"tool_name,omitempty"`
	ToolUseDelta              string            `json:"tool_use_delta,omitempty"`
	ToolResult                string            `json:"tool_result,omitempty"`
	UsageInputTokens          int               `json:"usage_input_tokens,omitempty"`
	UsageOutputTokens         int               `json:"usage_output_tokens,omitempty"`
	UsageTotalTokens          int               `json:"usage_total_tokens,omitempty"`
	UsageProvider             string            `json:"usage_provider,omitempty"`
	UsageModel                string            `json:"usage_model,omitempty"`
	ContextPolicy             string            `json:"context_policy,omitempty"`
	ContextReason             string            `json:"context_reason,omitempty"`
	ContextOriginal           int               `json:"context_original,omitempty"`
	ContextSent               int               `json:"context_sent,omitempty"`
	ContextSummaryHash        string            `json:"context_summary_hash,omitempty"`
	SkillAction               string            `json:"skill_action,omitempty"`
	SkillName                 string            `json:"skill_name,omitempty"`
	SkillResource             string            `json:"skill_resource,omitempty"`
	SkillQuery                string            `json:"skill_query,omitempty"`
	SkillSelectedNames        []string          `json:"skill_selected_names,omitempty"`
	SkillSelected             int               `json:"skill_selected,omitempty"`
	SkillOmitted              int               `json:"skill_omitted,omitempty"`
	SkillMatches              int               `json:"skill_matches,omitempty"`
	SkillMetadataOnly         bool              `json:"skill_metadata_only,omitempty"`
	SkillPromptBytesSet       bool              `json:"skill_prompt_bytes_set,omitempty"`
	MemoryCandidates          []string          `json:"memory_candidates,omitempty"`
	WorkspaceOperation        string            `json:"workspace_operation,omitempty"`
	WorkspacePaths            []string          `json:"workspace_paths,omitempty"`
	WorkspaceChanges          int               `json:"workspace_changes,omitempty"`
	WorkspaceAdded            int               `json:"workspace_added,omitempty"`
	WorkspaceModified         int               `json:"workspace_modified,omitempty"`
	WorkspaceDeleted          int               `json:"workspace_deleted,omitempty"`
	WorkspaceByteDelta        int               `json:"workspace_byte_delta,omitempty"`
	WorkspaceCheckpoint       string            `json:"workspace_checkpoint,omitempty"`
	WorkspaceBase             string            `json:"workspace_base,omitempty"`
	VerificationName          string            `json:"verification_name,omitempty"`
	VerificationPassed        bool              `json:"verification_passed,omitempty"`
	VerificationDiag          int               `json:"verification_diagnostics,omitempty"`
	VerificationPaths         []string          `json:"verification_paths,omitempty"`
	ApprovalAction            string            `json:"approval_action,omitempty"`
	ApprovalReason            string            `json:"approval_reason,omitempty"`
	ApprovalInputHash         string            `json:"approval_input_hash,omitempty"`
	ApprovalSummary           string            `json:"approval_summary,omitempty"`
	ApprovalSummaryRisk       string            `json:"approval_summary_risk,omitempty"`
	ApprovalSummaryPaths      []string          `json:"approval_summary_paths,omitempty"`
	ApprovalRequested         bool              `json:"approval_requested,omitempty"`
	ApprovalApproved          bool              `json:"approval_approved,omitempty"`
	ApprovalConsumed          bool              `json:"approval_consumed,omitempty"`
	ApprovalSingleUse         bool              `json:"approval_single_use,omitempty"`
	ApprovalInputBound        bool              `json:"approval_input_bound,omitempty"`
	TenantBoundary            string            `json:"tenant_boundary,omitempty"`
	TenantID                  string            `json:"tenant_id,omitempty"`
	TenantSubjectID           string            `json:"tenant_subject_id,omitempty"`
	TenantAttributes          map[string]string `json:"tenant_attributes,omitempty"`
	TenantReason              string            `json:"tenant_reason,omitempty"`
	CommandOperation          string            `json:"command_operation,omitempty"`
	CommandID                 string            `json:"command_id,omitempty"`
	CommandStatus             string            `json:"command_status,omitempty"`
	CommandPID                int               `json:"command_pid,omitempty"`
	CommandTTY                bool              `json:"command_tty,omitempty"`
	CommandSignalsProcessTree bool              `json:"command_signals_process_tree,omitempty"`
	CommandCols               int               `json:"command_cols,omitempty"`
	CommandRows               int               `json:"command_rows,omitempty"`
	CommandInputBytes         int               `json:"command_input_bytes,omitempty"`
	CommandNextSeq            int               `json:"command_next_seq,omitempty"`
	CommandOutputChunks       int               `json:"command_output_chunks,omitempty"`
	CommandDroppedChunks      int               `json:"command_dropped_chunks,omitempty"`
	CommandDroppedBytes       int               `json:"command_dropped_bytes,omitempty"`
	Result                    string            `json:"result,omitempty"`
	Error                     string            `json:"error,omitempty"`
}

func normalizeGoldenEvent(event Event) goldenEvent {
	out := goldenEvent{Kind: event.Kind, Turn: event.Turn}
	switch event.Kind {
	case EventAssistant:
		if event.Message != nil {
			out.Text = event.Message.PlainText()
		}
	case EventToolUseStart, EventToolUseDelta, EventToolUse:
		if event.ToolUse != nil {
			out.ToolID = event.ToolUse.ID
			out.ToolName = event.ToolUse.Name
		}
		out.ToolUseDelta = event.ToolUseDelta
	case EventToolResult:
		if event.ToolResult != nil {
			out.ToolID = event.ToolResult.ToolUseID
			out.ToolName = event.ToolResult.Name
			out.ToolResult = event.ToolResult.Content
		}
	case EventUsage:
		if event.Usage != nil {
			out.UsageInputTokens = event.Usage.InputTokens
			out.UsageOutputTokens = event.Usage.OutputTokens
			out.UsageTotalTokens = event.Usage.TotalTokens
			out.UsageProvider = event.Usage.Provider
			out.UsageModel = event.Usage.Model
		}
	case EventContextCompacted:
		if event.Compaction != nil {
			out.ContextPolicy = event.Compaction.Policy
			out.ContextReason = string(event.Compaction.Reason)
			out.ContextOriginal = event.Compaction.OriginalMessages
			out.ContextSent = event.Compaction.SentMessages
			out.ContextSummaryHash = event.Compaction.SummaryHash
		}
	case EventSkillDiscovery, EventSkillSearch, EventSkillLoaded, EventSkillResourceLoaded:
		if event.Skill != nil {
			out.SkillAction = event.Skill.Action
			out.SkillName = event.Skill.SkillName
			out.SkillResource = event.Skill.ResourceName
			out.SkillQuery = event.Skill.Query
			out.SkillSelectedNames = append([]string(nil), event.Skill.SelectedSkills...)
			out.SkillSelected = event.Skill.Selected
			out.SkillOmitted = event.Skill.Omitted
			out.SkillMatches = event.Skill.Matches
			out.SkillMetadataOnly = event.Skill.MetadataOnly
			out.SkillPromptBytesSet = event.Skill.PromptBytes > 0
		}
	case EventMemoryCandidates:
		if event.Memory != nil {
			for _, candidate := range event.Memory.Candidates {
				out.MemoryCandidates = append(out.MemoryCandidates, candidate.Memory.Name)
			}
		}
	case EventWorkspacePatch, EventWorkspaceDiff, EventWorkspaceCheckpoint, EventWorkspaceRestore:
		if event.Workspace != nil {
			out.WorkspaceOperation = event.Workspace.Operation
			out.WorkspacePaths = append([]string(nil), event.Workspace.Paths...)
			out.WorkspaceChanges = event.Workspace.Changes
			out.WorkspaceAdded = event.Workspace.Added
			out.WorkspaceModified = event.Workspace.Modified
			out.WorkspaceDeleted = event.Workspace.Deleted
			out.WorkspaceByteDelta = event.Workspace.ByteDelta
			out.WorkspaceCheckpoint = event.Workspace.CheckpointID
			out.WorkspaceBase = event.Workspace.BaseID
		}
	case EventVerification:
		if event.Verification != nil {
			out.VerificationName = event.Verification.Name
			out.VerificationPassed = event.Verification.Passed
			out.VerificationDiag = event.Verification.Diagnostics
			out.VerificationPaths = append([]string(nil), event.Verification.Paths...)
		}
	case EventApprovalRequested, EventApprovalGranted, EventApprovalDenied, EventApprovalConsumed:
		if event.Approval != nil {
			out.ApprovalAction = event.Approval.Action
			out.ApprovalReason = event.Approval.Reason
			if event.Approval.InputHash != "" {
				out.ApprovalInputHash = "set"
			}
			out.ApprovalSummary = event.Approval.Summary.Title
			out.ApprovalSummaryRisk = event.Approval.Summary.Risk
			out.ApprovalSummaryPaths = append([]string(nil), event.Approval.Summary.Paths...)
			out.ApprovalRequested = event.Approval.Requested
			out.ApprovalApproved = event.Approval.Approved
			out.ApprovalConsumed = event.Approval.Consumed
			out.ApprovalSingleUse = event.Approval.SingleUse
			out.ApprovalInputBound = event.Approval.InputBound
		}
	case EventTenantDenied:
		if event.Tenant != nil {
			out.TenantBoundary = event.Tenant.Boundary
			out.TenantID = event.Tenant.TenantID
			out.TenantSubjectID = event.Tenant.SubjectID
			out.TenantAttributes = cloneStringMap(event.Tenant.Attributes)
			out.TenantReason = event.Tenant.Reason
		}
	case EventCommandFinished, EventCommandStarted, EventCommandInput, EventCommandOutput, EventCommandStopped, EventCommandResized:
		if event.Command != nil {
			out.CommandOperation = event.Command.Operation
			out.CommandID = event.Command.CommandID
			out.CommandStatus = event.Command.Status
			out.CommandPID = event.Command.PID
			out.CommandTTY = event.Command.TTY
			out.CommandSignalsProcessTree = event.Command.SignalsProcessTree
			out.CommandCols = event.Command.Cols
			out.CommandRows = event.Command.Rows
			out.CommandInputBytes = event.Command.InputBytes
			out.CommandNextSeq = event.Command.NextSeq
			out.CommandOutputChunks = event.Command.OutputChunks
			out.CommandDroppedChunks = event.Command.DroppedChunks
			out.CommandDroppedBytes = event.Command.DroppedBytes
		}
	case EventResult:
		out.Result = event.Result
		if event.Usage != nil {
			out.UsageInputTokens = event.Usage.InputTokens
			out.UsageOutputTokens = event.Usage.OutputTokens
			out.UsageTotalTokens = event.Usage.TotalTokens
			out.UsageProvider = event.Usage.Provider
			out.UsageModel = event.Usage.Model
		}
	case EventError:
		if event.Err != nil {
			out.Error = event.Err.Error()
		}
	}
	return out
}

func intPtr(v int) *int { return &v }

type oneShotCompactionPolicy struct {
	emitted bool
}

func (p *oneShotCompactionPolicy) Apply(ctx context.Context, messages []model.Message) ([]model.Message, error) {
	result, err := p.ApplyWithResult(ctx, messages)
	return result.Messages, err
}

func (p *oneShotCompactionPolicy) ApplyWithResult(ctx context.Context, messages []model.Message) (contextwindow.PolicyResult, error) {
	if err := ctx.Err(); err != nil {
		return contextwindow.PolicyResult{}, err
	}
	out := model.CloneMessages(messages)
	if p.emitted {
		return contextwindow.PolicyResult{Messages: out}, nil
	}
	p.emitted = true
	return contextwindow.PolicyResult{
		Messages: out,
		Compaction: &contextwindow.CompactionRecord{
			Policy:           "observability_test",
			Reason:           contextwindow.CompactionReasonBudget,
			OriginalMessages: len(messages),
			SentMessages:     len(out),
			SummaryHash:      "summary-hash",
			SummaryPreview:   "summary",
		},
	}, nil
}
