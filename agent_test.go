package memaxagent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/budget"
	"github.com/MemaxLabs/memax-go-agent-sdk/contextwindow"
	"github.com/MemaxLabs/memax-go-agent-sdk/hook"
	"github.com/MemaxLabs/memax-go-agent-sdk/identity"
	"github.com/MemaxLabs/memax-go-agent-sdk/memory"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/output"
	"github.com/MemaxLabs/memax-go-agent-sdk/planner"
	"github.com/MemaxLabs/memax-go-agent-sdk/resultstore"
	"github.com/MemaxLabs/memax-go-agent-sdk/session"
	"github.com/MemaxLabs/memax-go-agent-sdk/skill"
	"github.com/MemaxLabs/memax-go-agent-sdk/telemetry"
	"github.com/MemaxLabs/memax-go-agent-sdk/tenant"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/agentpolicy"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/approvaltools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/commandtools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/skilltools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/verifytools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/workspacetools"
	"github.com/MemaxLabs/memax-go-agent-sdk/workspace"
)

func TestQueryRunsToolAndContinuesToResult(t *testing.T) {
	registry := tool.NewRegistry(tool.Definition{
		ToolSpec: model.ToolSpec{Name: "read", ReadOnly: true, ConcurrencySafe: true},
		Handler: func(_ context.Context, call tool.Call) (model.ToolResult, error) {
			var input struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(call.Use.Input, &input); err != nil {
				t.Fatalf("unmarshal tool input: %v", err)
			}
			return model.ToolResult{Content: "content from " + input.Path}, nil
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

	result, err := Drain(events)
	if err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if result != "done" {
		t.Fatalf("result = %q, want %q", result, "done")
	}
}

func TestQueryPreservesWhitespaceOnlyAssistantTextDeltas(t *testing.T) {
	events, err := Query(context.Background(), "summarize", Options{
		Model: &fakeModel{turns: [][]model.StreamEvent{{
			{Kind: model.StreamText, Text: "###"},
			{Kind: model.StreamText, Text: " "},
			{Kind: model.StreamText, Text: "1. Strong CLI/config hygiene"},
			{Kind: model.StreamText, Text: "\n"},
			{Kind: model.StreamText, Text: "The parser is careful about:"},
			{Kind: model.StreamText, Text: "\n"},
			{Kind: model.StreamText, Text: "- source precedence"},
			{Kind: model.StreamText, Text: "\n"},
			{Kind: model.StreamText, Text: "- conflict validation"},
			{Kind: model.StreamText, Text: "\n\n"},
			{Kind: model.StreamText, Text: "Done."},
		}}},
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}

	var assistantChunks []string
	var result string
	for event := range events {
		switch event.Kind {
		case EventAssistant:
			if event.Message != nil {
				assistantChunks = append(assistantChunks, event.Message.PlainText())
			}
		case EventResult:
			result = event.Result
		case EventError:
			t.Fatalf("query event error: %v", event.Err)
		}
	}

	want := "### 1. Strong CLI/config hygiene\nThe parser is careful about:\n- source precedence\n- conflict validation\n\nDone."
	if got := strings.Join(assistantChunks, ""); got != want {
		t.Fatalf("assistant chunks joined = %q, want %q", got, want)
	}
	if result != want {
		t.Fatalf("result = %q, want %q", result, want)
	}
}

func TestQueryNormalizesPathologicalBlankAssistantTextDeltas(t *testing.T) {
	events, err := Query(context.Background(), "continue", Options{
		Model: &fakeModel{turns: [][]model.StreamEvent{{
			{Kind: model.StreamText, Text: "Now"},
			{Kind: model.StreamText, Text: "\n\n\n\n\n\n\n\n"},
			{Kind: model.StreamText, Text: " updating"},
			{Kind: model.StreamText, Text: "\n\n\n\n\n\n\n\n"},
			{Kind: model.StreamText, Text: " layout"},
			{Kind: model.StreamText, Text: "\n\n\n\n\n\n\n\n"},
			{Kind: model.StreamText, Text: ".\n\n"},
			{Kind: model.StreamText, Text: "\n\n\n\n\n\n\n\n"},
			{Kind: model.StreamText, Text: "ts"},
			{Kind: model.StreamText, Text: "\n\n\n\n\n\n\n\n"},
			{Kind: model.StreamText, Text: "x"},
			{Kind: model.StreamText, Text: "\n\n\n\n\n\n\n\n"},
			{Kind: model.StreamText, Text: " with"},
			{Kind: model.StreamText, Text: "\n\n\n\n\n\n\n\n"},
			{Kind: model.StreamText, Text: " Ge"},
			{Kind: model.StreamText, Text: "\n\n\n\n\n\n\n\n"},
			{Kind: model.StreamText, Text: "ist"},
			{Kind: model.StreamText, Text: "\n\n\n\n\n\n\n\n"},
			{Kind: model.StreamText, Text: " font"},
			{Kind: model.StreamText, Text: "\n\n\n\n\n\n\n\n"},
			{Kind: model.StreamText, Text: " loading"},
			{Kind: model.StreamText, Text: "\n\n\n\n\n\n\n\n"},
			{Kind: model.StreamText, Text: "."},
		}}},
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}

	var assistantChunks []string
	var result string
	for event := range events {
		switch event.Kind {
		case EventAssistant:
			if event.Message != nil {
				assistantChunks = append(assistantChunks, event.Message.PlainText())
			}
		case EventResult:
			result = event.Result
		case EventError:
			t.Fatalf("query event error: %v", event.Err)
		}
	}

	want := "Now updating layout.tsx with Geist font loading."
	if got := strings.Join(assistantChunks, ""); got != want {
		t.Fatalf("assistant chunks joined = %q, want %q", got, want)
	}
	if result != want {
		t.Fatalf("result = %q, want %q", result, want)
	}
}

func TestQueryStartsSafeToolBeforeAssistantStreamEnds(t *testing.T) {
	started := make(chan struct{})
	registry := tool.NewRegistry(tool.Definition{
		ToolSpec: model.ToolSpec{Name: "lookup", ReadOnly: true, ConcurrencySafe: true},
		Handler: func(context.Context, tool.Call) (model.ToolResult, error) {
			close(started)
			return model.ToolResult{Content: "lookup result"}, nil
		},
	})
	store := session.NewMemoryStore()
	events, err := Query(context.Background(), "lookup before finishing text", Options{
		Model: &earlyToolModel{
			toolStarted: started,
			first: []model.StreamEvent{
				{
					Kind: model.StreamToolUseStart,
					ToolUse: model.ToolUse{
						ID:   "tool-1",
						Name: "lookup",
					},
				},
				{
					Kind: model.StreamToolUseDelta,
					ToolUse: model.ToolUse{
						ID:   "tool-1",
						Name: "lookup",
					},
					ToolUseDelta: `{}`,
				},
				{
					Kind: model.StreamToolUse,
					ToolUse: model.ToolUse{
						ID:    "tool-1",
						Name:  "lookup",
						Input: json.RawMessage(`{}`),
					},
				},
				{Kind: model.StreamText, Text: " while continuing"},
			},
			second: []model.StreamEvent{{Kind: model.StreamText, Text: "done"}},
		},
		Tools:    registry,
		Sessions: store,
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	result, err := Drain(events)
	if err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if result != "done" {
		t.Fatalf("result = %q, want done", result)
	}
	sessions, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(sessions))
	}
	messages, err := store.Messages(context.Background(), sessions[0].ID)
	if err != nil {
		t.Fatalf("Messages returned error: %v", err)
	}
	if len(messages) < 3 {
		t.Fatalf("messages = %#v, want user, assistant, tool result", messages)
	}
	if messages[1].Role != model.RoleAssistant || len(messages[1].Content) != 2 {
		t.Fatalf("assistant message = %#v, want tool use plus trailing text", messages[1])
	}
	if messages[2].Role != model.RoleTool || messages[2].ToolResult == nil || messages[2].ToolResult.Content != "lookup result" {
		t.Fatalf("tool result message = %#v, want persisted lookup result", messages[2])
	}
}

func TestQueryCancelsEarlyToolAndEmitsToolResultWhenStreamFails(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	registry := tool.NewRegistry(tool.Definition{
		ToolSpec: model.ToolSpec{Name: "lookup", ReadOnly: true, ConcurrencySafe: true},
		Handler: func(ctx context.Context, _ tool.Call) (model.ToolResult, error) {
			close(started)
			<-ctx.Done()
			close(cancelled)
			<-release
			return model.ToolResult{Content: "observed cancel", IsError: true}, nil
		},
	})
	store := session.NewMemoryStore()
	events, err := Query(context.Background(), "lookup before stream failure", Options{
		Model: &streamErrorModel{
			started: started,
			events: []model.StreamEvent{
				{
					Kind: model.StreamToolUse,
					ToolUse: model.ToolUse{
						ID:    "tool-1",
						Name:  "lookup",
						Input: json.RawMessage(`{}`),
					},
				},
			},
			err: errors.New("stream exploded"),
		},
		Tools:    registry,
		Sessions: store,
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}

	var toolResult *model.ToolResult
	var gotErr error
	for event := range events {
		switch event.Kind {
		case EventToolResult:
			result := *event.ToolResult
			toolResult = &result
		case EventError:
			gotErr = event.Err
		}
	}
	close(release)
	if gotErr == nil || !strings.Contains(gotErr.Error(), "stream exploded") {
		t.Fatalf("error = %v, want stream failure", gotErr)
	}
	if toolResult == nil {
		t.Fatal("missing cancellation tool result")
	}
	if !toolResult.IsError || toolResult.ToolUseID != "tool-1" || toolResult.Name != "lookup" {
		t.Fatalf("tool result = %#v, want cancellation error for lookup", toolResult)
	}
	if !strings.Contains(toolResult.Content, "model streaming stopped") {
		t.Fatalf("tool result content = %q, want streaming cancellation reason", toolResult.Content)
	}
	sessions, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(sessions))
	}
	messages, err := store.Messages(context.Background(), sessions[0].ID)
	if err != nil {
		t.Fatalf("Messages returned error: %v", err)
	}
	if len(messages) < 3 {
		t.Fatalf("messages = %#v, want user, assistant, canceled tool result", messages)
	}
	if messages[1].Role != model.RoleAssistant || len(messages[1].Content) != 1 || messages[1].Content[0].ToolUse == nil {
		t.Fatalf("assistant message = %#v, want persisted tool use", messages[1])
	}
	if messages[2].Role != model.RoleTool || messages[2].ToolResult == nil {
		t.Fatalf("tool result message = %#v, want persisted cancellation result", messages[2])
	}
	if messages[2].ToolResult.ToolUseID != "tool-1" || !messages[2].ToolResult.IsError {
		t.Fatalf("tool result message = %#v, want paired cancellation result", messages[2])
	}
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("early tool did not observe cancellation")
	}
}

func TestQueryAsyncReturnsBeforeStartupIOCompletes(t *testing.T) {
	store := &blockingCreateStore{
		inner:   session.NewMemoryStore(),
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	startedAt := time.Now()
	events := QueryAsync(context.Background(), "start", Options{
		Model:    &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "done"}}}},
		Sessions: store,
	})
	if elapsed := time.Since(startedAt); elapsed > 50*time.Millisecond {
		t.Fatalf("QueryAsync blocked caller for %s", elapsed)
	}
	<-store.started
	close(store.release)
	result, err := Drain(events)
	if err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if result != "done" {
		t.Fatalf("result = %q, want done", result)
	}
}

