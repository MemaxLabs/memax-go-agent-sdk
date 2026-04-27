package model

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestMessagePlainTextConcatenatesTextBlocksOnly(t *testing.T) {
	msg := Message{
		Role: RoleAssistant,
		Content: []ContentBlock{
			{Type: ContentText, Text: "hello "},
			{Type: ContentToolUse, ToolUse: &ToolUse{ID: "1", Name: "read"}},
			{Type: ContentText, Text: "world"},
		},
	}

	if got, want := msg.PlainText(), "hello world"; got != want {
		t.Fatalf("PlainText() = %q, want %q", got, want)
	}
}

func TestToolSpecMaxResultBytesIsNotModelFacing(t *testing.T) {
	spec := ToolSpec{Name: "read", MaxResultBytes: 10}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	if strings.Contains(string(data), "MaxResultBytes") || strings.Contains(string(data), "max_result") {
		t.Fatalf("MaxResultBytes leaked into JSON: %s", data)
	}
}

func TestMessageMetadataIsJSONSerializedForSessionStores(t *testing.T) {
	msg := Message{
		Role:     RoleUser,
		Content:  []ContentBlock{{Type: ContentText, Text: "hello"}},
		Metadata: map[string]any{"context_summary": true},
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	if !strings.Contains(string(data), "metadata") || !strings.Contains(string(data), "context_summary") {
		t.Fatalf("metadata missing from JSON: %s", data)
	}
	var got Message
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}
	if got.Metadata["context_summary"] != true {
		t.Fatalf("metadata = %#v, want context_summary true", got.Metadata)
	}
}

func TestNormalizeToolUseDefaultsEmptyInputToObject(t *testing.T) {
	for _, input := range []json.RawMessage{nil, json.RawMessage(""), json.RawMessage(" \n\t ")} {
		got := NormalizeToolUse(ToolUse{ID: "tool-1", Name: "read", Input: input})
		if string(got.Input) != `{}` {
			t.Fatalf("NormalizeToolUse(%q).Input = %q, want {}", string(input), string(got.Input))
		}
		if _, err := json.Marshal(got); err != nil {
			t.Fatalf("Marshal normalized tool use returned error: %v", err)
		}
	}
}

func TestNormalizeToolUseCopiesNonEmptyInput(t *testing.T) {
	input := json.RawMessage(`{"path":"README.md"}`)
	got := NormalizeToolUse(ToolUse{ID: "tool-1", Name: "read", Input: input})
	if string(got.Input) != `{"path":"README.md"}` {
		t.Fatalf("Input = %s, want original object", got.Input)
	}
	got.Input[0] = '['
	if string(input) != `{"path":"README.md"}` {
		t.Fatalf("NormalizeToolUse did not copy input: %s", input)
	}
}

func TestCloneMessagesReturnsDeepCopy(t *testing.T) {
	messages := []Message{{
		Role: RoleAssistant,
		Content: []ContentBlock{{
			Type:    ContentToolUse,
			ToolUse: &ToolUse{ID: "tool-1", Name: "read", Input: []byte(`{"path":"README.md"}`)},
		}, {
			Type: ContentProviderArtifact,
			ProviderArtifact: &ProviderArtifact{
				Provider: "openai",
				Type:     "reasoning",
				Data:     json.RawMessage(`{"type":"reasoning","encrypted_content":"secret"}`),
			},
		}},
		Metadata: map[string]any{"context_summary": true},
		ToolResult: &ToolResult{
			ToolUseID: "tool-1",
			Name:      "read",
			Content:   "result",
			Metadata:  map[string]any{"stored_result_id": "result-1"},
		},
	}}

	got := CloneMessages(messages)
	got[0].Content[0].ToolUse.Name = "mutated"
	got[0].Content[1].ProviderArtifact.Data[0] = '['
	got[0].Metadata["context_summary"] = false
	got[0].ToolResult.Metadata["stored_result_id"] = "mutated"

	if messages[0].Content[0].ToolUse.Name != "read" {
		t.Fatalf("tool use mutated: %#v", messages[0].Content[0].ToolUse)
	}
	if string(messages[0].Content[1].ProviderArtifact.Data) != `{"type":"reasoning","encrypted_content":"secret"}` {
		t.Fatalf("provider artifact mutated: %s", messages[0].Content[1].ProviderArtifact.Data)
	}
	if messages[0].Metadata["context_summary"] != true {
		t.Fatalf("metadata mutated: %#v", messages[0].Metadata)
	}
	if messages[0].ToolResult.Metadata["stored_result_id"] != "result-1" {
		t.Fatalf("tool result metadata mutated: %#v", messages[0].ToolResult.Metadata)
	}
}

func TestIsContextWindowExceeded(t *testing.T) {
	err := fmt.Errorf("stream model: %w", ErrContextWindowExceeded)
	if !IsContextWindowExceeded(err) {
		t.Fatalf("IsContextWindowExceeded(%v) = false, want true", err)
	}
}
