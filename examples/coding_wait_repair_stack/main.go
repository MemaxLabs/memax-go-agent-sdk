package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/stack/coding"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/commandtools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/verifytools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/workspacetools"
	"github.com/MemaxLabs/memax-go-agent-sdk/workspace"
)

func main() {
	if err := runExample(context.Background(), os.Stdout); err != nil {
		log.Fatal(err)
	}
}

// runExample is a runnable walkthrough of the interactive_dev preset's
// managed-session repair loop. It adapts the background-monitor pattern from
// leading coding agents into explicit host-owned tools: start a watcher, wait
// for fresh output, patch through the workspace seam, wait again, stop the
// session, and verify before finalizing.
func runExample(ctx context.Context, w io.Writer) error {
	ws := workspace.NewMemoryStore(map[string]string{
		"README.md": "status: broken",
	})
	exitOK := 0
	// Scripted sessions make wait deterministic for examples: each wait advances
	// to the next page immediately, while the real adapters block until fresh
	// output, a lifecycle change, or timeout. Page 2 repeats seq=1 so after_seq
	// filtering must suppress already-seen output before returning seq=2.
	sessions := commandtools.NewScriptedSessionManager(commandtools.ScriptedCommand{
		ID:  "watch-1",
		PID: 5152,
		Pages: []commandtools.ScriptedOutputPage{
			{
				Chunks: []commandtools.OutputChunk{{
					Seq:    1,
					Stream: "stderr",
					Text:   "watch: README.md status must be fixed\n",
				}},
				Running: true,
			},
			{
				Chunks: []commandtools.OutputChunk{
					{
						Seq:    1,
						Stream: "stderr",
						Text:   "watch: README.md status must be fixed\n",
					},
					{
						Seq:    2,
						Stream: "stdout",
						Text:   "watch: ok\n",
					},
				},
				Running: true,
			},
		},
		StopExitCode: &exitOK,
	})

	config := coding.InteractiveDev()
	config.Workspace = ws
	config.CommandSessions = sessions
	config.Verifier.Verifier = verifytools.VerifierFunc(func(ctx context.Context, req verifytools.Request) (verifytools.Result, error) {
		content, err := ws.ReadFile(ctx, "README.md")
		if err != nil {
			return verifytools.Result{}, err
		}
		if content == "status: fixed" {
			return verifytools.Result{
				Name:   req.Name,
				Passed: true,
				Output: "README.md matched expected status.",
			}, nil
		}
		return verifytools.Result{
			Name:   req.Name,
			Passed: false,
			Output: "README.md still needs the fixed status",
		}, nil
	})

	stack, err := coding.New(config)
	if err != nil {
		return err
	}

	events, err := memaxagent.Query(ctx, "Use watch mode to repair README.md and finish.", stack.WithModel(&stackModel{}))
	if err != nil {
		return err
	}

	for event := range events {
		switch event.Kind {
		case memaxagent.EventToolUse:
			fmt.Fprintf(w, "tool use: %s\n", event.ToolUse.Name)
		case memaxagent.EventCommandStarted:
			fmt.Fprintf(w, "command started: %s status=%s\n", event.Command.CommandID, event.Command.Status)
		case memaxagent.EventCommandOutput:
			fmt.Fprintf(w, "command output: %s id=%s chunks=%d next_seq=%d status=%s\n", event.Command.Operation, event.Command.CommandID, event.Command.OutputChunks, event.Command.NextSeq, event.Command.Status)
		case memaxagent.EventCommandStopped:
			fmt.Fprintf(w, "command stopped: %s status=%s\n", event.Command.CommandID, event.Command.Status)
		case memaxagent.EventWorkspacePatch:
			fmt.Fprintf(w, "workspace patch: %s\n", strings.Join(event.Workspace.Paths, ","))
		case memaxagent.EventToolResult:
			fmt.Fprintf(w, "tool result: %s\n", event.ToolResult.Content)
		case memaxagent.EventResult:
			fmt.Fprintf(w, "result: %s\n", event.Result)
		case memaxagent.EventError:
			return event.Err
		}
	}
	return nil
}

type stackModel struct {
	turn int
}

func (m *stackModel) Stream(context.Context, model.Request) (model.Stream, error) {
	m.turn++
	switch m.turn {
	case 1:
		return newStream(toolUse("tool-1", commandtools.StartToolName, map[string]any{
			"id":      "watch-1",
			"command": []string{"npm", "run", "test:watch"},
			"purpose": "run the README status watcher",
		})), nil
	case 2:
		return newStream(toolUse("tool-2", commandtools.WaitOutputToolName, map[string]any{
			"id":         "watch-1",
			"timeout_ms": 1000,
			"limit":      10,
		})), nil
	case 3:
		return newStream(toolUse("tool-3", workspacetools.CheckpointToolName, map[string]any{
			"label": "before wait-driven README repair",
		})), nil
	case 4:
		return newStream(toolUse("tool-4", workspacetools.ApplyPatchToolName, patchInput())), nil
	case 5:
		return newStream(toolUse("tool-5", commandtools.WaitOutputToolName, map[string]any{
			"id":         "watch-1",
			"after_seq":  1,
			"timeout_ms": 1000,
			"limit":      10,
		})), nil
	case 6:
		return newStream(toolUse("tool-6", commandtools.StopToolName, map[string]any{
			"id":    "watch-1",
			"force": true,
		})), nil
	case 7:
		return newStream(toolUse("tool-7", verifytools.ToolName, map[string]any{
			"name": "test",
		})), nil
	default:
		return newStream(model.StreamEvent{
			Kind: model.StreamText,
			Text: "Watch mode passed after wait-driven repair.",
		}), nil
	}
}

func patchInput() map[string]any {
	return map[string]any{
		"operations": []map[string]any{{
			"path":        "README.md",
			"old_content": "status: broken",
			"new_content": "status: fixed",
		}},
	}
}

func toolUse(id string, name string, input map[string]any) model.StreamEvent {
	return model.StreamEvent{
		Kind: model.StreamToolUse,
		ToolUse: model.ToolUse{
			ID:    id,
			Name:  name,
			Input: mustJSON(input),
		},
	}
}

func mustJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}

type stream struct {
	events []model.StreamEvent
	index  int
}

func newStream(events ...model.StreamEvent) *stream {
	return &stream{events: events}
}

func (s *stream) Recv() (model.StreamEvent, error) {
	if s.index >= len(s.events) {
		return model.StreamEvent{}, model.ErrEndOfStream
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

func (s *stream) Close() error {
	return nil
}