func TestQueryPersistsProviderArtifactsWithoutAssistantText(t *testing.T) {
	store := session.NewMemoryStore()
	sess, err := store.Create(context.Background())
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	artifact := &model.ProviderArtifact{
		Provider: "openai",
		Type:     "reasoning",
		ID:       "rs_1",
		Data:     json.RawMessage(`{"type":"reasoning","id":"rs_1","encrypted_content":"opaque"}`),
	}
	events, err := Query(context.Background(), "start", Options{
		Model: &fakeModel{turns: [][]model.StreamEvent{{
			{Kind: model.StreamProviderArtifact, ProviderArtifact: artifact},
			{Kind: model.StreamText, Text: "done"},
		}}},
		Sessions:  store,
		SessionID: sess.ID,
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	result, err := Drain(events)
	if err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if result != "done" {
		t.Fatalf("result = %q, want done", result)
	}
	messages, err := store.Messages(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("Messages returned error: %v", err)
	}
	assistant := messages[len(messages)-1]
	if assistant.PlainText() != "done" {
		t.Fatalf("PlainText() = %q, want done", assistant.PlainText())
	}
	if len(assistant.Content) != 2 || assistant.Content[0].ProviderArtifact == nil {
		t.Fatalf("assistant content = %#v, want provider artifact followed by text", assistant.Content)
	}
	if got := assistant.Content[0].ProviderArtifact; got.Provider != "openai" || got.Type != "reasoning" || got.ID != "rs_1" {
		t.Fatalf("provider artifact = %#v", got)
	}
}

func TestQueryFeedsValidationErrorBackToModel(t *testing.T) {
	registry := tool.NewRegistry(tool.Definition{
		ToolSpec: model.ToolSpec{
			Name: "read",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []any{"path"},
				"properties": map[string]any{
					"path": map[string]any{"type": "string"},
				},
				"additionalProperties": false,
			},
			ReadOnly: true,
		},
		Handler: func(context.Context, tool.Call) (model.ToolResult, error) {
			t.Fatal("handler should not run for invalid model input")
			return model.ToolResult{}, nil
		},
	})
	fake := &fakeModel{turns: [][]model.StreamEvent{
		{
			{
				Kind: model.StreamToolUse,
				ToolUse: model.ToolUse{
					ID:    "tool-1",
					Name:  "read",
					Input: json.RawMessage(`{"path":42}`),
				},
			},
		},
		{{Kind: model.StreamText, Text: "recovered"}},
	}}

	events, err := Query(context.Background(), "read the file", Options{
		Model: fake,
		Tools: registry,
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	result, err := Drain(events)
	if err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if result != "recovered" {
		t.Fatalf("result = %q, want %q", result, "recovered")
	}
	if got, want := len(fake.requests), 2; got != want {
		t.Fatalf("model calls = %d, want %d", got, want)
	}
	last := fake.requests[1].Messages[len(fake.requests[1].Messages)-1]
	if last.Role != model.RoleTool || last.ToolResult == nil || !last.ToolResult.IsError {
		t.Fatalf("last message before recovery = %#v, want tool error", last)
	}
}

func TestQueryProgressiveSkillDisclosureLoadsSkillThroughTool(t *testing.T) {
	fake := &fakeModel{turns: [][]model.StreamEvent{
		{
			{
				Kind: model.StreamToolUse,
				ToolUse: model.ToolUse{
					ID:    "skill-1",
					Name:  skill.LoadToolName,
					Input: json.RawMessage(`{"name":"database-review"}`),
				},
			},
		},
		{{Kind: model.StreamText, Text: "reviewed with skill"}},
	}}
	sourceCalls := 0
	source := skill.SourceFunc(func(context.Context) ([]skill.Skill, error) {
		sourceCalls++
		return []skill.Skill{{
			Name:        "database-review",
			Description: "Review database migrations.",
			WhenToUse:   "SQL changes are involved.",
			AlwaysOn:    true,
			Content:     "Check lock behavior and rollback safety.",
		}}, nil
	})

	events, err := Query(context.Background(), "review SQL migration", Options{
		Model:           fake,
		SkillSource:     source,
		SkillDisclosure: skill.DisclosureProgressive,
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	result, err := Drain(events)
	if err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if result != "reviewed with skill" {
		t.Fatalf("result = %q, want reviewed with skill", result)
	}
	if sourceCalls != 1 {
		t.Fatalf("source calls = %d, want one per run", sourceCalls)
	}
	if got := len(fake.requests); got != 2 {
		t.Fatalf("model calls = %d, want 2", got)
	}
	first := fake.requests[0]
	if !requestHasTool(first, skill.LoadToolName) {
		t.Fatalf("first request tools = %#v, want %s", first.Tools, skill.LoadToolName)
	}
	if strings.Contains(first.SystemPrompt, "Check lock behavior") {
		t.Fatalf("first prompt leaked full skill content:\n%s", first.SystemPrompt)
	}
	if !strings.Contains(first.SystemPrompt, "database-review") || !strings.Contains(first.SystemPrompt, "load_skill") {
		t.Fatalf("first prompt missing progressive skill metadata:\n%s", first.SystemPrompt)
	}
	second := fake.requests[1]
	if len(second.Messages) == 0 {
		t.Fatal("second request has no messages")
	}
	last := second.Messages[len(second.Messages)-1]
	if last.Role != model.RoleTool || last.ToolResult == nil || last.ToolResult.Name != skill.LoadToolName {
		t.Fatalf("last message = %#v, want load_skill tool result", last)
	}
	if !strings.Contains(last.ToolResult.Content, "Check lock behavior and rollback safety.") {
		t.Fatalf("load_skill result = %q, want full instructions", last.ToolResult.Content)
	}
}

func TestEffectiveToolSpecsIncludesRuntimeSkillTools(t *testing.T) {
	specs, err := EffectiveToolSpecs(Options{
		Tools: tool.NewRegistry(),
		SkillSource: skill.StaticSource{{
			Name:    "review",
			Content: "Review carefully.",
		}},
		SkillDisclosure: skill.DisclosureProgressive,
	})
	if err != nil {
		t.Fatalf("EffectiveToolSpecs() error = %v", err)
	}
	var found bool
	for _, spec := range specs {
		if spec.Name == skill.LoadToolName {
			found = true
			if !spec.ReadOnly {
				t.Fatalf("load_skill ReadOnly = false, want true")
			}
		}
	}
	if !found {
		t.Fatalf("EffectiveToolSpecs() did not include %s", skill.LoadToolName)
	}
}

func TestEffectiveToolSpecsOmitsRuntimeSkillToolsWithoutSkillSource(t *testing.T) {
	specs, err := EffectiveToolSpecs(Options{
		Tools: tool.NewRegistry(),
	})
	if err != nil {
		t.Fatalf("EffectiveToolSpecs() error = %v", err)
	}
	for _, spec := range specs {
		if spec.Name == skill.LoadToolName {
			t.Fatalf("EffectiveToolSpecs() included %s without SkillSource", skill.LoadToolName)
		}
	}
}

func TestCollectAssistantNormalizesEmptyToolInput(t *testing.T) {
	stream := &fakeStream{events: []model.StreamEvent{{
		Kind: model.StreamToolUse,
		ToolUse: model.ToolUse{
			ID:    "tool-1",
			Name:  "workspace_apply_patch",
			Input: json.RawMessage{},
		},
	}}}
	var started model.ToolUse
	assistant, uses, _, _, err := collectAssistant(
		context.Background(),
		func(Event) bool { return true },
		stream,
		"session-1",
		1,
		&recordingMeter{},
		func(use model.ToolUse) (<-chan model.ToolResult, bool, error) {
			started = use
			return nil, false, nil
		},
	)
	if err != nil {
		t.Fatalf("collectAssistant returned error: %v", err)
	}
	if len(uses) != 1 || string(uses[0].Input) != `{}` {
		t.Fatalf("uses = %#v, want normalized input", uses)
	}
	if got := string(started.Input); got != `{}` {
		t.Fatalf("early tool input = %q, want {}", got)
	}
	if len(assistant.Content) != 1 || assistant.Content[0].ToolUse == nil || string(assistant.Content[0].ToolUse.Input) != `{}` {
		t.Fatalf("assistant = %#v, want normalized tool block", assistant)
	}
	if _, err := json.Marshal(assistant); err != nil {
		t.Fatalf("Marshal assistant returned error: %v", err)
	}
}

func TestQueryProgressiveSkillResourceLoadsThroughTool(t *testing.T) {
	fake := &fakeModel{turns: [][]model.StreamEvent{
		{
			{
				Kind: model.StreamToolUse,
				ToolUse: model.ToolUse{
					ID:    "skill-1",
					Name:  skill.LoadToolName,
					Input: json.RawMessage(`{"name":"database-review"}`),
				},
			},
		},
		{
			{
				Kind: model.StreamToolUse,
				ToolUse: model.ToolUse{
					ID:    "resource-1",
					Name:  skill.ResourceToolName,
					Input: json.RawMessage(`{"skill_name":"database-review","resource":"migration-checklist"}`),
				},
			},
		},
		{{Kind: model.StreamText, Text: "reviewed with resource"}},
	}}
	source := skill.StaticSource{{
		Name:        "database-review",
		Description: "Review database migrations.",
		AlwaysOn:    true,
		Content:     "Check lock behavior.",
		Resources: []skill.ResourceRef{{
			Name:        "migration-checklist",
			Description: "Migration checklist.",
			Path:        "resources/migration-checklist.md",
			MIMEType:    "text/markdown",
			Bytes:       64,
		}},
	}}
	resources := skill.StaticResourceSource{{
		SkillName: "database-review",
		Name:      "migration-checklist",
		Path:      "resources/migration-checklist.md",
		MIMEType:  "text/markdown",
		Content:   "Step 1: confirm rollback.",
	}}

	events, err := Query(context.Background(), "review SQL migration", Options{
		Model:               fake,
		SkillSource:         source,
		SkillResourceSource: resources,
		SkillDisclosure:     skill.DisclosureProgressive,
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	result, err := Drain(events)
	if err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if result != "reviewed with resource" {
		t.Fatalf("result = %q, want reviewed with resource", result)
	}
	if got := len(fake.requests); got != 3 {
		t.Fatalf("model calls = %d, want 3", got)
	}
	first := fake.requests[0]
	if !requestHasTool(first, skill.ResourceToolName) {
		t.Fatalf("first request tools = %#v, want %s", first.Tools, skill.ResourceToolName)
	}
	if !strings.Contains(first.SystemPrompt, "migration-checklist") || !strings.Contains(first.SystemPrompt, skill.ResourceToolName) {
		t.Fatalf("first prompt missing resource metadata:\n%s", first.SystemPrompt)
	}
	if strings.Contains(first.SystemPrompt, "Step 1: confirm rollback.") {
		t.Fatalf("first prompt leaked resource content:\n%s", first.SystemPrompt)
	}
	secondLast := fake.requests[2].Messages[len(fake.requests[2].Messages)-1]
	if secondLast.Role != model.RoleTool || secondLast.ToolResult == nil || secondLast.ToolResult.Name != skill.ResourceToolName {
		t.Fatalf("last message before final = %#v, want resource tool result", secondLast)
	}
	if !strings.Contains(secondLast.ToolResult.Content, "Step 1: confirm rollback.") {
		t.Fatalf("resource result = %q, want full resource content", secondLast.ToolResult.Content)
	}
	if secondLast.ToolResult.Metadata[model.MetadataLoadedSkillResource] != true {
		t.Fatalf("resource metadata = %#v, want loaded resource marker", secondLast.ToolResult.Metadata)
	}
	if secondLast.ToolResult.Metadata["skill_loaded"] != true {
		t.Fatalf("resource metadata = %#v, want skill_loaded true", secondLast.ToolResult.Metadata)
	}
}

func TestQueryEmitsSkillEventsAndMetrics(t *testing.T) {
	fake := &fakeModel{turns: [][]model.StreamEvent{
		{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "search-1",
				Name:  "search_skills",
				Input: json.RawMessage(`{"query":"SQL migration","limit":1}`),
			},
		}},
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
		{{Kind: model.StreamText, Text: "done"}},
	}}
	source := skill.StaticSource{{
		Name:        "database-review",
		Description: "SQL migration review.",
		AlwaysOn:    true,
		Content:     "Check rollback safety.",
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
		Content:   "Confirm rollback.",
	}}
	searchTool, err := skilltools.NewSearchTool(skilltools.Config{Source: source})
	if err != nil {
		t.Fatalf("NewSearchTool returned error: %v", err)
	}
	meter := &recordingMeter{}

	stream, err := Query(context.Background(), "review SQL migration", Options{
		Model:               fake,
		Tools:               tool.NewRegistry(searchTool),
		SkillSource:         source,
		SkillResourceSource: resources,
		SkillDisclosure:     skill.DisclosureProgressive,
		Meter:               meter,
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	var events []Event
	for event := range stream {
		if event.Kind == EventError {
			t.Fatalf("unexpected error event: %v", event.Err)
		}
		events = append(events, event)
	}

	discovery := findSkillEvent(events, EventSkillDiscovery)
	if discovery == nil || discovery.Selected != 1 || discovery.PromptBytes == 0 || len(discovery.SelectedSkills) != 1 || discovery.SelectedSkills[0] != "database-review" {
		t.Fatalf("skill discovery event = %#v", discovery)
	}
	search := findSkillEvent(events, EventSkillSearch)
	if search == nil || search.Query != "SQL migration" || search.Matches != 1 || !search.MetadataOnly {
		t.Fatalf("skill search event = %#v", search)
	}
	loaded := findSkillEvent(events, EventSkillLoaded)
	if loaded == nil || loaded.SkillName != "database-review" {
		t.Fatalf("skill loaded event = %#v", loaded)
	}
	resource := findSkillEvent(events, EventSkillResourceLoaded)
	if resource == nil || resource.SkillName != "database-review" || resource.ResourceName != "migration-checklist" {
		t.Fatalf("skill resource event = %#v", resource)
	}
	for _, counter := range []string{
		"memax.skill.discovery",
		"memax.skill.search",
		"memax.skill.loaded",
		"memax.skill.resource_loaded",
	} {
		if !meter.hasCounter(counter) {
			t.Fatalf("meter counters = %#v, missing %s", meter.counterNames(), counter)
		}
	}
}

func TestQueryEmitsWorkspaceEventsAndMetrics(t *testing.T) {
	store := workspace.NewMemoryStore(map[string]string{"README.md": "one"})
	workspaceTools, err := workspacetools.NewTools(store)
	if err != nil {
		t.Fatalf("NewTools returned error: %v", err)
	}
	fake := &fakeModel{turns: [][]model.StreamEvent{
		{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "checkpoint-1",
				Name:  workspacetools.CheckpointToolName,
				Input: json.RawMessage(`{"label":"before"}`),
			},
		}},
		{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "patch-1",
				Name:  workspacetools.ApplyPatchToolName,
				Input: json.RawMessage(`{"operations":[{"path":"README.md","old_content":"one","new_content":"two"}]}`),
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
		{{Kind: model.StreamText, Text: "done"}},
	}}
	meter := &recordingMeter{}
	stream, err := Query(context.Background(), "patch README and restore it", Options{
		Model: fake,
		Tools: tool.NewRegistry(workspaceTools...),
		Meter: meter,
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	var events []Event
	for event := range stream {
		if event.Kind == EventError {
			t.Fatalf("unexpected error event: %v", event.Err)
		}
		events = append(events, event)
	}
	checkpoint := findWorkspaceEvent(events, EventWorkspaceCheckpoint)
	if checkpoint == nil || checkpoint.Operation != "checkpoint" || checkpoint.CheckpointID != "checkpoint-1" {
		t.Fatalf("checkpoint event = %#v", checkpoint)
	}
	patch := findWorkspaceEvent(events, EventWorkspacePatch)
	if patch == nil || patch.Operation != "patch" || patch.Changes != 1 || patch.Modified != 1 || patch.ByteDelta != 0 || !sameStrings(patch.Paths, []string{"README.md"}) {
		t.Fatalf("patch event = %#v", patch)
	}
	diff := findWorkspaceEvent(events, EventWorkspaceDiff)
	if diff == nil || diff.Operation != "diff" || diff.BaseID != "checkpoint-0" || diff.Changes != 1 || diff.Modified != 1 {
		t.Fatalf("diff event = %#v", diff)
	}
	restore := findWorkspaceEvent(events, EventWorkspaceRestore)
	if restore == nil || restore.Operation != "restore" || restore.CheckpointID != "checkpoint-1" {
		t.Fatalf("restore event = %#v", restore)
	}
	for _, counter := range []string{
		"memax.workspace.checkpoint",
		"memax.workspace.patch",
		"memax.workspace.diff",
		"memax.workspace.restore",
	} {
		if !meter.hasCounter(counter) {
			t.Fatalf("meter counters = %#v, missing %s", meter.counterNames(), counter)
		}
	}
}

func TestQueryEmitsWorkspaceCheckpointEventForAutoCheckpointedPatch(t *testing.T) {
	store := workspace.NewMemoryStore(map[string]string{"README.md": "one"})
	fake := &fakeModel{turns: [][]model.StreamEvent{
		{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "patch-1",
				Name:  workspacetools.ApplyPatchToolName,
				Input: json.RawMessage(`{"operations":[{"path":"README.md","old_content":"one","new_content":"two"}]}`),
			},
		}},
		{{Kind: model.StreamText, Text: "done"}},
	}}
	meter := &recordingMeter{}
	stream, err := Query(context.Background(), "patch README", Options{
		Model: fake,
		Tools: tool.NewRegistry(workspacetools.NewAutoCheckpointApplyPatchToolWithReview(store, nil)),
		Meter: meter,
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	var events []Event
	for event := range stream {
		if event.Kind == EventError {
			t.Fatalf("unexpected error event: %v", event.Err)
		}
		events = append(events, event)
	}
	checkpointIndex := -1
	patchIndex := -1
	for i, event := range events {
		switch event.Kind {
		case EventWorkspaceCheckpoint:
			checkpointIndex = i
			if event.Workspace == nil || event.Workspace.Operation != "checkpoint" || event.Workspace.CheckpointID != "checkpoint-1" {
				t.Fatalf("checkpoint event = %#v", event.Workspace)
			}
		case EventWorkspacePatch:
			patchIndex = i
			if event.Workspace == nil || event.Workspace.Operation != "patch" || event.Workspace.CheckpointID != "checkpoint-1" {
				t.Fatalf("patch event = %#v", event.Workspace)
			}
		}
	}
	if checkpointIndex < 0 || patchIndex < 0 || checkpointIndex > patchIndex {
		t.Fatalf("workspace event order checkpoint=%d patch=%d", checkpointIndex, patchIndex)
	}
	for _, counter := range []string{"memax.workspace.checkpoint", "memax.workspace.patch"} {
		if !meter.hasCounter(counter) {
			t.Fatalf("meter counters = %#v, missing %s", meter.counterNames(), counter)
		}
	}
}

func TestQueryEmitsVerificationEventsAndMetrics(t *testing.T) {
	verifyTool := verifytools.NewTool(verifytools.Config{
		Verifier: verifytools.VerifierFunc(func(_ context.Context, req verifytools.Request) (verifytools.Result, error) {
			return verifytools.Result{
				Name:   req.Name,
				Passed: false,
				Output: "unit test failed",
				Diagnostics: []verifytools.Diagnostic{{
					Path:     "README.md",
					Severity: "error",
					Message:  "expected fixed content",
				}},
			}, nil
		}),
	})
	fake := &fakeModel{turns: [][]model.StreamEvent{
		{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "verify-1",
				Name:  verifytools.ToolName,
				Input: json.RawMessage(`{"name":"test","target":"README.md"}`),
			},
		}},
		{{Kind: model.StreamText, Text: "verification failed as expected"}},
	}}
	meter := &recordingMeter{}
	stream, err := Query(context.Background(), "verify README", Options{
		Model: fake,
		Tools: tool.NewRegistry(verifyTool),
		Meter: meter,
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	var events []Event
	for event := range stream {
		if event.Kind == EventError {
			t.Fatalf("unexpected error event: %v", event.Err)
		}
		events = append(events, event)
	}
	verification := findVerificationEvent(events)
	if verification == nil || verification.Name != "test" || verification.Passed || verification.Diagnostics != 1 || !sameStrings(verification.Paths, []string{"README.md"}) {
		t.Fatalf("verification event = %#v", verification)
	}
	if !meter.hasCounter("memax.verification.run") {
		t.Fatalf("meter counters = %#v, missing verification counter", meter.counterNames())
	}
}

func TestQueryEmitsReadCommandOutputEventAndMetric(t *testing.T) {
	manager := commandtools.NewScriptedSessionManager(commandtools.ScriptedCommand{
		ID:  "watch-1",
		PID: 5152,
		Pages: []commandtools.ScriptedOutputPage{{
			Chunks: []commandtools.OutputChunk{{
				Seq:    1,
				Stream: "stdout",
				Text:   "watch: ok\n",
			}},
			Running:  false,
			ExitCode: intPtr(0),
		}},
	})
	fake := &fakeModel{turns: [][]model.StreamEvent{
		{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "start-1",
				Name:  commandtools.StartToolName,
				Input: json.RawMessage(`{"id":"watch-1","command":["npm","run","test:watch"],"purpose":"start watch mode"}`),
			},
		}},
		{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "read-1",
				Name:  commandtools.ReadOutputToolName,
				Input: json.RawMessage(`{"id":"watch-1"}`),
			},
		}},
		{{Kind: model.StreamText, Text: "watch completed"}},
	}}
	meter := &attributeRecordingMeter{}
	stream, err := Query(context.Background(), "start and read watch output", Options{
		Model: fake,
		Tools: tool.NewRegistry(
			commandtools.NewStartTool(manager),
			commandtools.NewReadOutputTool(manager),
		),
		Meter: meter,
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}

	var events []Event
	for event := range stream {
		if event.Kind == EventError {
			t.Fatalf("unexpected error event: %v", event.Err)
		}
		events = append(events, event)
	}

	output := findCommandEvent(events, EventCommandOutput)
	if output == nil {
		t.Fatalf("command output event missing from %#v", events)
	}
	if output.Operation != "read" {
		t.Fatalf("command output event operation = %q, want read", output.Operation)
	}
	if output.OutputChunks != 1 || output.NextSeq != 2 || output.ResumeAfterSeq != 1 {
		t.Fatalf("command output event = %#v, want one chunk, next_seq=2, and resume_after_seq=1", output)
	}
	if !meter.hasAddWithAttribute("memax.command.output", "memax.command.operation", "read") {
		t.Fatalf("meter adds = %#v, want memax.command.output with memax.command.operation=read", meter.snapshotAdds())
	}
}

func TestQueryEmitsWaitCommandOutputEventAndMetric(t *testing.T) {
	manager := commandtools.NewScriptedSessionManager(commandtools.ScriptedCommand{
		ID:  "watch-1",
		PID: 5152,
		Pages: []commandtools.ScriptedOutputPage{{
			Chunks: []commandtools.OutputChunk{{
				Seq:    1,
				Stream: "stdout",
				Text:   "watch: ok\n",
			}},
			Running:  false,
			ExitCode: intPtr(0),
		}},
	})
	fake := &fakeModel{turns: [][]model.StreamEvent{
		{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "start-1",
				Name:  commandtools.StartToolName,
				Input: json.RawMessage(`{"id":"watch-1","command":["npm","run","test:watch"],"purpose":"start watch mode"}`),
			},
		}},
		{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "wait-1",
				Name:  commandtools.WaitOutputToolName,
				Input: json.RawMessage(`{"id":"watch-1","timeout_ms":0}`),
			},
		}},
		{{Kind: model.StreamText, Text: "watch completed"}},
	}}
	meter := &attributeRecordingMeter{}
	stream, err := Query(context.Background(), "start and wait for watch output", Options{
		Model: fake,
		Tools: tool.NewRegistry(
			commandtools.NewStartTool(manager),
			commandtools.NewWaitTool(manager),
		),
		Meter: meter,
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}

	var events []Event
	for event := range stream {
		if event.Kind == EventError {
			t.Fatalf("unexpected error event: %v", event.Err)
		}
		events = append(events, event)
	}

	output := findCommandEvent(events, EventCommandOutput)
	if output == nil {
		t.Fatalf("command output event missing from %#v", events)
	}
	if output.Operation != "wait" {
		t.Fatalf("command output event operation = %q, want wait", output.Operation)
	}
	if output.OutputChunks != 1 || output.NextSeq != 2 || output.ResumeAfterSeq != 1 {
		t.Fatalf("command output event = %#v, want one chunk, next_seq=2, and resume_after_seq=1", output)
	}
	if !meter.hasAddWithAttribute("memax.command.output", "memax.command.operation", "wait") {
		t.Fatalf("meter adds = %#v, want memax.command.output with memax.command.operation=wait", meter.snapshotAdds())
	}
}

func TestQueryProgressiveSkillResourceCanLoadBeforeSkillWithMetadata(t *testing.T) {
	fake := &fakeModel{turns: [][]model.StreamEvent{
		{
			{
				Kind: model.StreamToolUse,
				ToolUse: model.ToolUse{
					ID:    "resource-1",
					Name:  skill.ResourceToolName,
					Input: json.RawMessage(`{"skill_name":"database-review","resource":"migration-checklist"}`),
				},
			},
		},
		{{Kind: model.StreamText, Text: "reviewed with direct resource"}},
	}}
	source := skill.StaticSource{{
		Name:     "database-review",
		AlwaysOn: true,
		Resources: []skill.ResourceRef{{
			Name: "migration-checklist",
			Path: "resources/migration-checklist.md",
		}},
	}}
	resources := skill.StaticResourceSource{{
		SkillName: "database-review",
		Name:      "migration-checklist",
		Content:   "Direct resource content.",
	}}

	events, err := Query(context.Background(), "review SQL migration", Options{
		Model:               fake,
		SkillSource:         source,
		SkillResourceSource: resources,
		SkillDisclosure:     skill.DisclosureProgressive,
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	result, err := Drain(events)
	if err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if result != "reviewed with direct resource" {
		t.Fatalf("result = %q, want reviewed with direct resource", result)
	}
	last := fake.requests[1].Messages[len(fake.requests[1].Messages)-1]
	if last.ToolResult == nil || last.ToolResult.Name != skill.ResourceToolName {
		t.Fatalf("last message = %#v, want resource result", last)
	}
	if last.ToolResult.Metadata["skill_loaded"] != false {
		t.Fatalf("resource metadata = %#v, want skill_loaded false", last.ToolResult.Metadata)
	}
}

func TestQueryFeedsHookDenialBackToModel(t *testing.T) {
	registry := tool.NewRegistry(tool.Definition{
		ToolSpec: model.ToolSpec{Name: "write"},
		Handler: func(context.Context, tool.Call) (model.ToolResult, error) {
			t.Fatal("handler should not run after hook denial")
			return model.ToolResult{}, nil
		},
	})
	hooks := hook.NewRunner(hook.WithBeforeToolUse(func(context.Context, hook.BeforeToolUseInput) (hook.BeforeToolUseResult, error) {
		return hook.BeforeToolUseResult{DenyReason: "writes are disabled"}, nil
	}))
	fake := &fakeModel{turns: [][]model.StreamEvent{
		{
			{
				Kind: model.StreamToolUse,
				ToolUse: model.ToolUse{
					ID:    "tool-1",
					Name:  "write",
					Input: json.RawMessage(`{}`),
				},
			},
		},
		{{Kind: model.StreamText, Text: "recovered"}},
	}}

	events, err := Query(context.Background(), "write the file", Options{
		Model: fake,
		Tools: registry,
		Hooks: hooks,
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	result, err := Drain(events)
	if err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if result != "recovered" {
		t.Fatalf("result = %q, want recovered", result)
	}
	last := fake.requests[1].Messages[len(fake.requests[1].Messages)-1]
	if last.ToolResult == nil || !last.ToolResult.IsError || last.ToolResult.Content != "writes are disabled" {
		t.Fatalf("last message before recovery = %#v, want hook denial tool error", last)
	}
}

func TestQueryPropagatesTenantToModelAndValidation(t *testing.T) {
	registry := tool.NewRegistry(tool.Definition{
		ToolSpec: model.ToolSpec{Name: "read", ReadOnly: true},
		Handler: func(context.Context, tool.Call) (model.ToolResult, error) {
			return model.ToolResult{Content: "ok"}, nil
		},
	})
	scope := tenant.Scope{
		ID:        "tenant-1",
		SubjectID: "user-1",
		Attributes: map[string]string{
			"region": "us",
		},
	}
	var requests []tenant.Request
	fake := &fakeModel{turns: [][]model.StreamEvent{
		{{Kind: model.StreamToolUse, ToolUse: model.ToolUse{ID: "tool-1", Name: "read", Input: json.RawMessage(`{}`)}}},
		{{Kind: model.StreamText, Text: "done"}},
	}}

	events, err := Query(context.Background(), "start", Options{
		Model:  fake,
		Tools:  registry,
		Tenant: scope,
		TenantValidator: tenant.ValidatorFunc(func(_ context.Context, req tenant.Request) error {
			requests = append(requests, req)
			return nil
		}),
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	result, err := Drain(events)
	if err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if result != "done" {
		t.Fatalf("result = %q, want done", result)
	}
	if len(fake.requests) != 2 {
		t.Fatalf("len(fake.requests) = %d, want 2", len(fake.requests))
	}
	for i, req := range fake.requests {
		if req.Tenant.ID != "tenant-1" || req.Tenant.SubjectID != "user-1" || req.Tenant.Attributes["region"] != "us" {
			t.Fatalf("request %d tenant = %#v, want propagated scope", i, req.Tenant)
		}
	}
	if len(requests) != 4 {
		t.Fatalf("len(tenant requests) = %d, want 4", len(requests))
	}
	if requests[0].Boundary != tenant.BoundarySessionStart {
		t.Fatalf("requests[0].Boundary = %q, want session_start", requests[0].Boundary)
	}
	if requests[1].Boundary != tenant.BoundaryModelRequest {
		t.Fatalf("requests[1].Boundary = %q, want model_request", requests[1].Boundary)
	}
	if requests[2].Boundary != tenant.BoundaryToolUse || requests[2].ToolName != "read" || !requests[2].ToolReadOnly {
		t.Fatalf("requests[2] = %#v, want read-only tool_use", requests[2])
	}
	if requests[3].Boundary != tenant.BoundaryModelRequest {
		t.Fatalf("requests[3].Boundary = %q, want model_request", requests[3].Boundary)
	}
}

func TestQueryTenantValidatorCanDenySessionStart(t *testing.T) {
	_, err := Query(context.Background(), "start", Options{
		Model:  &fakeModel{},
		Tenant: tenant.Scope{ID: "tenant-1"},
		TenantValidator: tenant.ValidatorFunc(func(_ context.Context, req tenant.Request) error {
			if req.Boundary == tenant.BoundarySessionStart {
				return errors.New("tenant mismatch")
			}
			return nil
		}),
	})
	if err == nil || err.Error() != "tenant validation failed: tenant mismatch" {
		t.Fatalf("Query error = %v, want tenant validation failure", err)
	}
}

func TestQueryAsyncEmitsTenantDeniedBeforeStartupError(t *testing.T) {
	events := QueryAsync(context.Background(), "start", Options{
		Model:  &fakeModel{},
		Tenant: tenant.Scope{ID: "tenant-1", SubjectID: "user-1"},
		TenantValidator: tenant.ValidatorFunc(func(_ context.Context, req tenant.Request) error {
			if req.Boundary == tenant.BoundarySessionStart {
				return errors.New("tenant mismatch")
			}
			return nil
		}),
	})

	var got []Event
	for event := range events {
		got = append(got, event)
	}
	if len(got) != 2 {
		t.Fatalf("len(events) = %d, want 2", len(got))
	}
	if got[0].Kind != EventTenantDenied {
		t.Fatalf("events[0].Kind = %q, want tenant_denied", got[0].Kind)
	}
	if got[0].Tenant == nil || got[0].Tenant.Boundary != "session_start" || got[0].Tenant.TenantID != "tenant-1" || got[0].Tenant.SubjectID != "user-1" || got[0].Tenant.Reason != "tenant mismatch" {
		t.Fatalf("tenant event = %#v, want structured startup denial", got[0].Tenant)
	}
	if got[1].Kind != EventError || got[1].Err == nil || got[1].Err.Error() != "tenant validation failed: tenant mismatch" {
		t.Fatalf("events[1] = %#v, want startup error", got[1])
	}
}

func TestQueryAsyncObserverSeesStartupTenantDenialOnce(t *testing.T) {
	var observed []Event
	ctx := WithEventObserver(context.Background(), EventObserverFunc(func(_ context.Context, event Event) {
		observed = append(observed, event)
	}))
	events := QueryAsync(ctx, "start", Options{
		Model:  &fakeModel{},
		Tenant: tenant.Scope{ID: "tenant-1", SubjectID: "user-1"},
		TenantValidator: tenant.ValidatorFunc(func(_ context.Context, req tenant.Request) error {
			if req.Boundary == tenant.BoundarySessionStart {
				return errors.New("tenant mismatch")
			}
			return nil
		}),
	})
	for range events {
	}
	if len(observed) != 2 {
		t.Fatalf("len(observed) = %d, want 2", len(observed))
	}
	if observed[0].Kind != EventTenantDenied || observed[0].Tenant == nil || observed[0].Tenant.Boundary != "session_start" {
		t.Fatalf("observed[0] = %#v, want tenant denial event", observed[0])
	}
	if observed[1].Kind != EventError || observed[1].Err == nil || observed[1].Err.Error() != "tenant mismatch" {
		t.Fatalf("observed[1] = %#v, want startup error event", observed[1])
	}
}

func TestQueryObserverSeesStartupTenantDenial(t *testing.T) {
	var observed []Event
	ctx := WithEventObserver(context.Background(), EventObserverFunc(func(_ context.Context, event Event) {
		observed = append(observed, event)
	}))

	_, err := Query(ctx, "start", Options{
		Model:  &fakeModel{},
		Tenant: tenant.Scope{ID: "tenant-1", SubjectID: "user-1"},
		TenantValidator: tenant.ValidatorFunc(func(_ context.Context, req tenant.Request) error {
			if req.Boundary == tenant.BoundarySessionStart {
				return errors.New("tenant mismatch")
			}
			return nil
		}),
	})
	if err == nil || err.Error() != "tenant validation failed: tenant mismatch" {
		t.Fatalf("Query error = %v, want tenant validation failure", err)
	}
	if len(observed) != 2 {
		t.Fatalf("len(observed) = %d, want 2", len(observed))
	}
	if observed[0].Kind != EventTenantDenied || observed[0].Tenant == nil || observed[0].Tenant.Boundary != "session_start" {
		t.Fatalf("observed[0] = %#v, want tenant denial event", observed[0])
	}
	if observed[1].Kind != EventError || observed[1].Err == nil || observed[1].Err.Error() != "tenant mismatch" {
		t.Fatalf("observed[1] = %#v, want startup error event", observed[1])
	}
}

func TestWithEventObserverComposesExistingObserver(t *testing.T) {
	var first []EventKind
	var second []EventKind
	ctx := WithEventObserver(context.Background(), EventObserverFunc(func(_ context.Context, event Event) {
		first = append(first, event.Kind)
	}))
	ctx = WithEventObserver(ctx, EventObserverFunc(func(_ context.Context, event Event) {
		second = append(second, event.Kind)
	}))

	events, err := Query(ctx, "start", Options{
		Model: &fakeModel{turns: [][]model.StreamEvent{
			{{Kind: model.StreamText, Text: "done"}},
		}},
	})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	for range events {
	}
	if len(first) == 0 || len(second) == 0 {
		t.Fatalf("first = %#v second = %#v, want both observers to receive events", first, second)
	}
	if len(first) != len(second) {
		t.Fatalf("first = %#v second = %#v, want matching event counts", first, second)
	}
}

func TestQueryAppliesContextPolicyBeforeModelRequest(t *testing.T) {
	fake := &fakeModel{turns: [][]model.StreamEvent{
		{{Kind: model.StreamToolUse, ToolUse: model.ToolUse{ID: "tool-1", Name: "noop", Input: json.RawMessage(`{}`)}}},
		{{Kind: model.StreamToolUse, ToolUse: model.ToolUse{ID: "tool-2", Name: "noop", Input: json.RawMessage(`{}`)}}},
		{{Kind: model.StreamText, Text: "done"}},
	}}
	registry := tool.NewRegistry(tool.Definition{
		ToolSpec: model.ToolSpec{Name: "noop"},
		Handler: func(context.Context, tool.Call) (model.ToolResult, error) {
			return model.ToolResult{Content: "ok"}, nil
		},
	})

	events, err := Query(context.Background(), "start", Options{
		Model:   fake,
		Tools:   registry,
		Context: contextwindow.RecentMessages{MaxMessages: 2},
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	for event := range events {
		if event.Kind == EventError {
			t.Fatalf("query error: %v", event.Err)
		}
	}

	lastRequest := fake.requests[len(fake.requests)-1]
	if len(lastRequest.Messages) != 2 {
		t.Fatalf("len(messages) = %d, want 2", len(lastRequest.Messages))
	}
	if lastRequest.Messages[0].Role == model.RoleTool {
		t.Fatalf("context policy left orphan tool result first: %#v", lastRequest.Messages)
	}
}

func TestQueryRetriesContextWindowErrorWithRetryPolicy(t *testing.T) {
	fake := &fakeModel{
		turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "done"}}},
	}
	retryModel := &contextRetryModel{fake: fake}
	registry := tool.NewRegistry(
		tool.Definition{ToolSpec: model.ToolSpec{Name: "read_file"}},
		tool.Definition{ToolSpec: model.ToolSpec{Name: "write_file"}},
	)
	selectCalls := 0
	events, err := Query(context.Background(), "start", Options{
		Model:        retryModel,
		Tools:        registry,
		ContextRetry: replaceContextPolicy{text: "compact"},
		ToolSelector: tool.SelectorFunc(func(_ context.Context, _ *tool.Registry, req tool.SelectRequest) ([]model.ToolSpec, error) {
			selectCalls++
			if len(req.Messages) > 0 && strings.Contains(req.Messages[0].PlainText(), "compact") {
				return []model.ToolSpec{{Name: "read_file"}}, nil
			}
			return []model.ToolSpec{{Name: "write_file"}}, nil
		}),
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	var contextEvent *ContextEvent
	result, err := drainWithContextEvent(events, &contextEvent)
	if err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if result != "done" {
		t.Fatalf("result = %q, want done", result)
	}
	if len(retryModel.requests) != 2 {
		t.Fatalf("model requests = %d, want retry", len(retryModel.requests))
	}
	if contextEvent == nil || contextEvent.OriginalMessages != 1 || contextEvent.SentMessages != 1 {
		t.Fatalf("context event = %#v, want retry context event", contextEvent)
	}
	if retryModel.requests[1].Messages[0].PlainText() != "compact" {
		t.Fatalf("retry messages = %#v, want compacted prompt", retryModel.requests[1].Messages)
	}
	if selectCalls != 2 {
		t.Fatalf("selector calls = %d, want original and retry selection", selectCalls)
	}
	if got := requestToolNames(retryModel.requests[0]); !sameStrings(got, []string{"write_file"}) {
		t.Fatalf("original retry tools = %#v, want write_file", got)
	}
	if got := requestToolNames(retryModel.requests[1]); !sameStrings(got, []string{"read_file"}) {
		t.Fatalf("compacted retry tools = %#v, want read_file", got)
	}
}

func drainWithContextEvent(events <-chan Event, contextEvent **ContextEvent) (string, error) {
	for event := range events {
		switch event.Kind {
		case EventContextApplied:
			*contextEvent = event.Context
		case EventResult:
			return event.Result, nil
		case EventError:
			return "", event.Err
		}
	}
	return "", nil
}

func TestQueryEmitsContextAppliedEvent(t *testing.T) {
	fake := &fakeModel{turns: [][]model.StreamEvent{
		{{Kind: model.StreamToolUse, ToolUse: model.ToolUse{ID: "tool-1", Name: "noop", Input: json.RawMessage(`{}`)}}},
		{{Kind: model.StreamText, Text: "done"}},
	}}
	registry := tool.NewRegistry(tool.Definition{
		ToolSpec: model.ToolSpec{Name: "noop"},
		Handler: func(context.Context, tool.Call) (model.ToolResult, error) {
			return model.ToolResult{Content: "ok"}, nil
		},
	})

	events, err := Query(context.Background(), "start", Options{
		Model:   fake,
		Tools:   registry,
		Context: contextwindow.RecentMessages{MaxMessages: 2},
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}

	var contextEvent *ContextEvent
	for event := range events {
		if event.Kind == EventContextApplied {
			contextEvent = event.Context
		}
		if event.Kind == EventError {
			t.Fatalf("query error: %v", event.Err)
		}
	}
	if contextEvent == nil {
		t.Fatal("missing context applied event")
	}
	if contextEvent.OriginalMessages != 3 || contextEvent.SentMessages != 2 {
		t.Fatalf("context event = %#v, want 3 -> 2", contextEvent)
	}
}

func TestQueryEmitsContextAppliedEventWhenMessageCountIsUnchanged(t *testing.T) {
	fake := &fakeModel{turns: [][]model.StreamEvent{
		{{Kind: model.StreamText, Text: "done"}},
	}}

	events, err := Query(context.Background(), "start", Options{
		Model:   fake,
		Context: replaceContextPolicy{text: "summary"},
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}

	var contextEvent *ContextEvent
	for event := range events {
		if event.Kind == EventContextApplied {
			contextEvent = event.Context
		}
		if event.Kind == EventError {
			t.Fatalf("query error: %v", event.Err)
		}
	}
	if contextEvent == nil {
		t.Fatal("missing context applied event")
	}
	if contextEvent.OriginalMessages != 1 || contextEvent.SentMessages != 1 {
		t.Fatalf("context event = %#v, want 1 -> 1", contextEvent)
	}
	if len(fake.requests) != 1 || fake.requests[0].Messages[0].PlainText() != "summary" {
		t.Fatalf("model request = %#v", fake.requests)
	}
}

func TestQueryStartsTracingSpans(t *testing.T) {
	tracer := &recordingTracer{}
	events, err := Query(context.Background(), "start", Options{
		Model: &fakeModel{turns: [][]model.StreamEvent{
			{{Kind: model.StreamText, Text: "done"}},
		}},
		Tracer: tracer,
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if _, err := Drain(events); err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}

	for _, name := range []string{"memaxagent.query", "memaxagent.turn", "memaxagent.model.stream"} {
		if !tracer.hasSpan(name) {
			t.Fatalf("missing span %q in %#v", name, tracer.names())
		}
	}
}

func TestQueryRecordsMetrics(t *testing.T) {
	meter := &recordingMeter{}
	events, err := Query(context.Background(), "start", Options{
		Model: &fakeModel{turns: [][]model.StreamEvent{
			{{Kind: model.StreamText, Text: "done"}},
		}},
		Meter: meter,
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	for event := range events {
		if event.Kind == EventError {
			t.Fatalf("query error: %v", event.Err)
		}
	}

	for _, name := range []string{"memax.query.started", "memax.turn.started", "memax.model.stream.started", "memax.query.completed"} {
		if !meter.hasCounter(name) {
			t.Fatalf("missing counter %q in %#v", name, meter.counterNames())
		}
	}
	if !meter.hasRecord("memax.model.stream.duration_ms") || !meter.hasRecord("memax.turn.duration_ms") {
		t.Fatalf("missing duration records in %#v", meter.recordNames())
	}
}

func TestQueryAppliesToolSelectorBeforeModelRequest(t *testing.T) {
	fake := &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "done"}}}}
	registry := tool.NewRegistry(
		tool.Definition{ToolSpec: model.ToolSpec{Name: "search_tools", AlwaysLoad: true}},
		tool.Definition{ToolSpec: model.ToolSpec{Name: "read_file", SearchHint: "read workspace file", ShouldDefer: true}},
		tool.Definition{ToolSpec: model.ToolSpec{Name: "write_file", SearchHint: "write workspace file", ShouldDefer: true}},
	)

	events, err := Query(context.Background(), "read the workspace", Options{
		Model:        fake,
		Tools:        registry,
		ToolSelector: tool.SearchSelector{},
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if _, err := Drain(events); err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if len(fake.requests) != 1 {
		t.Fatalf("model requests = %d, want 1", len(fake.requests))
	}
	got := requestToolNames(fake.requests[0])
	want := []string{"search_tools", "read_file"}
	if !sameStrings(got, want) {
		t.Fatalf("tools = %#v, want %#v", got, want)
	}
}

func TestQueryBuildsPromptFromIdentityAndSkills(t *testing.T) {
	fake := &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "done"}}}}
	events, err := Query(context.Background(), "review the SQL migration", Options{
		Model: fake,
		Identity: identity.Identity{
			Name:    "reviewer",
			Mission: "find correctness risks",
		},
		Skills: []skill.Skill{{
			Name:        "database-review",
			Description: "SQL migration review",
			Content:     "Check locking and rollback behavior.",
		}},
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if _, err := Drain(events); err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if len(fake.requests) != 1 {
		t.Fatalf("model requests = %d, want 1", len(fake.requests))
	}
	system := fake.requests[0].SystemPrompt
	for _, want := range []string{"reviewer", "find correctness risks", "database-review", "Check locking"} {
		if !strings.Contains(system, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, system)
		}
	}
	if fake.requests[0].AppendSystemPrompt != "" {
		t.Fatalf("AppendSystemPrompt = %q, want empty after prompt assembly", fake.requests[0].AppendSystemPrompt)
	}
}

func TestQueryLoadsSkillsFromSource(t *testing.T) {
	fake := &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "done"}}}}
	events, err := Query(context.Background(), "inspect auth code", Options{
		Model: fake,
		SkillSource: skill.StaticSource{{
			Name:        "security-review",
			Description: "auth and access control review",
			Content:     "Check authorization boundaries.",
		}},
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if _, err := Drain(events); err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if len(fake.requests) != 1 || !strings.Contains(fake.requests[0].SystemPrompt, "security-review") {
		t.Fatalf("system prompt = %q, want loaded skill", fake.requests[0].SystemPrompt)
	}
}

func TestQueryValidatesStructuredOutput(t *testing.T) {
	fake := &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: `{"answer":"done"}`}}}}
	events, err := Query(context.Background(), "start", Options{
		Model:  fake,
		Output: answerOutputContract(),
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	result, err := Drain(events)
	if err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if result != `{"answer":"done"}` {
		t.Fatalf("result = %q, want structured JSON", result)
	}
	if len(fake.requests) != 1 {
		t.Fatalf("model requests = %d, want 1", len(fake.requests))
	}
	if !strings.Contains(fake.requests[0].SystemPrompt, "Final answer contract") {
		t.Fatalf("system prompt = %q, want output contract guidance", fake.requests[0].SystemPrompt)
	}
}

func TestQueryEmitsUsageAndAggregatesOnResult(t *testing.T) {
	meter := &recordingMeter{}
	fake := &fakeModel{turns: [][]model.StreamEvent{{{
		Kind: model.StreamText,
		Text: "done",
	}, {
		Kind: model.StreamUsage,
		Usage: &model.Usage{
			Provider:     "test",
			Model:        "fake",
			InputTokens:  3,
			OutputTokens: 5,
			TotalTokens:  8,
		},
	}}}}
	events, err := Query(context.Background(), "start", Options{
		Model: fake,
		Meter: meter,
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	var usageEvent *model.Usage
	var resultEvent *model.Usage
	for event := range events {
		if event.Kind == EventError {
			t.Fatalf("query error: %v", event.Err)
		}
		switch event.Kind {
		case EventUsage:
			usageEvent = event.Usage
		case EventResult:
			resultEvent = event.Usage
		}
	}
	if usageEvent == nil || usageEvent.InputTokens != 3 || usageEvent.OutputTokens != 5 || usageEvent.TotalTokens != 8 {
		t.Fatalf("usage event = %#v, want token counts", usageEvent)
	}
	if resultEvent == nil || resultEvent.InputTokens != 3 || resultEvent.OutputTokens != 5 || resultEvent.TotalTokens != 8 {
		t.Fatalf("result usage = %#v, want aggregate token counts", resultEvent)
	}
	for _, want := range []string{"memax.model.input_tokens", "memax.model.output_tokens", "memax.model.total_tokens"} {
		if !meter.hasCounter(want) {
			t.Fatalf("meter counters = %#v, missing %s", meter.counterNames(), want)
		}
	}
}

func TestQueryBudgetStopsBeforeSecondModelCall(t *testing.T) {
	registry := tool.NewRegistry(tool.Definition{
		ToolSpec: model.ToolSpec{Name: "lookup", ReadOnly: true},
		Handler: func(context.Context, tool.Call) (model.ToolResult, error) {
			return model.ToolResult{Content: "tool result"}, nil
		},
	})
	stopReasons := make(chan hook.StopReason, 1)
	hooks := hook.NewRunner(hook.WithStop(func(_ context.Context, input hook.StopInput) error {
		stopReasons <- input.Reason
		return nil
	}))
	fake := &fakeModel{turns: [][]model.StreamEvent{
		{{Kind: model.StreamToolUse, ToolUse: model.ToolUse{ID: "tool-1", Name: "lookup", Input: json.RawMessage(`{}`)}}},
		{{Kind: model.StreamText, Text: "should not run"}},
	}}

	events, err := Query(context.Background(), "start", Options{
		Model:  fake,
		Tools:  registry,
		Hooks:  hooks,
		Budget: budget.Policy{MaxModelCalls: 1},
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	var budgetErr error
	for event := range events {
		if event.Kind == EventError {
			budgetErr = event.Err
		}
	}
	if budgetErr == nil || !strings.Contains(budgetErr.Error(), "max model calls") {
		t.Fatalf("budget error = %v, want max model calls budget error", budgetErr)
	}
	if len(fake.requests) != 1 {
		t.Fatalf("model requests = %d, want 1", len(fake.requests))
	}
	select {
	case stopReason := <-stopReasons:
		if stopReason != hook.StopReasonBudget {
			t.Fatalf("stop reason = %q, want budget", stopReason)
		}
	default:
		t.Fatal("missing stop hook call")
	}
}

func TestQueryBudgetStopsBeforeToolBatch(t *testing.T) {
	runCount := 0
	registry := tool.NewRegistry(tool.Definition{
		ToolSpec: model.ToolSpec{Name: "lookup", ReadOnly: true},
		Handler: func(context.Context, tool.Call) (model.ToolResult, error) {
			runCount++
			return model.ToolResult{Content: "tool result"}, nil
		},
	})
	fake := &fakeModel{turns: [][]model.StreamEvent{{{
		Kind:    model.StreamToolUse,
		ToolUse: model.ToolUse{ID: "tool-1", Name: "lookup", Input: json.RawMessage(`{}`)},
	}, {
		Kind:    model.StreamToolUse,
		ToolUse: model.ToolUse{ID: "tool-2", Name: "lookup", Input: json.RawMessage(`{}`)},
	}}}}

	events, err := Query(context.Background(), "start", Options{
		Model:  fake,
		Tools:  registry,
		Budget: budget.Policy{MaxToolCalls: 1},
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if _, err := Drain(events); err == nil || !strings.Contains(err.Error(), "max tool calls") {
		t.Fatalf("Drain error = %v, want max tool calls budget error", err)
	}
	if runCount != 0 {
		t.Fatalf("tool handler ran %d times, want 0", runCount)
	}
}

func TestQueryBudgetStopsAfterTokenUsage(t *testing.T) {
	fake := &fakeModel{turns: [][]model.StreamEvent{{{
		Kind: model.StreamText,
		Text: "done",
	}, {
		Kind:  model.StreamUsage,
		Usage: &model.Usage{InputTokens: 6, OutputTokens: 5, TotalTokens: 11},
	}}}}

	events, err := Query(context.Background(), "start", Options{
		Model:  fake,
		Budget: budget.Policy{MaxTotalTokens: 10},
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if _, err := Drain(events); err == nil || !strings.Contains(err.Error(), "max total tokens") {
		t.Fatalf("Drain error = %v, want max total tokens budget error", err)
	}
	if len(fake.requests) != 1 {
		t.Fatalf("model requests = %d, want 1", len(fake.requests))
	}
}

func TestQueryRetriesInvalidStructuredOutput(t *testing.T) {
	store := session.NewMemoryStore()
	fake := &fakeModel{turns: [][]model.StreamEvent{
		{{Kind: model.StreamText, Text: `not json`}},
		{{Kind: model.StreamText, Text: `{"answer":"fixed"}`}},
	}}
	events, err := Query(context.Background(), "start", Options{
		Model:    fake,
		Sessions: store,
		Output:   answerOutputContract(),
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	var sessionID string
	var result string
	for event := range events {
		if event.SessionID != "" {
			sessionID = event.SessionID
		}
		if event.Kind == EventError {
			t.Fatalf("query error: %v", event.Err)
		}
		if event.Kind == EventResult {
			result = event.Result
		}
	}
	if result != `{"answer":"fixed"}` {
		t.Fatalf("result = %q, want repaired JSON", result)
	}
	if len(fake.requests) != 2 {
		t.Fatalf("model requests = %d, want retry", len(fake.requests))
	}
	if len(fake.requests[1].Messages) < 3 || !strings.Contains(fake.requests[1].Messages[len(fake.requests[1].Messages)-1].PlainText(), "structured output contract") {
		t.Fatalf("retry messages = %#v, want validation retry prompt", fake.requests[1].Messages)
	}
	messages, err := store.Messages(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("Messages returned error: %v", err)
	}
	if len(messages) < 3 || messages[1].PlainText() != "not json" || !strings.Contains(messages[2].PlainText(), "not valid JSON") {
		t.Fatalf("session messages = %#v, want invalid answer and retry prompt", messages)
	}
}

func TestQueryStructuredOutputRetryUsesDefaultSessionStore(t *testing.T) {
	fake := &fakeModel{turns: [][]model.StreamEvent{
		{{Kind: model.StreamText, Text: `not json`}},
		{{Kind: model.StreamText, Text: `{"answer":"fixed"}`}},
	}}
	events, err := Query(context.Background(), "start", Options{
		Model:  fake,
		Output: answerOutputContract(),
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if _, err := Drain(events); err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if len(fake.requests) != 2 {
		t.Fatalf("model requests = %d, want retry", len(fake.requests))
	}
	retryMessages := fake.requests[1].Messages
	if len(retryMessages) < 3 {
		t.Fatalf("retry messages = %#v, want user, invalid assistant, repair prompt", retryMessages)
	}
	if retryMessages[1].Role != model.RoleAssistant || retryMessages[1].PlainText() != "not json" {
		t.Fatalf("retry assistant message = %#v, want invalid assistant persisted", retryMessages[1])
	}
	if retryMessages[2].Role != model.RoleUser || !strings.Contains(retryMessages[2].PlainText(), "not valid JSON") {
		t.Fatalf("retry prompt message = %#v, want validation repair prompt", retryMessages[2])
	}
}

func TestQueryStructuredOutputExhaustionStopsRun(t *testing.T) {
	fake := &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: `not json`}}}}
	events, err := Query(context.Background(), "start", Options{
		Model:  fake,
		Output: output.Contract{Schema: answerOutputContract().Schema, MaxRetries: -1},
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if _, err := Drain(events); err == nil || !strings.Contains(err.Error(), "validate structured output") {
		t.Fatalf("Drain error = %v, want structured output validation error", err)
	}
	if len(fake.requests) != 1 {
		t.Fatalf("model requests = %d, want no retry", len(fake.requests))
	}
}

func TestQueryBeforeFinalDenialExhaustionStopsRun(t *testing.T) {
	fake := &fakeModel{turns: [][]model.StreamEvent{
		{{Kind: model.StreamText, Text: "not yet"}},
		{{Kind: model.StreamText, Text: "still not"}},
	}}
	var stopReason hook.StopReason
	hooks := hook.NewRunner(
		hook.WithBeforeFinal(func(context.Context, hook.BeforeFinalInput) (hook.BeforeFinalResult, error) {
			return hook.BeforeFinalResult{DenyReason: "run verification first"}, nil
		}),
		hook.WithStop(func(_ context.Context, input hook.StopInput) error {
			stopReason = input.Reason
			return nil
		}),
	)
	events, err := Query(context.Background(), "start", Options{
		Model:           fake,
		Hooks:           hooks,
		MaxFinalDenials: 1,
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	var gotErr error
	for event := range events {
		if event.Kind == EventError {
			gotErr = event.Err
		}
	}
	if gotErr == nil || !strings.Contains(gotErr.Error(), "finalization denied after 1 retries") || !strings.Contains(gotErr.Error(), "run verification first") {
		t.Fatalf("event error = %v, want finalization denial exhaustion", gotErr)
	}
	if len(fake.requests) != 2 {
		t.Fatalf("model requests = %d, want one retry then exhaustion", len(fake.requests))
	}
	if stopReason != hook.StopReasonPolicy {
		t.Fatalf("stop reason = %q, want policy", stopReason)
	}
	retryMessages := fake.requests[1].Messages
	if len(retryMessages) < 3 || !strings.Contains(retryMessages[len(retryMessages)-1].PlainText(), "run verification first") {
		t.Fatalf("retry messages = %#v, want finalization repair prompt", retryMessages)
	}
}

func TestQueryBeforeFinalNegativeMaxDenialsDisablesRetry(t *testing.T) {
	fake := &fakeModel{turns: [][]model.StreamEvent{
		{{Kind: model.StreamText, Text: "not yet"}},
	}}
	events, err := Query(context.Background(), "start", Options{
		Model: fake,
		Hooks: hook.NewRunner(hook.WithBeforeFinal(func(context.Context, hook.BeforeFinalInput) (hook.BeforeFinalResult, error) {
			return hook.BeforeFinalResult{DenyReason: "blocked"}, nil
		})),
		MaxFinalDenials: -1,
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if _, err := Drain(events); err == nil || !strings.Contains(err.Error(), "finalization denied after 0 retries") {
		t.Fatalf("Drain error = %v, want immediate finalization denial exhaustion", err)
	}
	if len(fake.requests) != 1 {
		t.Fatalf("model requests = %d, want no retry", len(fake.requests))
	}
}

func TestQueryRejectsInvalidOutputSchema(t *testing.T) {
	_, err := Query(context.Background(), "start", Options{
		Model: &fakeModel{},
		Output: output.Contract{Schema: map[string]any{
			"type": "not-a-json-schema-type",
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "compile output contract") {
		t.Fatalf("Query error = %v, want output schema compile error", err)
	}
}

func TestQueryWithoutStructuredOutputAcceptsPlainText(t *testing.T) {
	fake := &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: `not json`}}}}
	events, err := Query(context.Background(), "start", Options{Model: fake})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	result, err := Drain(events)
	if err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if result != "not json" {
		t.Fatalf("result = %q, want plain text", result)
	}
}

func TestQueryLoadsMemoriesFromSource(t *testing.T) {
	fake := &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "done"}}}}
	var got memory.Request
	events, err := Query(context.Background(), "inspect billing code", Options{
		Model:           fake,
		ParentSessionID: "00000000-0000-7000-8000-000000000010",
		MemorySource: memory.SourceFunc(func(_ context.Context, req memory.Request) ([]memory.Memory, error) {
			got = req
			return []memory.Memory{{
				Name:    "billing-rules",
				Scope:   memory.ScopeProject,
				Content: "Billing changes require audit logging.",
			}}, nil
		}),
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if _, err := Drain(events); err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if got.SessionID == "" {
		t.Fatal("memory source did not receive active session id")
	}
	if got.ParentSessionID != "00000000-0000-7000-8000-000000000010" {
		t.Fatalf("memory source parent session = %q, want 00000000-0000-7000-8000-000000000010", got.ParentSessionID)
	}
	if len(got.Messages) != 1 || got.Messages[0].PlainText() != "inspect billing code" {
		t.Fatalf("memory source messages = %#v, want current messages", got.Messages)
	}
	if got.Query != "inspect billing code" {
		t.Fatalf("memory source query = %q, want prompt text", got.Query)
	}
	if len(fake.requests) != 1 || !strings.Contains(fake.requests[0].SystemPrompt, "Billing changes require audit logging.") {
		t.Fatalf("system prompt = %q, want loaded memory", fake.requests[0].SystemPrompt)
	}
}

func TestQueryLoadsMemorySourceOncePerRun(t *testing.T) {
	registry := tool.NewRegistry(tool.Definition{
		ToolSpec: model.ToolSpec{Name: "lookup"},
		Handler: func(context.Context, tool.Call) (model.ToolResult, error) {
			return model.ToolResult{Content: "tool result mentions frontend but should not reload memories"}, nil
		},
	})
	fake := &fakeModel{turns: [][]model.StreamEvent{
		{{Kind: model.StreamToolUse, ToolUse: model.ToolUse{ID: "tool-1", Name: "lookup", Input: json.RawMessage(`{}`)}}},
		{{Kind: model.StreamText, Text: "done"}},
	}}
	var calls int
	var got memory.Request
	events, err := Query(context.Background(), "inspect billing code", Options{
		Model: fake,
		Tools: registry,
		MemorySource: memory.SourceFunc(func(_ context.Context, req memory.Request) ([]memory.Memory, error) {
			calls++
			got = req
			return []memory.Memory{{Name: "billing", Scope: memory.ScopeProject, Content: "Billing changes require audit logging."}}, nil
		}),
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if _, err := Drain(events); err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("memory source calls = %d, want 1", calls)
	}
	if got.Query != "inspect billing code" {
		t.Fatalf("memory source query = %q, want user prompt only", got.Query)
	}
}

func TestQueryLoadsPlannerWithSessionContext(t *testing.T) {
	fake := &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "done"}}}}
	var got planner.Request
	events, err := Query(context.Background(), "inspect billing code", Options{
		Model:           fake,
		ParentSessionID: "00000000-0000-7000-8000-000000000010",
		Identity:        identity.Identity{Name: "planner-agent"},
		Planner: planner.PolicyFunc(func(_ context.Context, req planner.Request) (planner.Plan, error) {
			got = req
			return planner.Plan{
				Goal: "inspect billing code safely",
				Steps: []planner.Step{{
					ID:        "step-1",
					Title:     "read relevant files",
					Status:    planner.StatusInProgress,
					ToolHints: []string{"read_file"},
				}},
			}, nil
		}),
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if _, err := Drain(events); err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if got.SessionID == "" {
		t.Fatal("planner did not receive active session id")
	}
	if got.ParentSessionID != "00000000-0000-7000-8000-000000000010" {
		t.Fatalf("planner parent session = %q, want 00000000-0000-7000-8000-000000000010", got.ParentSessionID)
	}
	if got.Identity.Name != "planner-agent" {
		t.Fatalf("planner identity = %#v, want planner-agent", got.Identity)
	}
	if len(got.Messages) != 1 || got.Messages[0].PlainText() != "inspect billing code" {
		t.Fatalf("planner messages = %#v, want current prompt", got.Messages)
	}
	if got.Query != "inspect billing code" {
		t.Fatalf("planner query = %q, want user prompt", got.Query)
	}
	if len(fake.requests) != 1 || !strings.Contains(fake.requests[0].SystemPrompt, "inspect billing code safely") {
		t.Fatalf("system prompt = %q, want plan injection", fake.requests[0].SystemPrompt)
	}
}

func TestQueryPlannerErrorStopsRun(t *testing.T) {
	plannerErr := errors.New("planner unavailable")
	events, err := Query(context.Background(), "start", Options{
		Model: &fakeModel{},
		Planner: planner.PolicyFunc(func(context.Context, planner.Request) (planner.Plan, error) {
			return planner.Plan{}, plannerErr
		}),
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if _, err := Drain(events); err == nil || !strings.Contains(err.Error(), "build prompt") || !errors.Is(err, plannerErr) {
		t.Fatalf("Drain error = %v, want planner error", err)
	}
}

func TestQueryEmitsMemoryCandidatesAfterValidResult(t *testing.T) {
	fake := &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "rollback notes added"}}}}
	store := &countingMessageStore{inner: session.NewMemoryStore()}
	var got memory.DistillRequest
	events, err := Query(context.Background(), "review migration", Options{
		Model:    fake,
		Sessions: store,
		Planner: planner.Static(planner.Plan{
			Goal: "review migration",
			Steps: []planner.Step{{
				ID:     "task-1",
				Title:  "check rollback",
				Status: planner.StatusCompleted,
			}},
		}),
		MemoryDistiller: memory.DistillerFunc(func(_ context.Context, req memory.DistillRequest) ([]memory.Candidate, error) {
			got = req
			return []memory.Candidate{{
				Memory: memory.Memory{
					Name:    "migration-rollback",
					Scope:   memory.ScopeProject,
					Content: "Migration reviews require rollback notes.",
				},
				Reason:     "final answer confirmed rollback notes",
				Confidence: 0.9,
			}}, nil
		}),
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	var candidates []memory.Candidate
	var result string
	for event := range events {
		switch event.Kind {
		case EventError:
			t.Fatalf("query error: %v", event.Err)
		case EventMemoryCandidates:
			candidates = event.Memory.Candidates
		case EventResult:
			result = event.Result
		}
	}
	if result != "rollback notes added" {
		t.Fatalf("result = %q, want final result", result)
	}
	if len(candidates) != 1 || candidates[0].Memory.Name != "migration-rollback" {
		t.Fatalf("candidates = %#v, want distilled memory", candidates)
	}
	if got.SessionID == "" || got.Result != "rollback notes added" || got.Plan.Goal != "review migration" {
		t.Fatalf("distill request = %#v, want session, result, and plan", got)
	}
	if len(got.Messages) < 2 || got.Messages[len(got.Messages)-1].PlainText() != "rollback notes added" {
		t.Fatalf("distill messages = %#v, want final assistant in transcript", got.Messages)
	}
	if calls := store.messageCalls(); calls != 1 {
		t.Fatalf("session Messages calls = %d, want one load before distillation", calls)
	}
}

func TestQueryMemoryDistillerErrorStopsRun(t *testing.T) {
	distillErr := errors.New("distiller unavailable")
	events, err := Query(context.Background(), "start", Options{
		Model: &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "done"}}}},
		MemoryDistiller: memory.DistillerFunc(func(context.Context, memory.DistillRequest) ([]memory.Candidate, error) {
			return nil, distillErr
		}),
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if _, err := Drain(events); err == nil || !strings.Contains(err.Error(), "distill memories") || !errors.Is(err, distillErr) {
		t.Fatalf("Drain error = %v, want distiller error", err)
	}
}

func TestQueryMemoryCandidateHandlerPersistsAfterEvent(t *testing.T) {
	store := memory.NewMemoryStore(nil)
	releaseHandler := make(chan struct{})
	events, err := Query(context.Background(), "review migration", Options{
		Model: &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "done"}}}},
		MemoryDistiller: memory.StaticDistiller{{
			Memory: memory.Memory{
				Name:    "migration-rollback",
				Scope:   memory.ScopeProject,
				Content: "Migration reviews require rollback notes.",
			},
			Confidence: 0.9,
		}},
		MemoryCandidateHandler: memory.CandidateHandlerFunc(func(ctx context.Context, req memory.CandidateRequest) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-releaseHandler:
			}
			return memory.WriterHandler{Writer: store, MinConfidence: 0.5}.HandleCandidates(ctx, req)
		}),
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	var sawCandidates bool
	var result string
	for event := range events {
		switch event.Kind {
		case EventError:
			t.Fatalf("query error: %v", event.Err)
		case EventMemoryCandidates:
			sawCandidates = true
			items, err := store.Memories(context.Background(), memory.Request{})
			if err != nil {
				t.Fatalf("Memories returned error: %v", err)
			}
			if len(items) != 0 {
				t.Fatalf("stored memories during candidate event = %#v, want handler to run after event", items)
			}
			close(releaseHandler)
		case EventResult:
			result = event.Result
		}
	}
	if !sawCandidates {
		t.Fatal("EventMemoryCandidates not emitted")
	}
	if result != "done" {
		t.Fatalf("result = %q, want done", result)
	}
	items, err := store.Memories(context.Background(), memory.Request{})
	if err != nil {
		t.Fatalf("Memories returned error: %v", err)
	}
	if len(items) != 1 || items[0].Name != "migration-rollback" {
		t.Fatalf("stored memories = %#v, want persisted candidate", items)
	}
}

func TestQueryMemoryCandidateHandlerErrorDoesNotStopRun(t *testing.T) {
	handlerErr := errors.New("review queue unavailable")
	events, err := Query(context.Background(), "start", Options{
		Model: &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "done"}}}},
		MemoryDistiller: memory.StaticDistiller{{
			Memory:     memory.Memory{Name: "lesson", Content: "Persist me."},
			Confidence: 0.9,
		}},
		MemoryCandidateHandler: memory.CandidateHandlerFunc(func(context.Context, memory.CandidateRequest) error {
			return handlerErr
		}),
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	var gotErr error
	var result string
	for event := range events {
		switch event.Kind {
		case EventError:
			t.Fatalf("terminal query error: %v", event.Err)
		case EventMemoryCandidateHandlerError:
			gotErr = event.Err
		case EventResult:
			result = event.Result
		}
	}
	if gotErr == nil || !strings.Contains(gotErr.Error(), "handle memory candidates") || !errors.Is(gotErr, handlerErr) {
		t.Fatalf("handler event error = %v, want handler error", gotErr)
	}
	if result != "done" {
		t.Fatalf("result = %q, want done", result)
	}
}

func TestMemoryQueryUsesRecentUserMessagesOnly(t *testing.T) {
	messages := []model.Message{
		{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.ContentText, Text: "old billing context"}}},
		{Role: model.RoleTool, ToolResult: &model.ToolResult{Name: "lookup", Content: "frontend noise"}},
		{Role: model.RoleAssistant, Content: []model.ContentBlock{{Type: model.ContentText, Text: "assistant noise"}}},
		{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.ContentText, Text: "first recent"}}},
		{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.ContentText, Text: "second recent"}}},
		{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.ContentText, Text: "third recent"}}},
	}
	got := memoryQuery(messages)
	if got != "first recent second recent third recent" {
		t.Fatalf("memoryQuery = %q", got)
	}
	for _, blocked := range []string{"old billing", "frontend", "assistant"} {
		if strings.Contains(got, blocked) {
			t.Fatalf("memoryQuery = %q, want no %q", got, blocked)
		}
	}
}

