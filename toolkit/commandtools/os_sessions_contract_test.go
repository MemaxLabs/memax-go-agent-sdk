package commandtools_test

import (
	"bufio"
	"os"
	"testing"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/commandtools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/commandtools/sessiontest"
)

func TestOSSessionManagerContract(t *testing.T) {
	sessiontest.Run(t, sessiontest.Contract{
		NewManager: func(t testing.TB, _ sessiontest.Scenario) commandtools.SessionManager {
			t.Helper()
			manager, err := commandtools.NewOSSessionManager(
				t.TempDir(),
				commandtools.WithOSSessionManagerStopGrace(25*time.Millisecond),
				commandtools.WithOSSessionManagerDrainTimeout(25*time.Millisecond),
			)
			if err != nil {
				t.Fatalf("NewOSSessionManager returned error: %v", err)
			}
			return manager
		},
		NaturalExitRequest: func(testing.TB) commandtools.StartRequest {
			return contractHelperRequest("ready-then-finish", "100ms")
		},
		LongRunningRequest: func(testing.TB) commandtools.StartRequest {
			return contractHelperRequest("linger")
		},
		InteractiveRequest: func(testing.TB) commandtools.StartRequest {
			return contractHelperRequest("echo-stdin")
		},
		TTYRequest: func(testing.TB) commandtools.StartRequest {
			req := contractHelperRequest("linger")
			req.TTY = true
			req.Cols = 90
			req.Rows = 30
			return req
		},
	})
}

func contractHelperRequest(args ...string) commandtools.StartRequest {
	argv := []string{os.Args[0], "-test.run=TestOSSessionManagerContractHelperProcess", "--", "session"}
	argv = append(argv, args...)
	return commandtools.StartRequest{
		Argv:    argv,
		Env:     contractHelperEnv(),
		Timeout: 10 * time.Second,
	}
}

func contractHelperEnv() map[string]string {
	env := map[string]string{"GO_WANT_COMMANDTOOLS_CONTRACT_HELPER_PROCESS": "1"}
	if value := os.Getenv("SystemRoot"); value != "" {
		env["SystemRoot"] = value
	}
	if value := os.Getenv("PATH"); value != "" {
		env["PATH"] = value
	}
	return env
}

func TestOSSessionManagerContractHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_COMMANDTOOLS_CONTRACT_HELPER_PROCESS") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) < 3 || args[1] != "session" {
		os.Exit(2)
	}
	switch args[2] {
	case "ready-then-finish":
		delay := 100 * time.Millisecond
		if len(args) >= 4 {
			parsed, err := time.ParseDuration(args[3])
			if err != nil {
				os.Exit(2)
			}
			delay = parsed
		}
		_, _ = os.Stdout.WriteString("ready\n")
		time.Sleep(delay)
		_, _ = os.Stdout.WriteString("done\n")
	case "linger":
		_, _ = os.Stdout.WriteString("ready\n")
		time.Sleep(30 * time.Second)
	case "echo-stdin":
		_, _ = os.Stdout.WriteString("ready\n")
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "exit" {
				_, _ = os.Stdout.WriteString("bye\n")
				os.Exit(0)
			}
			_, _ = os.Stdout.WriteString("echo:" + line + "\n")
		}
		if err := scanner.Err(); err != nil {
			_, _ = os.Stderr.WriteString(err.Error())
			os.Exit(1)
		}
	default:
		os.Exit(2)
	}
}
