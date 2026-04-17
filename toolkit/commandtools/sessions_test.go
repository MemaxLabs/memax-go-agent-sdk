package commandtools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/hook"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

func TestScriptedSessionManagerStartReadStop(t *testing.T) {
	manager := NewScriptedSessionManager(ScriptedCommand{
		ID:  "server-1",
		PID: 4242,
		Pages: []ScriptedOutputPage{
			{
				Chunks: []OutputChunk{
					{Seq: 1, Stream: "stdout", Text: "ready\n", Time: time.Unix(1, 0).UTC()},
				},
				Running: true,
			},
			{
				Chunks: []OutputChunk{
					{Seq: 2, Stream: "stderr", Text: "stopping\n", Time: time.Unix(2, 0).UTC()},
				},
				Running:  false,
				ExitCode: intPtr(0),
			},
		},
		StopExitCode: intPtr(143),
	})
	started, err := manager.StartCommand(context.Background(), StartRequest{
		SessionID: "session-1",
		Argv:      []string{"npm", "run", "dev"},
		Purpose:   "start dev server",
	})
	if err != nil {
		t.Fatalf("StartCommand returned error: %v", err)
	}
	if started.ID != "server-1" || started.Status != SessionRunning {
		t.Fatalf("started = %#v, want running scripted session", started)
	}
	first, err := manager.ReadCommandOutput(context.Background(), ReadRequest{SessionID: "session-1", ID: "server-1"})
	if err != nil {
		t.Fatalf("ReadCommandOutput first returned error: %v", err)
	}
	if len(first.Chunks) != 1 || first.Chunks[0].Text != "ready\n" || first.Session.Status != SessionRunning {
		t.Fatalf("first = %#v, want running ready chunk", first)
	}
	second, err := manager.ReadCommandOutput(context.Background(), ReadRequest{SessionID: "session-1", ID: "server-1"})
	if err != nil {
		t.Fatalf("ReadCommandOutput second returned error: %v", err)
	}
	if len(second.Chunks) != 1 || second.Chunks[0].Text != "stopping\n" || second.Session.Status != SessionExited {
		t.Fatalf("second = %#v, want exited stopping chunk", second)
	}
	stopped, err := manager.StopCommand(context.Background(), StopRequest{SessionID: "session-1", ID: "server-1"})
	if err != nil {
		t.Fatalf("StopCommand returned error: %v", err)
	}
	if stopped.Status != SessionExited || stopped.ExitCode == nil || *stopped.ExitCode != 0 {
		t.Fatalf("stopped = %#v, want exited session unchanged after natural exit", stopped)
	}
}

func TestStartReadStopToolsReturnMetadata(t *testing.T) {
	manager := NewScriptedSessionManager(ScriptedCommand{
		ID:  "dev-1",
		PID: 1234,
		Pages: []ScriptedOutputPage{{
			Chunks:  []OutputChunk{{Seq: 1, Stream: "stdout", Text: "listening on :3000\n"}},
			Running: true,
		}},
		StopExitCode: intPtr(143),
	})
	startTool := NewStartTool(manager)
	readTool := NewReadOutputTool(manager)
	stopTool := NewStopTool(manager)
	runtime := tool.Runtime{SessionID: "session-1"}

	started, err := startTool.(tool.Definition).Handler(context.Background(), tool.Call{
		Runtime: runtime,
		Use: model.ToolUse{
			Name:  StartToolName,
			Input: json.RawMessage(`{"command":["npm","run","dev"],"purpose":"start dev server"}`),
		},
	})
	if err != nil {
		t.Fatalf("start handler returned error: %v", err)
	}
	if started.Metadata[MetadataCommandOperation] != "start" || started.Metadata[MetadataCommandSessionID] != "dev-1" {
		t.Fatalf("started metadata = %#v, want command start metadata", started.Metadata)
	}
	read, err := readTool.(tool.Definition).Handler(context.Background(), tool.Call{
		Runtime: runtime,
		Use: model.ToolUse{
			Name:  ReadOutputToolName,
			Input: json.RawMessage(`{"id":"dev-1"}`),
		},
	})
	if err != nil {
		t.Fatalf("read handler returned error: %v", err)
	}
	if read.Metadata[MetadataCommandOperation] != "read" || read.Metadata[MetadataCommandOutputChunks] != 1 {
		t.Fatalf("read metadata = %#v, want command read metadata", read.Metadata)
	}
	if !strings.Contains(read.Content, "listening on :3000") {
		t.Fatalf("read content = %q, want output chunk", read.Content)
	}
	stopped, err := stopTool.(tool.Definition).Handler(context.Background(), tool.Call{
		Runtime: runtime,
		Use: model.ToolUse{
			Name:  StopToolName,
			Input: json.RawMessage(`{"id":"dev-1","force":true}`),
		},
	})
	if err != nil {
		t.Fatalf("stop handler returned error: %v", err)
	}
	if stopped.Metadata[MetadataCommandOperation] != "stop" {
		t.Fatalf("stop metadata = %#v, want stop operation", stopped.Metadata)
	}
}

func TestApprovalSummaryFromStartInput(t *testing.T) {
	summary, err := ApprovalSummaryFromStartInput([]byte(`{"command":["npm","run","dev"],"purpose":"start local dev server"}`))
	if err != nil {
		t.Fatalf("ApprovalSummaryFromStartInput returned error: %v", err)
	}
	if summary.Title != "Start command session: npm run dev" ||
		summary.Description != "start local dev server" ||
		summary.Changes != 1 {
		t.Fatalf("summary = %#v, want start command approval summary", summary)
	}
}

func TestSessionCleanupOptions(t *testing.T) {
	manager := NewScriptedSessionManager(ScriptedCommand{ID: "proc-1"})
	if _, err := manager.StartCommand(context.Background(), StartRequest{
		SessionID: "session-1",
		Argv:      []string{"sleep", "60"},
	}); err != nil {
		t.Fatalf("StartCommand returned error: %v", err)
	}
	runner := hook.NewRunner(SessionCleanupOptions(manager)...)
	if errs := runner.SessionEnded(context.Background(), hook.SessionEndedInput{SessionID: "session-1", Reason: hook.StopReasonResult}); len(errs) > 0 {
		t.Fatalf("SessionEnded returned errors: %v", errs)
	}
	sessions, err := manager.ListCommands(context.Background(), ListRequest{SessionID: "session-1", IncludeCompleted: true})
	if err != nil {
		t.Fatalf("ListCommands returned error: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("sessions = %#v, want cleanup to remove session-owned commands", sessions)
	}
}

func intPtr(v int) *int { return &v }