func TestQueryMemorySourceErrorStopsRun(t *testing.T) {
	sourceErr := errors.New("memory store unavailable")
	events, err := Query(context.Background(), "start", Options{
		Model: &fakeModel{},
		MemorySource: memory.SourceFunc(func(context.Context, memory.Request) ([]memory.Memory, error) {
			return nil, sourceErr
		}),
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if _, err := Drain(events); err == nil || !strings.Contains(err.Error(), "build prompt") || !errors.Is(err, sourceErr) {
		t.Fatalf("Drain error = %v, want memory source error", err)
	}
}

func TestQueryToolSelectorErrorStopsRun(t *testing.T) {
	selectorErr := errors.New("selector unavailable")
	events, err := Query(context.Background(), "start", Options{
		Model: &fakeModel{},
		ToolSelector: tool.SelectorFunc(func(context.Context, *tool.Registry, tool.SelectRequest) ([]model.ToolSpec, error) {
			return nil, selectorErr
		}),
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if _, err := Drain(events); err == nil || !strings.Contains(err.Error(), "select tools") {
		t.Fatalf("Drain error = %v, want select tools error", err)
	}
}

func TestQueryRunsLifecycleHooks(t *testing.T) {
	var calls []string
	hooks := hook.NewRunner(
		hook.WithSessionStarted(func(_ context.Context, input hook.SessionStartedInput) error {
			if input.SessionID == "" {
				t.Fatal("missing session id")
			}
			calls = append(calls, "session_started")
			return nil
		}),
		hook.WithUserPrompt(func(_ context.Context, input hook.UserPromptInput) (hook.UserPromptResult, error) {
			calls = append(calls, "user_prompt")
			return hook.UserPromptResult{Prompt: input.Prompt + " rewritten"}, nil
		}),
		hook.WithStop(func(_ context.Context, input hook.StopInput) error {
			if input.Reason != hook.StopReasonResult {
				t.Fatalf("stop reason = %q, want result", input.Reason)
			}
			calls = append(calls, "stop")
			return nil
		}),
		hook.WithSessionEnded(func(_ context.Context, input hook.SessionEndedInput) error {
			if input.Reason != hook.StopReasonResult {
				t.Fatalf("session ended reason = %q, want result", input.Reason)
			}
			calls = append(calls, "session_ended")
			return nil
		}),
	)
	fake := &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "done"}}}}

	events, err := Query(context.Background(), "start", Options{
		Model: fake,
		Hooks: hooks,
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if _, err := Drain(events); err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if len(fake.requests) != 1 || fake.requests[0].Messages[0].PlainText() != "start rewritten" {
		t.Fatalf("model request = %#v", fake.requests)
	}
	want := []string{"session_started", "user_prompt", "stop", "session_ended"}
	if !sameStrings(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestQueryPropagatesParentSessionID(t *testing.T) {
	fake := &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "done"}}}}
	events, err := Query(context.Background(), "start", Options{
		Model:           fake,
		ParentSessionID: "00000000-0000-7000-8000-000000000010",
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	var started Event
	for event := range events {
		if event.Kind == EventSessionStarted {
			started = event
		}
		if event.Kind == EventError {
			t.Fatalf("query error: %v", event.Err)
		}
		if event.Kind == EventResult {
			break
		}
	}
	if started.ParentSessionID != "00000000-0000-7000-8000-000000000010" {
		t.Fatalf("started event = %#v, want parent session id", started)
	}
	if len(fake.requests) != 1 || fake.requests[0].ParentSessionID != "00000000-0000-7000-8000-000000000010" {
		t.Fatalf("model request = %#v, want parent session id", fake.requests)
	}
}

func TestQueryResumesExistingSession(t *testing.T) {
	store := session.NewMemoryStore()
	sess, err := store.Create(context.Background())
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if err := store.Append(context.Background(), sess.ID, model.Message{
		Role:    model.RoleUser,
		Content: []model.ContentBlock{{Type: model.ContentText, Text: "previous"}},
	}); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}
	fake := &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "done"}}}}

	events, err := Query(context.Background(), "next", Options{
		Model:     fake,
		Sessions:  store,
		SessionID: sess.ID,
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	var started Event
	for event := range events {
		if event.Kind == EventSessionStarted {
			started = event
		}
		if event.Kind == EventError {
			t.Fatalf("query error: %v", event.Err)
		}
		if event.Kind == EventResult {
			break
		}
	}
	if started.SessionID != sess.ID {
		t.Fatalf("started event session = %q, want resumed session %q", started.SessionID, sess.ID)
	}
	if len(fake.requests) != 1 || len(fake.requests[0].Messages) != 2 {
		t.Fatalf("model request = %#v, want previous + next messages", fake.requests)
	}
	if fake.requests[0].Messages[0].PlainText() != "previous" || fake.requests[0].Messages[1].PlainText() != "next" {
		t.Fatalf("model messages = %#v, want resumed transcript", fake.requests[0].Messages)
	}
}

func TestQueryEmitsContextCompactedEvent(t *testing.T) {
	store := session.NewMemoryStore()
	sess, err := store.Create(context.Background())
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if err := store.Append(context.Background(), sess.ID, model.Message{
		Role:    model.RoleUser,
		Content: []model.ContentBlock{{Type: model.ContentText, Text: "old-old-old-old"}},
	}); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}
	fake := &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "done"}}}}

	events, err := Query(context.Background(), "recent", Options{
		Model:     fake,
		Sessions:  store,
		SessionID: sess.ID,
		Context: contextwindow.SummarizingBudget{
			MaxTokens:        16,
			MaxSummaryTokens: 10,
			SummaryPrefix:    "S:",
			Summarizer: contextwindow.SummarizerFunc(func(context.Context, []model.Message) (string, error) {
				return "summary", nil
			}),
		},
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	var compacted *contextwindow.CompactionRecord
	for event := range events {
		if event.Kind == EventContextCompacted {
			compacted = event.Compaction
		}
		if event.Kind == EventError {
			t.Fatalf("query error: %v", event.Err)
		}
	}
	if compacted == nil {
		t.Fatal("missing context compacted event")
	}
	if compacted.OriginalMessages != 2 || compacted.SentMessages != 2 || compacted.SummaryHash == "" {
		t.Fatalf("compaction = %#v, want 2 -> 2 with summary hash", compacted)
	}
	if len(fake.requests) != 1 || len(fake.requests[0].Messages) != 2 {
		t.Fatalf("model request = %#v, want summary plus recent", fake.requests)
	}
	if !contextwindow.IsSummaryMessage(fake.requests[0].Messages[0]) {
		t.Fatalf("first model message metadata = %#v, want context summary", fake.requests[0].Messages[0].Metadata)
	}
}

func TestQueryUsesPersistedCompactionCheckpointOnNextRun(t *testing.T) {
	store := session.NewMemoryStore()
	sess, err := store.Create(context.Background())
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if err := store.Append(context.Background(), sess.ID, model.Message{
		Role:    model.RoleUser,
		Content: []model.ContentBlock{{Type: model.ContentText, Text: strings.Repeat("old ", 20)}},
	}); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}
	summarizeCalls := 0
	policy := contextwindow.SummarizingBudget{
		MaxTokens:        40,
		MaxSummaryTokens: 12,
		SummaryPrefix:    "S:",
		Summarizer: contextwindow.SummarizerFunc(func(context.Context, []model.Message) (string, error) {
			summarizeCalls++
			return "summary", nil
		}),
	}
	firstModel := &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "done"}}}}
	events, err := Query(context.Background(), "recent", Options{
		Model:     firstModel,
		Sessions:  store,
		SessionID: sess.ID,
		Context:   policy,
	})
	if err != nil {
		t.Fatalf("first Query returned error: %v", err)
	}
	for event := range events {
		if event.Kind == EventError {
			t.Fatalf("first query error: %v", event.Err)
		}
	}
	if summarizeCalls != 1 {
		t.Fatalf("summarizeCalls after first query = %d, want 1", summarizeCalls)
	}

	secondModel := &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "done again"}}}}
	events, err = Query(context.Background(), "next", Options{
		Model:     secondModel,
		Sessions:  store,
		SessionID: sess.ID,
		Context:   policy,
	})
	if err != nil {
		t.Fatalf("second Query returned error: %v", err)
	}
	for event := range events {
		if event.Kind == EventError {
			t.Fatalf("second query error: %v", event.Err)
		}
	}
	if summarizeCalls != 1 {
		t.Fatalf("summarizeCalls after second query = %d, want persisted checkpoint reuse", summarizeCalls)
	}
	if len(secondModel.requests) != 1 || len(secondModel.requests[0].Messages) == 0 {
		t.Fatalf("second model request = %#v", secondModel.requests)
	}
	if !contextwindow.IsSummaryMessage(secondModel.requests[0].Messages[0]) {
		t.Fatalf("second request first message = %#v, want persisted summary", secondModel.requests[0].Messages[0])
	}
	raw, err := store.Messages(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("Messages returned error: %v", err)
	}
	if len(raw) != 5 {
		t.Fatalf("raw transcript len = %d, want original + two prompts + two assistant messages", len(raw))
	}
}

