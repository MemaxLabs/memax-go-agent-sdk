package sessiontest

import (
	"testing"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/commandtools"
)

func TestRunScriptedSessionManagerContract(t *testing.T) {
	Run(t, Contract{
		NewManager: func(t testing.TB, scenario Scenario) commandtools.SessionManager {
			t.Helper()
			switch scenario {
			case ScenarioNaturalExit:
				return commandtools.NewScriptedSessionManager(scriptedNaturalExitCommand())
			case ScenarioStopCleanup:
				return commandtools.NewScriptedSessionManager(scriptedLongRunningCommand("server-1"), scriptedLongRunningCommand("server-2"))
			case ScenarioWriteInput:
				return commandtools.NewScriptedSessionManager(scriptedInteractiveCommand())
			case ScenarioResizeTTY:
				return commandtools.NewScriptedSessionManager(commandtools.ScriptedCommand{
					ID:   "tty-1",
					PID:  4244,
					TTY:  true,
					Cols: 90,
					Rows: 30,
				})
			default:
				t.Fatalf("unexpected scenario %q", scenario)
				return nil
			}
		},
		NaturalExitRequest: func(testing.TB) commandtools.StartRequest {
			return commandtools.StartRequest{Argv: []string{"scripted", "natural"}}
		},
		LongRunningRequest: func(testing.TB) commandtools.StartRequest {
			return commandtools.StartRequest{Argv: []string{"scripted", "linger"}}
		},
		InteractiveRequest: func(testing.TB) commandtools.StartRequest {
			return commandtools.StartRequest{Argv: []string{"scripted", "interactive"}}
		},
		TTYRequest: func(testing.TB) commandtools.StartRequest {
			return commandtools.StartRequest{
				Argv: []string{"scripted", "tty"},
				TTY:  true,
				Cols: 90,
				Rows: 30,
			}
		},
	})
}

func scriptedNaturalExitCommand() commandtools.ScriptedCommand {
	return commandtools.ScriptedCommand{
		ID:  "natural-1",
		PID: 4242,
		Pages: []commandtools.ScriptedOutputPage{
			{
				Chunks:  []commandtools.OutputChunk{{Seq: 1, Stream: "stdout", Text: "ready\n", Time: time.Unix(1, 0).UTC()}},
				Running: true,
			},
			{
				Chunks:   []commandtools.OutputChunk{{Seq: 2, Stream: "stdout", Text: "done\n", Time: time.Unix(2, 0).UTC()}},
				Running:  false,
				ExitCode: intPtr(0),
			},
		},
	}
}

func scriptedLongRunningCommand(id string) commandtools.ScriptedCommand {
	return commandtools.ScriptedCommand{
		ID:  id,
		PID: 4243,
		Pages: []commandtools.ScriptedOutputPage{{
			Chunks:  []commandtools.OutputChunk{{Seq: 1, Stream: "stdout", Text: "ready\n", Time: time.Unix(1, 0).UTC()}},
			Running: true,
		}},
		StopExitCode: intPtr(143),
	}
}

func scriptedInteractiveCommand() commandtools.ScriptedCommand {
	return commandtools.ScriptedCommand{
		ID:  "interactive-1",
		PID: 4245,
		Pages: []commandtools.ScriptedOutputPage{{
			Chunks:  []commandtools.OutputChunk{{Seq: 1, Stream: "stdout", Text: "ready\n", Time: time.Unix(1, 0).UTC()}},
			Running: true,
		}},
		WritePages: []commandtools.ScriptedWritePage{
			{
				Page: commandtools.ScriptedOutputPage{
					Chunks:  []commandtools.OutputChunk{{Seq: 2, Stream: "stdout", Text: "echo:hello\n", Time: time.Unix(2, 0).UTC()}},
					Running: true,
				},
			},
			{
				Page: commandtools.ScriptedOutputPage{
					Chunks:   []commandtools.OutputChunk{{Seq: 3, Stream: "stdout", Text: "bye\n", Time: time.Unix(3, 0).UTC()}},
					Running:  false,
					ExitCode: intPtr(0),
				},
			},
		},
	}
}

func intPtr(value int) *int {
	return &value
}
