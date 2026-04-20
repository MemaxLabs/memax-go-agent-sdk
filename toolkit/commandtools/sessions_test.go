package commandtools

import (
	"context"
	"encoding/json"
	"errors"
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

func TestScriptedSessionManagerClassifiesLookupErrors(t *testing.T) {
	manager := NewScriptedSessionManager(ScriptedCommand{
		ID:  "server-1",
		PID: 4242,
		Pages: []ScriptedOutputPage{{
			Chunks:  []OutputChunk{{Seq: 1, Stream: "stdout", Text: "ready\n", Time: time.Unix(1, 0).UTC()}},
			Running: true,
		}},
	})
	started, err := manager.StartCommand(context.Background(), StartRequest{
		SessionID: "session-1",
		Argv:      []string{"dev-server"},
	})
	if err != nil {
		t.Fatalf("StartCommand returned error: %v", err)
	}
	if _, err := manager.ReadCommandOutput(context.Background(), ReadRequest{
		SessionID: "session-2",
		ID:        started.ID,
	}); err != nil {
		assertCommandSessionError(t, err, ErrCommandSessionNotVisible, "commandtools: command session server-1 is not visible in this agent session")
	} else {
		t.Fatal("ReadCommandOutput cross-session returned nil error, want ErrCommandSessionNotVisible")
	}
	if _, err := manager.ReadCommandOutput(context.Background(), ReadRequest{
		SessionID: "session-1",
		ID:        "missing",
	}); err != nil {
		assertCommandSessionError(t, err, ErrCommandSessionUnknown, "commandtools: unknown command session missing")
	} else {
		t.Fatal("ReadCommandOutput missing-session returned nil error, want ErrCommandSessionUnknown")
	}
}

func TestScriptedSessionManagerClassifiesStateErrors(t *testing.T) {
	manager := NewScriptedSessionManager(
		ScriptedCommand{ID: "server-1", PID: 4242},
		ScriptedCommand{ID: "server-2", PID: 4243},
	)
	if _, err := manager.StartCommand(context.Background(), StartRequest{
		SessionID: "session-1",
		ID:        "server-1",
		Argv:      []string{"first"},
	}); err != nil {
		t.Fatalf("StartCommand first returned error: %v", err)
	}
	if _, err := manager.StartCommand(context.Background(), StartRequest{
		SessionID: "session-1",
		ID:        "server-1",
		Argv:      []string{"duplicate"},
	}); err != nil {
		assertCommandSessionError(t, err, ErrCommandSessionAlreadyExists, "commandtools: command session server-1 already exists")
	} else {
		t.Fatal("StartCommand duplicate returned nil error, want ErrCommandSessionAlreadyExists")
	}
	if _, err := manager.ResizeCommandTerminal(context.Background(), ResizeRequest{
		SessionID: "session-1",
		ID:        "server-1",
		Cols:      100,
		Rows:      30,
	}); err != nil {
		assertCommandSessionError(t, err, ErrCommandSessionNotPTY, "commandtools: command session server-1 is not PTY-backed")
	} else {
		t.Fatal("ResizeCommandTerminal non-PTY returned nil error, want ErrCommandSessionNotPTY")
	}

	ttyManager := NewScriptedSessionManager(ScriptedCommand{
		ID:  "tty-1",
		PID: 4244,
		TTY: true,
		Pages: []ScriptedOutputPage{{
			Running:  false,
			ExitCode: intPtr(0),
		}},
	})
	if _, err := ttyManager.StartCommand(context.Background(), StartRequest{
		SessionID: "session-1",
		Argv:      []string{"tty"},
	}); err != nil {
		t.Fatalf("StartCommand tty returned error: %v", err)
	}
	if _, err := ttyManager.ReadCommandOutput(context.Background(), ReadRequest{
		SessionID: "session-1",
		ID:        "tty-1",
	}); err != nil {
		t.Fatalf("ReadCommandOutput tty terminal page returned error: %v", err)
	}
	if _, err := ttyManager.ResizeCommandTerminal(context.Background(), ResizeRequest{
		SessionID: "session-1",
		ID:        "tty-1",
		Cols:      100,
		Rows:      30,
	}); err != nil {
		assertCommandSessionError(t, err, ErrCommandSessionNotRunning, "commandtools: command session tty-1 is not running")
	} else {
		t.Fatal("ResizeCommandTerminal exited session returned nil error, want ErrCommandSessionNotRunning")
	}
}

func TestScriptedSessionManagerReadAfterSeq(t *testing.T) {
	manager := NewScriptedSessionManager(ScriptedCommand{
		ID: "server-1",
		Pages: []ScriptedOutputPage{
			{
				Chunks: []OutputChunk{
					{Seq: 1, Stream: "stdout", Text: "one\n", Time: time.Unix(1, 0).UTC()},
					{Seq: 2, Stream: "stdout", Text: "two\n", Time: time.Unix(2, 0).UTC()},
				},
				Running: true,
			},
		},
	})
	if _, err := manager.StartCommand(context.Background(), StartRequest{
		SessionID: "session-1",
		Argv:      []string{"npm", "run", "watch"},
	}); err != nil {
		t.Fatalf("StartCommand returned error: %v", err)
	}
	result, err := manager.ReadCommandOutput(context.Background(), ReadRequest{
		SessionID: "session-1",
		ID:        "server-1",
		AfterSeq:  1,
	})
	if err != nil {
		t.Fatalf("ReadCommandOutput returned error: %v", err)
	}
	if len(result.Chunks) != 1 || result.Chunks[0].Seq != 2 || result.Chunks[0].Text != "two\n" {
		t.Fatalf("result = %#v, want only chunks after seq 1", result)
	}
	if result.NextSeq != 3 || result.Session.NextSeq != 3 {
		t.Fatalf("result next seq = %d session next seq = %d, want 3", result.NextSeq, result.Session.NextSeq)
	}
}

func TestScriptedSessionManagerReadAfterSeqSkipsStalePage(t *testing.T) {
	manager := NewScriptedSessionManager(ScriptedCommand{
		ID: "server-1",
		Pages: []ScriptedOutputPage{
			{
				Chunks: []OutputChunk{
					{Seq: 1, Stream: "stdout", Text: "old\n", Time: time.Unix(1, 0).UTC()},
				},
				Running: true,
			},
			{
				Chunks: []OutputChunk{
					{Seq: 3, Stream: "stdout", Text: "new\n", Time: time.Unix(3, 0).UTC()},
				},
				Running: true,
			},
		},
	})
	if _, err := manager.StartCommand(context.Background(), StartRequest{
		SessionID: "session-1",
		Argv:      []string{"npm", "run", "watch"},
	}); err != nil {
		t.Fatalf("StartCommand returned error: %v", err)
	}
	result, err := manager.ReadCommandOutput(context.Background(), ReadRequest{
		SessionID: "session-1",
		ID:        "server-1",
		AfterSeq:  2,
	})
	if err != nil {
		t.Fatalf("ReadCommandOutput returned error: %v", err)
	}
	if len(result.Chunks) != 1 || result.Chunks[0].Seq != 3 || result.Chunks[0].Text != "new\n" {
		t.Fatalf("result = %#v, want first newer chunk after stale page", result)
	}
	if result.NextSeq != 4 || result.Session.NextSeq != 4 {
		t.Fatalf("result next seq = %d session next seq = %d, want 4", result.NextSeq, result.Session.NextSeq)
	}
	next, err := manager.ReadCommandOutput(context.Background(), ReadRequest{
		SessionID: "session-1",
		ID:        "server-1",
		AfterSeq:  result.NextSeq - 1,
	})
	if err != nil {
		t.Fatalf("ReadCommandOutput next returned error: %v", err)
	}
	if len(next.Chunks) != 0 {
		t.Fatalf("next = %#v, want no remaining chunks after stale-page skip", next)
	}
}

func TestScriptedSessionManagerReadEmptyRunningPageWithoutAfterSeq(t *testing.T) {
	manager := NewScriptedSessionManager(ScriptedCommand{
		ID: "server-1",
		Pages: []ScriptedOutputPage{
			{Running: true},
			{
				Chunks: []OutputChunk{
					{Seq: 1, Stream: "stdout", Text: "later\n", Time: time.Unix(1, 0).UTC()},
				},
				Running: true,
			},
		},
	})
	if _, err := manager.StartCommand(context.Background(), StartRequest{
		SessionID: "session-1",
		Argv:      []string{"npm", "run", "watch"},
	}); err != nil {
		t.Fatalf("StartCommand returned error: %v", err)
	}
	first, err := manager.ReadCommandOutput(context.Background(), ReadRequest{
		SessionID: "session-1",
		ID:        "server-1",
	})
	if err != nil {
		t.Fatalf("ReadCommandOutput first returned error: %v", err)
	}
	if len(first.Chunks) != 0 || first.Session.Status != SessionRunning || first.NextSeq != 0 {
		t.Fatalf("first = %#v, want empty running page preserved on plain poll", first)
	}
	second, err := manager.ReadCommandOutput(context.Background(), ReadRequest{
		SessionID: "session-1",
		ID:        "server-1",
	})
	if err != nil {
		t.Fatalf("ReadCommandOutput second returned error: %v", err)
	}
	if len(second.Chunks) != 1 || second.Chunks[0].Text != "later\n" || second.NextSeq != 2 {
		t.Fatalf("second = %#v, want later chunk on second poll", second)
	}
}

func TestScriptedSessionManagerReadFilteredTerminalPageUpdatesStatus(t *testing.T) {
	manager := NewScriptedSessionManager(ScriptedCommand{
		ID: "server-1",
		Pages: []ScriptedOutputPage{
			{
				Chunks: []OutputChunk{
					{Seq: 1, Stream: "stdout", Text: "done\n", Time: time.Unix(1, 0).UTC()},
				},
				Running:  false,
				ExitCode: intPtr(0),
			},
		},
	})
	if _, err := manager.StartCommand(context.Background(), StartRequest{
		SessionID: "session-1",
		Argv:      []string{"npm", "run", "watch"},
	}); err != nil {
		t.Fatalf("StartCommand returned error: %v", err)
	}
	first, err := manager.ReadCommandOutput(context.Background(), ReadRequest{
		SessionID: "session-1",
		ID:        "server-1",
		AfterSeq:  5,
	})
	if err != nil {
		t.Fatalf("ReadCommandOutput first returned error: %v", err)
	}
	if len(first.Chunks) != 0 || first.Session.Status != SessionExited || first.Session.ExitCode == nil || *first.Session.ExitCode != 0 {
		t.Fatalf("first = %#v, want filtered terminal page to mark session exited", first)
	}
	if first.Session.FinishedAt == nil {
		t.Fatalf("first session finished_at = nil, want timestamp when terminal page exits")
	}
	second, err := manager.ReadCommandOutput(context.Background(), ReadRequest{
		SessionID: "session-1",
		ID:        "server-1",
		AfterSeq:  first.NextSeq,
	})
	if err != nil {
		t.Fatalf("ReadCommandOutput second returned error: %v", err)
	}
	if len(second.Chunks) != 0 || second.Session.Status != SessionExited {
		t.Fatalf("second = %#v, want exited status preserved after terminal page consumption", second)
	}
}

func TestScriptedSessionManagerReadAfterSeqSkipsToTerminalPage(t *testing.T) {
	manager := NewScriptedSessionManager(ScriptedCommand{
		ID: "server-1",
		Pages: []ScriptedOutputPage{
			{
				Chunks: []OutputChunk{
					{Seq: 1, Stream: "stdout", Text: "old\n", Time: time.Unix(1, 0).UTC()},
				},
				Running: true,
			},
			{
				Chunks: []OutputChunk{
					{Seq: 2, Stream: "stdout", Text: "done\n", Time: time.Unix(2, 0).UTC()},
				},
				Running:  false,
				ExitCode: intPtr(0),
			},
		},
	})
	if _, err := manager.StartCommand(context.Background(), StartRequest{
		SessionID: "session-1",
		Argv:      []string{"npm", "run", "watch"},
	}); err != nil {
		t.Fatalf("StartCommand returned error: %v", err)
	}
	result, err := manager.ReadCommandOutput(context.Background(), ReadRequest{
		SessionID: "session-1",
		ID:        "server-1",
		AfterSeq:  5,
	})
	if err != nil {
		t.Fatalf("ReadCommandOutput returned error: %v", err)
	}
	if len(result.Chunks) != 0 {
		t.Fatalf("result = %#v, want stale pages filtered during resumed read", result)
	}
	if result.Session.Status != SessionExited || result.Session.ExitCode == nil || *result.Session.ExitCode != 0 {
		t.Fatalf("result session = %#v, want terminal page to mark session exited after stale skip", result.Session)
	}
	if result.Session.FinishedAt == nil {
		t.Fatalf("result session finished_at = nil, want timestamp when stale-skip reaches terminal page")
	}
	next, err := manager.ReadCommandOutput(context.Background(), ReadRequest{
		SessionID: "session-1",
		ID:        "server-1",
		AfterSeq:  result.NextSeq,
	})
	if err != nil {
		t.Fatalf("ReadCommandOutput next returned error: %v", err)
	}
	if len(next.Chunks) != 0 || next.Session.Status != SessionExited {
		t.Fatalf("next = %#v, want exited status preserved after stale-terminal consumption", next)
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
		ID:   "dev-1",
		PID:  1234,
		TTY:  true,
		Cols: 120,
		Rows: 40,
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
	resizeTool := NewResizeTool(manager)
	stopTool := NewStopTool(manager)
	runtime := tool.Runtime{SessionID: "session-1"}
	if resizeTool.(tool.Definition).ToolSpec.Destructive {
		t.Fatalf("resize tool should not be destructive")
	}

	started, err := startTool.(tool.Definition).Handler(context.Background(), tool.Call{
		Runtime: runtime,
		Use: model.ToolUse{
			Name:  StartToolName,
			Input: json.RawMessage(`{"command":["npm","run","dev"],"purpose":"start dev server","tty":true,"cols":120,"rows":40}`),
		},
	})
	if err != nil {
		t.Fatalf("start handler returned error: %v", err)
	}
	if started.Metadata[MetadataCommandOperation] != "start" || started.Metadata[MetadataCommandSessionID] != "dev-1" || started.Metadata[MetadataCommandTTY] != true || started.Metadata[MetadataCommandCols] != 120 || started.Metadata[MetadataCommandRows] != 40 {
		t.Fatalf("started metadata = %#v, want command start metadata", started.Metadata)
	}
	if !strings.Contains(started.Content, "tty: true") || !strings.Contains(started.Content, "size: 120x40") {
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
	resized, err := resizeTool.(tool.Definition).Handler(context.Background(), tool.Call{
		Runtime: runtime,
		Use: model.ToolUse{
			Name:  ResizeToolName,
			Input: json.RawMessage(`{"id":"dev-1","cols":140,"rows":50}`),
		},
	})
	if err != nil {
		t.Fatalf("resize handler returned error: %v", err)
	}
	if resized.Metadata[MetadataCommandOperation] != "resize" || resized.Metadata[MetadataCommandCols] != 140 || resized.Metadata[MetadataCommandRows] != 50 {
		t.Fatalf("resize metadata = %#v, want resize operation with geometry", resized.Metadata)
	}
	if !strings.Contains(resized.Content, "size: 140x50") {
		t.Fatalf("resize content = %q, want updated geometry", resized.Content)
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
	if stopped.Metadata[MetadataCommandOperation] != "stop" || stopped.Metadata[MetadataCommandTTY] != true || stopped.Metadata[MetadataCommandCols] != 140 || stopped.Metadata[MetadataCommandRows] != 50 {
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

func TestStartRequestFromInputNormalizesTTYGeometry(t *testing.T) {
	req, err := startRequestFromInput(startInput{
		Command: []string{"python", "-i"},
		TTY:     true,
	})
	if err != nil {
		t.Fatalf("startRequestFromInput returned error: %v", err)
	}
	if req.Cols != defaultTTYCols || req.Rows != defaultTTYRows {
		t.Fatalf("request = %#v, want default tty geometry", req)
	}
}

func TestStartRequestFromInputRejectsInvalidTTYGeometry(t *testing.T) {
	cases := []startInput{
		{Command: []string{"python", "-i"}, TTY: true, Cols: 120},
		{Command: []string{"python", "-i"}, TTY: false, Cols: 120, Rows: 40},
		{Command: []string{"python", "-i"}, TTY: true, Cols: maxTTYDimension + 1, Rows: 40},
	}
	for _, input := range cases {
		if _, err := startRequestFromInput(input); err == nil {
			t.Fatalf("startRequestFromInput(%#v) returned nil error, want validation failure", input)
		}
	}
}

func TestResizeToolRejectsOversizedGeometry(t *testing.T) {
	manager := NewScriptedSessionManager(ScriptedCommand{ID: "dev-1", TTY: true, Cols: 80, Rows: 24})
	if _, err := manager.StartCommand(context.Background(), StartRequest{
		SessionID: "session-1",
		Argv:      []string{"python", "-i"},
		TTY:       true,
		Cols:      80,
		Rows:      24,
	}); err != nil {
		t.Fatalf("StartCommand returned error: %v", err)
	}
	toolDef := NewResizeTool(manager).(tool.Definition)
	_, err := toolDef.Handler(context.Background(), tool.Call{
		Runtime: tool.Runtime{SessionID: "session-1"},
		Use: model.ToolUse{
			Name:  ResizeToolName,
			Input: json.RawMessage(`{"id":"dev-1","cols":70000,"rows":40}`),
		},
	})
	if err == nil || !strings.Contains(err.Error(), "32767") {
		t.Fatalf("resize handler error = %v, want geometry limit failure", err)
	}
}

func TestScriptedSessionManagerRejectsInvalidTTYGeometry(t *testing.T) {
	cases := []struct {
		name string
		cmd  ScriptedCommand
		req  StartRequest
	}{
		{
			name: "scripted command oversized",
			cmd:  ScriptedCommand{ID: "dev-1", TTY: true, Cols: maxTTYDimension + 1, Rows: 24},
			req:  StartRequest{SessionID: "session-1", Argv: []string{"python", "-i"}},
		},
		{
			name: "request missing one dimension",
			cmd:  ScriptedCommand{ID: "dev-1"},
			req:  StartRequest{SessionID: "session-1", Argv: []string{"python", "-i"}, TTY: true, Cols: 120},
		},
	}
	for _, tc := range cases {
		manager := NewScriptedSessionManager(tc.cmd)
		if _, err := manager.StartCommand(context.Background(), tc.req); err == nil {
			t.Fatalf("%s: StartCommand returned nil error, want geometry validation failure", tc.name)
		}
	}
}

func TestSessionMetadataOmitsGeometryWithoutTTY(t *testing.T) {
	metadata := sessionMetadata(CommandSession{
		ID:        "cmd-1",
		Argv:      []string{"go", "test"},
		Status:    SessionRunning,
		StartedAt: time.Unix(1, 0).UTC(),
		NextSeq:   1,
	})
	if _, ok := metadata[MetadataCommandCols]; ok {
		t.Fatalf("metadata = %#v, want cols omitted without tty", metadata)
	}
	if _, ok := metadata[MetadataCommandRows]; ok {
		t.Fatalf("metadata = %#v, want rows omitted without tty", metadata)
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
		ResizeToolName,
	}
	if !sameStrings(names, want) {
		t.Fatalf("tool names = %#v, want %#v", names, want)
	}
}

func assertCommandSessionError(t testing.TB, err, kind error, want string) {
	t.Helper()
	if !errors.Is(err, kind) {
		t.Fatalf("error = %v, want %v", err, kind)
	}
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

func intPtr(v int) *int { return &v }