func TestQueryContinuesWhenCompactionCheckpointSaveFails(t *testing.T) {
	inner := session.NewMemoryStore()
	store := &failingCompactionStore{
		inner: inner,
		err:   errors.New("checkpoint store unavailable"),
	}
	meter := &recordingMeter{}
	sess, err := store.Create(context.Background())
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if err := store.Append(context.Background(), sess.ID, model.Message{
		Role:    model.RoleUser,
		Content: []model.ContentBlock{{Type: model.ContentText, Text: strings.Repeat("old ", 20)}},
	}); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}

	events, err := Query(context.Background(), "recent", Options{
		Model:     &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "done"}}}},
		Meter:     meter,
		Sessions:  store,
		SessionID: sess.ID,
		Context: contextwindow.SummarizingBudget{
			MaxTokens:        40,
			MaxSummaryTokens: 12,
			SummaryPrefix:    "S:",
			Summarizer: contextwindow.SummarizerFunc(func(context.Context, []model.Message) (string, error) {
				return "summary", nil
			}),
		},
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	var compacted bool
	var result string
	for event := range events {
		switch event.Kind {
		case EventContextCompacted:
			compacted = true
		case EventError:
			t.Fatalf("query error: %v", event.Err)
		case EventResult:
			result = event.Result
		}
	}
	if !compacted {
		t.Fatal("missing context compacted event")
	}
	if result != "done" {
		t.Fatalf("result = %q, want done despite checkpoint save failure", result)
	}
	raw, err := inner.Messages(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("Messages returned error: %v", err)
	}
	if len(raw) != 3 {
		t.Fatalf("raw transcript len = %d, want old + prompt + assistant", len(raw))
	}
	if !meter.hasCounter("memax.context.compaction_checkpoint.errors") {
		t.Fatalf("missing checkpoint error metric in %#v", meter.counterNames())
	}
}

