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

func TestScriptedSessionManagerWriteInput(t *testing.T) {
	manager := NewScriptedSessionManager(ScriptedCommand{
		ID:  "server-1",
		PID: 4242,
		WritePages: []ScriptedWritePage{
			{
				Page: ScriptedOutputPage{
					Chunks: []OutputChunk{
						{Seq: 1, Stream: "stdout", Text: "echo:hello\n", Time: time.Unix(1, 0).UTC()},
					},
					Running: true,
				},
			},
			{
				Page: ScriptedOutputPage{
					Chunks: []OutputChunk{
						{Seq: 2, Stream: "stdout", Text: "bye\n", Time: time.Unix(2, 0).UTC()},
					},
					Running:  false,
					ExitCode: intPtr(0),
				},
			},
		},
	})
	if _, err := manager.StartCommand(context.Background(), StartRequest{
		SessionID: "session-1",
		Argv:      []string{"python", "-i"},
	}); err != nil {
		t.Fatalf("StartCommand returned error: %v", err)
	}
	first, err := manager.WriteCommandInput(context.Background(), WriteRequest{
		SessionID: "session-1",
		ID:        "server-1",
		Input:     "hello\n",
	})
	if err != nil {
		t.Fatalf("WriteCommandInput first returned error: %v", err)
	}
	if len(first.Chunks) != 1 || first.Chunks[0].Text != "echo:hello\n" || first.Session.Status != SessionRunning {
		t.Fatalf("first = %#v, want running echoed write result", first)
	}
	second, err := manager.WriteCommandInput(context.Background(), WriteRequest{
		SessionID: "session-1",
		ID:        "server-1",
		Input:     "exit\n",
	})
	if err != nil {
		t.Fatalf("WriteCommandInput second returned error: %v", err)
	}
	if len(second.Chunks) != 1 || second.Chunks[0].Text != "bye\n" || second.Session.Status != SessionExited {
		t.Fatalf("second = %#v, want exited write result", second)
	}
	requests := manager.WriteRequests()
	if len(requests) != 2 || requests[0].Input != "hello\n" || requests[1].Input != "exit\n" {
		t.Fatalf("write requests = %#v, want captured inputs", requests)
	}
}

func TestStartReadStopToolsReturnMetadata(t *testing.T) {
	manager := NewScriptedSessionManager(ScriptedCommand{
		ID:  "dev-1",
		PID: 1234,
		TTY: true,
		Pages: []ScriptedOutputPage{{
			Chunks:  []OutputChunk{{Seq: 1, Stream: "pty", Text: "listening on :3000\n"}},
			Running: true,
		}},
		WritePages: []ScriptedWritePage{{
			Page: ScriptedOutputPage{
				Chunks:  []OutputChunk{{Seq: 2, Stream: "pty", Text: "pong\n"}},
				Running: true,
			},
		}},
		StopExitCode: intPtr(143),
	})
	startTool := NewStartTool(manager)
	readTool := NewReadOutputTool(manager)
	writeTool := NewWriteInputTool(manager)
	stopTool := NewStopTool(manager)
	runtime := tool.Runtime{SessionID: "session-1"}

	started, err := startTool.(tool.Definition).Handler(context.Background(), tool.Call{
		Runtime: runtime,
		Use: model.ToolUse{
			Name:  StartToolName,
			Input: json.RawMessage(`{"command":["npm","run","dev"],"purpose":"start dev server","tty":true}`),
		},
	})
	if err != nil {
		t.Fatalf("start handler returned error: %v", err)
	}
	if started.Metadata[MetadataCommandOperation] != "start" || started.Metadata[MetadataCommandSessionID] != "dev-1" || started.Metadata[MetadataCommandTTY] != true {
		t.Fatalf("started metadata = %#v, want command start metadata", started.Metadata)
	}
	if !strings.Contains(started.Content, "tty: true") {
		t.Fatalf("start content = %q, want tty indicator", started.Content)
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
	if !strings.Contains(read.Content, "[pty #1]") || !strings.Contains(read.Content, "listening on :3000") {
		t.Fatalf("read content = %q, want output chunk", read.Content)
	}
	wrote, err := writeTool.(tool.Definition).Handler(context.Background(), tool.Call{
		Runtime: runtime,
		Use: model.ToolUse{
			Name:  WriteInputToolName,
			Input: json.RawMessage(`{"id":"dev-1","input":"ping","append_newline":true}`),
		},
	})
	if err != nil {
		t.Fatalf("write handler returned error: %v", err)
	}
	if wrote.Metadata[MetadataCommandOperation] != "write" || wrote.Metadata[MetadataCommandInputBytes] != 5 || wrote.Metadata[MetadataCommandOutputChunks] != 1 || wrote.Metadata[MetadataCommandTTY] != true {
		t.Fatalf("write metadata = %#v, want command write metadata", wrote.Metadata)
	}
	if !strings.Contains(wrote.Content, "[pty #2]") || !strings.Contains(wrote.Content, "pong") {
		t.Fatalf("write content = %q, want write output chunk", wrote.Content)
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
	if stopped.Metadata[MetadataCommandOperation] != "stop" || stopped.Metadata[MetadataCommandTTY] != true {
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

func TestApprovalSummaryFromTTYStartInput(t *testing.T) {
	summary, err := ApprovalSummaryFromStartInput([]byte(`{"command":["python","-i"],"tty":true}`))
	if err != nil {
		t.Fatalf("ApprovalSummaryFromStartInput returned error: %v", err)
	}
	if !strings.Contains(summary.Risk, "PTY sessions") {
		t.Fatalf("summary = %#v, want tty-specific risk text", summary)
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

func TestNewSessionToolsIncludesWriteWhenSupported(t *testing.T) {
	manager := NewScriptedSessionManager(ScriptedCommand{ID: "server-1"})
	tools, err := NewSessionTools(manager)
	if err != nil {
		t.Fatalf("NewSessionTools returned error: %v", err)
	}
	var names []string
	for _, entry := range tools {
		names = append(names, entry.Spec().Name)
	}
	want := []string{
		StartToolName,
		ReadOutputToolName,
		StopToolName,
		ListToolName,
		WriteInputToolName,
	}
	if !sameStrings(names, want) {
		t.Fatalf("tool names = %#v, want %#v", names, want)
	}
}

func intPtr(v int) *int { return &v }
