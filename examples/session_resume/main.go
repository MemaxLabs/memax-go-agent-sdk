package main

import (
	"context"
	"fmt"
	"log"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/session"
)

func main() {
	ctx := context.Background()
	sessions := session.NewMemoryStore()

	firstSessionID, firstResult := run(ctx, sessions, "", "Remember that the workspace is in memory.")
	fmt.Printf("first session: %s\n", firstSessionID)
	fmt.Printf("first result: %s\n", firstResult)

	secondSessionID, secondResult := run(ctx, sessions, firstSessionID, "Continue from the previous request.")
	fmt.Printf("resumed session: %s\n", secondSessionID)
	fmt.Printf("second result: %s\n", secondResult)
}

func run(ctx context.Context, sessions session.Store, sessionID string, prompt string) (string, string) {
	modelClient := &countingModel{}
	events, err := memaxagent.Query(ctx, prompt, memaxagent.Options{
		Model:     modelClient,
		Sessions:  sessions,
		SessionID: sessionID,
	})
	if err != nil {
		log.Fatal(err)
	}

	var started string
	var result string
	for event := range events {
		switch event.Kind {
		case memaxagent.EventSessionStarted:
			started = event.SessionID
		case memaxagent.EventResult:
			result = event.Result
		case memaxagent.EventError:
			log.Fatal(event.Err)
		}
	}
	return started, result
}

type countingModel struct{}

func (m *countingModel) Stream(_ context.Context, req model.Request) (model.Stream, error) {
	text := fmt.Sprintf("model received %d transcript messages", len(req.Messages))
	return &stream{events: []model.StreamEvent{{Kind: model.StreamText, Text: text}}}, nil
}

type stream struct {
	events []model.StreamEvent
	index  int
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