func TestQueryContinuesWhenRetryCompactionCheckpointSaveFails(t *testing.T) {
	inner := session.NewMemoryStore()
	store := &failingCompactionStore{
		inner: inner,
		err:   errors.New("checkpoint store unavailable"),
	}
	meter := &recordingMeter{}
	retryModel := &contextRetryModel{
		fake: &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "done"}}}},
	}

	events, err := Query(context.Background(), "start", Options{
		Model:        retryModel,
		Meter:        meter,
		Sessions:     store,
		ContextRetry: compactingContextPolicy{text: "retry compacted"},
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	var compacted bool
	var result string
	for event := range events {
		switch event.Kind {
		case EventContextCompacted:
			compacted = true
		case EventError:
			t.Fatalf("query error: %v", event.Err)
		case EventResult:
			result = event.Result
		}
	}
	if !compacted {
		t.Fatal("missing retry context compacted event")
	}
	if result != "done" {
		t.Fatalf("result = %q, want done despite retry checkpoint save failure", result)
	}
	if !meter.hasCounter("memax.context.compaction_checkpoint.errors") {
		t.Fatalf("missing checkpoint error metric in %#v", meter.counterNames())
	}
}

func TestMemoryDistillerReceivesActiveCompactedView(t *testing.T) {
	store := session.NewMemoryStore()
	sess, err := store.Create(context.Background())
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if err := store.Append(context.Background(), sess.ID, model.Message{
		Role:    model.RoleUser,
		Content: []model.ContentBlock{{Type: model.ContentText, Text: strings.Repeat("old raw detail ", 20)}},
	}); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}
	policy := contextwindow.SummarizingBudget{
		MaxTokens:        40,
		MaxSummaryTokens: 12,
		SummaryPrefix:    "S:",
		Summarizer: contextwindow.SummarizerFunc(func(context.Context, []model.Message) (string, error) {
			return "summary", nil
		}),
	}
	events, err := Query(context.Background(), "recent", Options{
		Model:     &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "done"}}}},
		Sessions:  store,
		SessionID: sess.ID,
		Context:   policy,
	})
	if err != nil {
		t.Fatalf("first Query returned error: %v", err)
	}
	for event := range events {
		if event.Kind == EventError {
			t.Fatalf("first query error: %v", event.Err)
		}
	}

	var got memory.DistillRequest
	events, err = Query(context.Background(), "next", Options{
		Model:     &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "done again"}}}},
		Sessions:  store,
		SessionID: sess.ID,
		Context:   policy,
		MemoryDistiller: memory.DistillerFunc(func(_ context.Context, req memory.DistillRequest) ([]memory.Candidate, error) {
			got = req
			return nil, nil
		}),
	})
	if err != nil {
		t.Fatalf("second Query returned error: %v", err)
	}
	for event := range events {
		if event.Kind == EventError {
			t.Fatalf("second query error: %v", event.Err)
		}
	}
	if len(got.Messages) == 0 || !contextwindow.IsSummaryMessage(got.Messages[0]) {
		t.Fatalf("distiller messages = %#v, want active compacted view beginning with summary", got.Messages)
	}
	if got.Messages[0].PlainText() != "S:summary" {
		t.Fatalf("summary text = %q, want persisted summary", got.Messages[0].PlainText())
	}
	for _, msg := range got.Messages {
		if strings.Contains(msg.PlainText(), "old raw detail") {
			t.Fatalf("distiller messages = %#v, want compacted view without raw pre-checkpoint details", got.Messages)
		}
	}
}

