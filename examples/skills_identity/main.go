package main

import (
	"context"
	"fmt"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/identity"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/skill"
)

func main() {
	events, err := memaxagent.Query(context.Background(), "Review the migration plan for database risk.", memaxagent.Options{
		Model: &scriptedModel{},
		Identity: identity.Identity{
			Name:     "Migration Reviewer",
			Role:     "database change reviewer",
			Mission:  "identify correctness, rollback, and operational risks before implementation",
			Autonomy: identity.AutonomyConservative,
		},
		Skills: []skill.Skill{
			{
				Name:        "database-review",
				Description: "Review schema and data migration plans.",
				WhenToUse:   "The task involves SQL, database migrations, indexes, or rollback plans.",
				Content:     "Check lock behavior, rollback path, data backfill safety, and observability before approving changes.",
			},
		},
	})
	if err != nil {
		panic(err)
	}

	result, err := memaxagent.Drain(events)
	if err != nil {
		panic(err)
	}
	fmt.Println(result)
}

type scriptedModel struct{}

func (m *scriptedModel) Stream(_ context.Context, req model.Request) (model.Stream, error) {
	return &scriptedStream{events: []model.StreamEvent{{
		Kind: model.StreamText,
		Text: fmt.Sprintf("prompt contains %d bytes of assembled guidance", len(req.SystemPrompt)),
	}}}, nil
}

type scriptedStream struct {
	events []model.StreamEvent
	index  int
}

func (s *scriptedStream) Recv() (model.StreamEvent, error) {
	if s.index >= len(s.events) {
		return model.StreamEvent{}, model.ErrEndOfStream
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

func (s *scriptedStream) Close() error {
	return nil
}
