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

func TestIsContextWindowExceeded(t *testing.T) {
	err := fmt.Errorf("stream model: %w", ErrContextWindowExceeded)
	if !IsContextWindowExceeded(err) {
		t.Fatalf("IsContextWindowExceeded(%v) = false, want true", err)
	}
}