func TestQueryRunsContextAppliedHook(t *testing.T) {
	var got hook.ContextAppliedInput
	hooks := hook.NewRunner(hook.WithContextApplied(func(_ context.Context, input hook.ContextAppliedInput) error {
		got = input
		return nil
	}))
	fake := &fakeModel{turns: [][]model.StreamEvent{
		{{Kind: model.StreamToolUse, ToolUse: model.ToolUse{ID: "tool-1", Name: "noop", Input: json.RawMessage(`{}`)}}},
		{{Kind: model.StreamText, Text: "done"}},
	}}
	registry := tool.NewRegistry(tool.Definition{
		ToolSpec: model.ToolSpec{Name: "noop"},
		Handler: func(context.Context, tool.Call) (model.ToolResult, error) {
			return model.ToolResult{Content: "ok"}, nil
		},
	})

	events, err := Query(context.Background(), "start", Options{
		Model:   fake,
		Tools:   registry,
		Hooks:   hooks,
		Context: contextwindow.RecentMessages{MaxMessages: 2},
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if _, err := Drain(events); err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if got.OriginalMessages != 3 || got.SentMessages != 2 {
		t.Fatalf("context hook input = %#v, want 3 -> 2", got)
	}
}

func TestQueryEmitsApprovalEventsAndMetrics(t *testing.T) {
	store := workspace.NewMemoryStore(map[string]string{"README.md": "old"})
	workspaceTools, err := workspacetools.NewTools(store)
	if err != nil {
		t.Fatalf("NewTools returned error: %v", err)
	}
	policy := agentpolicy.RequireApprovalBeforeToolsWithOptions(
		[]string{workspacetools.ApplyPatchToolName},
		agentpolicy.WithInputBoundApprovals(),
		agentpolicy.WithSingleUseApprovals(),
	)
	approvalTool := approvaltools.NewTool(approvaltools.Config{
		Approver: approvaltools.StaticApprover{Decision: approvaltools.Decision{
			Approved: true,
			Reason:   "approved test patch",
		}},
	})
	fake := &fakeModel{turns: [][]model.StreamEvent{
		{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "approval-1",
				Name:  approvaltools.ToolName,
				Input: json.RawMessage(`{"action":"workspace_apply_patch","reason":"test patch","summary":{"title":"Review README patch","description":"Change README.md from old to new","risk":"low","paths":["README.md"],"changes":1,"modified":1,"byte_delta":0},"tool_input":{"operations":[{"path":"README.md","old_content":"old","new_content":"new"}]}}`),
			},
		}},
		{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "patch-1",
				Name:  workspacetools.ApplyPatchToolName,
				Input: json.RawMessage(`{"operations":[{"path":"README.md","old_content":"old","new_content":"new"}]}`),
			},
		}},
		{{Kind: model.StreamText, Text: "done"}},
	}}
	meter := &recordingMeter{}
	stream, err := Query(context.Background(), "request approval then patch", Options{
		Model: fake,
		Tools: tool.NewRegistry(append(workspaceTools, approvalTool)...),
		Hooks: hook.NewRunner(policy.Options()...),
		Meter: meter,
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	var events []Event
	for event := range stream {
		if event.Kind == EventError {
			t.Fatalf("unexpected error event: %v", event.Err)
		}
		events = append(events, event)
	}
	requested := findApprovalEvent(events, EventApprovalRequested)
	if requested == nil || !requested.Requested || requested.Action != workspacetools.ApplyPatchToolName || requested.InputHash == "" {
		t.Fatalf("approval requested event = %#v", requested)
	}
	if requested.Summary.Title != "Review README patch" || requested.Summary.Description != "Change README.md from old to new" || requested.Summary.Risk != "low" || requested.Summary.Changes != 1 || requested.Summary.Modified != 1 || !sameStrings(requested.Summary.Paths, []string{"README.md"}) {
		t.Fatalf("approval requested summary = %#v, want structured patch summary", requested.Summary)
	}
	granted := findApprovalEvent(events, EventApprovalGranted)
	if granted == nil || !granted.Approved || granted.Reason != "approved test patch" || granted.InputHash != requested.InputHash {
		t.Fatalf("approval granted event = %#v, requested = %#v", granted, requested)
	}
	consumed := findApprovalEvent(events, EventApprovalConsumed)
	if consumed == nil || !consumed.Consumed || !consumed.SingleUse || !consumed.InputBound || consumed.InputHash != requested.InputHash {
		t.Fatalf("approval consumed event = %#v, requested = %#v", consumed, requested)
	}
	for _, counter := range []string{
		"memax.approval.requests",
		"memax.approval.grants",
		"memax.approval.consumed",
	} {
		if !meter.hasCounter(counter) {
			t.Fatalf("meter counters = %#v, missing %s", meter.counterNames(), counter)
		}
	}
}

func TestQueryPersistsStoredResultMetadataInSession(t *testing.T) {
	store := session.NewMemoryStore()
	results := resultstore.NewMemoryStore()
	fake := &fakeModel{turns: [][]model.StreamEvent{
		{{Kind: model.StreamToolUse, ToolUse: model.ToolUse{ID: "tool-1", Name: "read", Input: json.RawMessage(`{}`)}}},
		{{Kind: model.StreamText, Text: "done"}},
	}}
	registry := tool.NewRegistry(tool.Definition{
		ToolSpec: model.ToolSpec{Name: "read", MaxResultBytes: 4},
		Handler: func(context.Context, tool.Call) (model.ToolResult, error) {
			return model.ToolResult{Content: "abcdef"}, nil
		},
	})

	events, err := Query(context.Background(), "start", Options{
		Model:       fake,
		Tools:       registry,
		Sessions:    store,
		ResultStore: results,
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	var sessionID string
	for event := range events {
		if event.SessionID != "" {
			sessionID = event.SessionID
		}
		if event.Kind == EventError {
			t.Fatalf("query error: %v", event.Err)
		}
		if event.Kind == EventResult {
			break
		}
	}
	messages, err := store.Messages(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("Messages returned error: %v", err)
	}
	var toolResult *model.ToolResult
	for _, msg := range messages {
		if msg.ToolResult != nil {
			toolResult = msg.ToolResult
			break
		}
	}
	if toolResult == nil {
		t.Fatal("session transcript missing tool result")
	}
	id, ok := toolResult.Metadata["stored_result_id"].(string)
	if !ok || id == "" {
		t.Fatalf("tool result metadata = %#v, want stored result id", toolResult.Metadata)
	}
	entry, err := results.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if entry.Content != "abcdef" || toolResult.Content != "abcd" {
		t.Fatalf("stored content = %q, transcript content = %q", entry.Content, toolResult.Content)
	}
}

func TestQueryUserPromptHookCanDeny(t *testing.T) {
	_, err := Query(context.Background(), "start", Options{
		Model: &fakeModel{},
		Hooks: hook.NewRunner(hook.WithUserPrompt(func(context.Context, hook.UserPromptInput) (hook.UserPromptResult, error) {
			return hook.UserPromptResult{DenyReason: "blocked prompt"}, nil
		})),
	})
	if err == nil || err.Error() != "blocked prompt" {
		t.Fatalf("Query error = %v, want blocked prompt", err)
	}
}

func TestQueryStopHookErrorSurfacesBeforeResult(t *testing.T) {
	errStop := errors.New("stop sink unavailable")
	events, err := Query(context.Background(), "start", Options{
		Model: &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "done"}}}},
		Hooks: hook.NewRunner(hook.WithStop(func(context.Context, hook.StopInput) error {
			return errStop
		})),
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if _, err := Drain(events); err == nil || !strings.Contains(err.Error(), "stop hook failed") {
		t.Fatalf("Drain error = %v, want stop hook failure", err)
	}
}

type replaceContextPolicy struct {
	text string
}

func (p replaceContextPolicy) Apply(_ context.Context, messages []model.Message) ([]model.Message, error) {
	if len(messages) == 0 {
		return nil, nil
	}
	return []model.Message{
		{
			Role: model.RoleUser,
			Content: []model.ContentBlock{
				{Type: model.ContentText, Text: p.text},
			},
		},
	}, nil
}

type compactingContextPolicy struct {
	text string
}

func (p compactingContextPolicy) Apply(ctx context.Context, messages []model.Message) ([]model.Message, error) {
	result, err := p.ApplyWithResult(ctx, messages)
	if err != nil {
		return nil, err
	}
	return result.Messages, nil
}

func (p compactingContextPolicy) ApplyWithResult(_ context.Context, _ []model.Message) (contextwindow.PolicyResult, error) {
	return contextwindow.PolicyResult{
		Messages: []model.Message{{
			Role:    model.RoleUser,
			Content: []model.ContentBlock{{Type: model.ContentText, Text: p.text}},
		}},
		Compaction: &contextwindow.CompactionRecord{
			Policy:             "test",
			Reason:             contextwindow.CompactionReasonBudget,
			OriginalMessages:   1,
			SentMessages:       1,
			SummarizedMessages: 1,
			SummaryHash:        "test-hash",
			SummaryPreview:     p.text,
		},
	}, nil
}

type blockingCreateStore struct {
	inner   *session.MemoryStore
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type countingMessageStore struct {
	inner *session.MemoryStore
	mu    sync.Mutex
	calls int
}

type failingCompactionStore struct {
	inner *session.MemoryStore
	err   error
}

func (s *countingMessageStore) Create(ctx context.Context) (session.Session, error) {
	return s.inner.Create(ctx)
}

func (s *countingMessageStore) Append(ctx context.Context, id string, msg model.Message) error {
	return s.inner.Append(ctx, id, msg)
}

func (s *countingMessageStore) Messages(ctx context.Context, id string) ([]model.Message, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	return s.inner.Messages(ctx, id)
}

func (s *countingMessageStore) messageCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *failingCompactionStore) Create(ctx context.Context) (session.Session, error) {
	return s.inner.Create(ctx)
}

func (s *failingCompactionStore) Append(ctx context.Context, id string, msg model.Message) error {
	return s.inner.Append(ctx, id, msg)
}

func (s *failingCompactionStore) Messages(ctx context.Context, id string) ([]model.Message, error) {
	return s.inner.Messages(ctx, id)
}

func (s *failingCompactionStore) MessageView(ctx context.Context, id string) (session.MessageView, error) {
	return s.inner.MessageView(ctx, id)
}

func (s *failingCompactionStore) SaveCompaction(context.Context, string, session.CompactionCheckpoint) error {
	return s.err
}

func (s *blockingCreateStore) Create(ctx context.Context) (session.Session, error) {
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.release:
	case <-ctx.Done():
		return session.Session{}, ctx.Err()
	}
	return s.inner.Create(ctx)
}

