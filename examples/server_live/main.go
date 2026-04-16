package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/contextwindow"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/permission"
	"github.com/MemaxLabs/memax-go-agent-sdk/providers/anthropic"
	"github.com/MemaxLabs/memax-go-agent-sdk/providers/openai"
	"github.com/MemaxLabs/memax-go-agent-sdk/session"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/filetools"
)

func main() {
	client, err := modelClientFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	root := getenv("WORKSPACE_ROOT", ".")
	fs, err := filetools.NewOSFS(
		root,
		filetools.WithSymlinkContainment(true),
		filetools.WithMaxReadBytes(512*1024),
		filetools.WithMaxListEntries(5000),
	)
	if err != nil {
		log.Fatal(err)
	}

	registry := tool.NewRegistry(
		filetools.NewListTool(fs),
		filetools.NewReadTool(fs),
	)
	server := &agentServer{
		model:    client,
		sessions: session.NewJSONLStore(getenv("SESSION_DIR", ".memax-sessions")),
		tools:    registry,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/query", server.query)
	httpServer := &http.Server{
		Addr:              getenv("ADDR", ":8080"),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("serving workspace %q on %s", root, httpServer.Addr)
	log.Fatal(httpServer.ListenAndServe())
}

type agentServer struct {
	model    model.Client
	sessions session.Store
	tools    *tool.Registry
}

type queryRequest struct {
	Prompt    string `json:"prompt"`
	SessionID string `json:"session_id"`
}

type queryResponse struct {
	SessionID string `json:"session_id"`
	Result    string `json:"result"`
}

func (s *agentServer) query(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var input queryRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		http.Error(w, "invalid JSON request", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(input.Prompt) == "" {
		http.Error(w, "prompt is required", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	events := memaxagent.QueryAsync(ctx, input.Prompt, memaxagent.Options{
		Model:       s.model,
		Tools:       s.tools,
		Sessions:    s.sessions,
		SessionID:   input.SessionID,
		Permissions: permission.ReadOnly{},
		Context:     contextwindow.TokenBudget{MaxTokens: 24000},
		MaxTurns:    12,
	})

	var out queryResponse
	for event := range events {
		switch event.Kind {
		case memaxagent.EventSessionStarted:
			out.SessionID = event.SessionID
		case memaxagent.EventResult:
			out.Result = event.Result
		case memaxagent.EventError:
			http.Error(w, event.Err.Error(), http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func modelClientFromEnv() (model.Client, error) {
	switch strings.ToLower(os.Getenv("AGENT_PROVIDER")) {
	case "openai":
		client := openai.NewFromEnv("")
		if client.APIKey == "" || client.Model == "" {
			return nil, fmt.Errorf("set OPENAI_API_KEY and OPENAI_MODEL")
		}
		return client, nil
	case "anthropic":
		client := anthropic.NewFromEnv("")
		if client.APIKey == "" || client.Model == "" {
			return nil, fmt.Errorf("set ANTHROPIC_API_KEY and ANTHROPIC_MODEL")
		}
		return client, nil
	default:
		return nil, fmt.Errorf("set AGENT_PROVIDER to openai or anthropic")
	}
}

func getenv(name string, fallback string) string {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	return value
}
