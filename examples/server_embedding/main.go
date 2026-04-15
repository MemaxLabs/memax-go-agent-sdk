package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/session"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/filetools"
)

func main() {
	sessions := session.NewMemoryStore()
	fs := filetools.NewMemoryFS(map[string]string{
		"README.md": "This server embeds the SDK behind an HTTP endpoint.",
	})
	registry := tool.NewRegistry(
		filetools.NewListTool(fs),
		filetools.NewReadTool(fs),
		filetools.NewWriteTool(fs),
	)
	server := &agentServer{
		sessions: sessions,
		tools:    registry,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/query", server.query)
	httpServer := &http.Server{
		Addr:              ":8080",
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Println("listening on http://localhost:8080")
	log.Println(`try: curl -s localhost:8080/query -d '{"prompt":"inspect workspace"}'`)
	log.Fatal(httpServer.ListenAndServe())
}

type agentServer struct {
	sessions session.Store
	tools    *tool.Registry
}

type queryRequest struct {
	Prompt    string `json:"prompt"`
	SessionID string `json:"session_id"`
}

type queryResponse struct {
	SessionID string   `json:"session_id"`
	Result    string   `json:"result"`
	Events    []string `json:"events"`
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
	if input.Prompt == "" {
		http.Error(w, "prompt is required", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	events, err := memaxagent.Query(ctx, input.Prompt, memaxagent.Options{
		Model:     &scriptedModel{},
		Tools:     s.tools,
		Sessions:  s.sessions,
		SessionID: input.SessionID,
		MaxTurns:  4,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var out queryResponse
	for event := range events {
		switch event.Kind {
		case memaxagent.EventSessionStarted:
			out.SessionID = event.SessionID
		case memaxagent.EventToolUse:
			out.Events = append(out.Events, "tool_use:"+event.ToolUse.Name)
		case memaxagent.EventToolResult:
			out.Events = append(out.Events, "tool_result:"+event.ToolResult.Name)
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

type scriptedModel struct {
	turn int
}

func (m *scriptedModel) Stream(context.Context, model.Request) (model.Stream, error) {
	m.turn++
	switch m.turn {
	case 1:
		return newStream(model.StreamEvent{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "tool-1",
				Name:  filetools.ListToolName,
				Input: json.RawMessage(`{}`),
			},
		}), nil
	default:
		return newStream(model.StreamEvent{
			Kind: model.StreamText,
			Text: "Server run completed with application-owned tools.",
		}), nil
	}
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