func (s *blockingCreateStore) Append(ctx context.Context, id string, msg model.Message) error {
	return s.inner.Append(ctx, id, msg)
}

func (s *blockingCreateStore) Messages(ctx context.Context, id string) ([]model.Message, error) {
	return s.inner.Messages(ctx, id)
}

type fakeModel struct {
	turns    [][]model.StreamEvent
	requests []model.Request
	calls    int
}

func (f *fakeModel) Stream(_ context.Context, req model.Request) (model.Stream, error) {
	f.requests = append(f.requests, req)
	if f.calls >= len(f.turns) {
		return &fakeStream{}, nil
	}
	stream := &fakeStream{events: f.turns[f.calls]}
	f.calls++
	return stream, nil
}

type contextRetryModel struct {
	fake     *fakeModel
	requests []model.Request
	calls    int
}

func (m *contextRetryModel) Stream(ctx context.Context, req model.Request) (model.Stream, error) {
	m.requests = append(m.requests, req)
	if m.calls == 0 {
		m.calls++
		return nil, model.ErrContextWindowExceeded
	}
	m.calls++
	return m.fake.Stream(ctx, req)
}

type fakeStream struct {
	events []model.StreamEvent
	index  int
}

func (s *fakeStream) Recv() (model.StreamEvent, error) {
	if s.index >= len(s.events) {
		return model.StreamEvent{}, model.ErrEndOfStream
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

func (s *fakeStream) Close() error {
	return nil
}

type earlyToolModel struct {
	toolStarted <-chan struct{}
	first       []model.StreamEvent
	second      []model.StreamEvent
	calls       int
}

func (m *earlyToolModel) Stream(context.Context, model.Request) (model.Stream, error) {
	m.calls++
	if m.calls == 1 {
		return &earlyToolStream{events: m.first, toolStarted: m.toolStarted}, nil
	}
	return &fakeStream{events: m.second}, nil
}

type earlyToolStream struct {
	events      []model.StreamEvent
	index       int
	toolStarted <-chan struct{}
}

func (s *earlyToolStream) Recv() (model.StreamEvent, error) {
	if s.index >= len(s.events) {
		return model.StreamEvent{}, model.ErrEndOfStream
	}
	if s.index == len(s.events)-1 {
		select {
		case <-s.toolStarted:
		case <-time.After(5 * time.Second):
			return model.StreamEvent{}, errors.New("safe tool did not start before trailing assistant text")
		}
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

func (s *earlyToolStream) Close() error {
	return nil
}

type streamErrorModel struct {
	started <-chan struct{}
	events  []model.StreamEvent
	err     error
}

func (m *streamErrorModel) Stream(context.Context, model.Request) (model.Stream, error) {
	return &streamErrorStream{events: m.events, started: m.started, err: m.err}, nil
}

type streamErrorStream struct {
	events  []model.StreamEvent
	index   int
	started <-chan struct{}
	err     error
}

func (s *streamErrorStream) Recv() (model.StreamEvent, error) {
	if s.index >= len(s.events) {
		if s.started != nil {
			select {
			case <-s.started:
			case <-time.After(5 * time.Second):
				return model.StreamEvent{}, errors.New("early tool did not start before stream error")
			}
		}
		return model.StreamEvent{}, s.err
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

func (s *streamErrorStream) Close() error {
	return nil
}

func requestHasTool(req model.Request, name string) bool {
	for _, spec := range req.Tools {
		if spec.Name == name {
			return true
		}
	}
	return false
}

type recordingTracer struct {
	mu    sync.Mutex
	spans []*recordingSpan
}

func (t *recordingTracer) Start(ctx context.Context, name string, attrs ...telemetry.Attribute) (context.Context, telemetry.Span) {
	span := &recordingSpan{name: name, attrs: append([]telemetry.Attribute(nil), attrs...)}
	t.mu.Lock()
	t.spans = append(t.spans, span)
	t.mu.Unlock()
	return ctx, span
}

func (t *recordingTracer) hasSpan(name string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, span := range t.spans {
		if span.name == name {
			return true
		}
	}
	return false
}

func (t *recordingTracer) names() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	names := make([]string, 0, len(t.spans))
	for _, span := range t.spans {
		names = append(names, span.name)
	}
	return names
}

type recordingSpan struct {
	name   string
	attrs  []telemetry.Attribute
	ended  bool
	errors []error
}

func (s *recordingSpan) Set(attrs ...telemetry.Attribute) {
	s.attrs = append(s.attrs, attrs...)
}

func (s *recordingSpan) RecordError(err error) {
	if err != nil {
		s.errors = append(s.errors, err)
	}
}

func (s *recordingSpan) End() {
	s.ended = true
}

type recordingMeter struct {
	mu       sync.Mutex
	counters []string
	records  []string
}

func (m *recordingMeter) Add(_ context.Context, name string, _ int64, _ ...telemetry.Attribute) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counters = append(m.counters, name)
}

func (m *recordingMeter) Record(_ context.Context, name string, _ float64, _ ...telemetry.Attribute) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = append(m.records, name)
}

func (m *recordingMeter) hasCounter(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.counters {
		if existing == name {
			return true
		}
	}
	return false
}

func (m *recordingMeter) hasRecord(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.records {
		if existing == name {
			return true
		}
	}
	return false
}

func (m *recordingMeter) counterNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.counters...)
}

func (m *recordingMeter) recordNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.records...)
}

type metricAddObservation struct {
	name  string
	attrs []telemetry.Attribute
}

type attributeRecordingMeter struct {
	mu   sync.Mutex
	adds []metricAddObservation
}

func (m *attributeRecordingMeter) Add(_ context.Context, name string, _ int64, attrs ...telemetry.Attribute) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.adds = append(m.adds, metricAddObservation{
		name:  name,
		attrs: append([]telemetry.Attribute(nil), attrs...),
	})
}

func (m *attributeRecordingMeter) Record(context.Context, string, float64, ...telemetry.Attribute) {}

func (m *attributeRecordingMeter) hasAddWithAttribute(metricName, key string, want any) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, add := range m.adds {
		if add.name != metricName {
			continue
		}
		for _, attr := range add.attrs {
			if attr.Key == key && attr.Value == want {
				return true
			}
		}
	}
	return false
}

func (m *attributeRecordingMeter) snapshotAdds() []metricAddObservation {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]metricAddObservation, len(m.adds))
	for i, add := range m.adds {
		out[i] = metricAddObservation{
			name:  add.name,
			attrs: append([]telemetry.Attribute(nil), add.attrs...),
		}
	}
	return out
}

func sameStrings(a []string, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func findSkillEvent(events []Event, kind EventKind) *SkillEvent {
	for _, event := range events {
		if event.Kind == kind && event.Skill != nil {
			return event.Skill
		}
	}
	return nil
}

func findWorkspaceEvent(events []Event, kind EventKind) *WorkspaceEvent {
	for _, event := range events {
		if event.Kind == kind && event.Workspace != nil {
			return event.Workspace
		}
	}
	return nil
}

func findVerificationEvent(events []Event) *VerificationEvent {
	for _, event := range events {
		if event.Kind == EventVerification && event.Verification != nil {
			return event.Verification
		}
	}
	return nil
}

func findApprovalEvent(events []Event, kind EventKind) *ApprovalEvent {
	for _, event := range events {
		if event.Kind == kind && event.Approval != nil {
			return event.Approval
		}
	}
	return nil
}

func findCommandEvent(events []Event, kind EventKind) *CommandEvent {
	for _, event := range events {
		if event.Kind == kind && event.Command != nil {
			return event.Command
		}
	}
	return nil
}

func answerOutputContract() output.Contract {
	return output.Contract{Schema: map[string]any{
		"type":     "object",
		"required": []any{"answer"},
		"properties": map[string]any{
			"answer": map[string]any{"type": "string"},
		},
		"additionalProperties": false,
	}}
}

func requestToolNames(req model.Request) []string {
	out := make([]string, 0, len(req.Tools))
	for _, spec := range req.Tools {
		out = append(out, spec.Name)
	}
	return out
}
