package memory

import (
	"context"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/planner"
)

func TestStaticDistillerReturnsDefensiveCopies(t *testing.T) {
	distiller := StaticDistiller{{
		Memory: Memory{
			Name:     "rollback",
			Scope:    ScopeProject,
			Content:  "Rollbacks require notes.",
			Tags:     []string{"billing"},
			Metadata: map[string]any{"source": "test"},
		},
		Reason:     "final answer mentioned rollback",
		Confidence: 0.9,
	}}

	first, err := distiller.Distill(context.Background(), DistillRequest{})
	if err != nil {
		t.Fatalf("Distill returned error: %v", err)
	}
	first[0].Memory.Tags[0] = "mutated"
	first[0].Memory.Metadata["source"] = "mutated"

	second, err := distiller.Distill(context.Background(), DistillRequest{})
	if err != nil {
		t.Fatalf("Distill returned error: %v", err)
	}
	if second[0].Memory.Tags[0] != "billing" || second[0].Memory.Metadata["source"] != "test" {
		t.Fatalf("candidates = %#v, want defensive copies", second)
	}
}

func TestRuleDistillerMatchesResultAndPlan(t *testing.T) {
	distiller := RuleDistiller{{
		WhenResultContains: "rollback",
		WhenPlanContains:   "migration",
		Memory: Memory{
			Name:    "migration-rollback",
			Scope:   ScopeProject,
			Content: "Migration reviews require rollback notes.",
		},
		Reason:     "captured review policy",
		Confidence: 0.8,
	}}

	candidates, err := distiller.Distill(context.Background(), DistillRequest{
		Result: "Add rollback notes before merging.",
		Plan: planner.Plan{Goal: "review migration", Steps: []planner.Step{{
			Title: "check rollback",
		}}},
	})
	if err != nil {
		t.Fatalf("Distill returned error: %v", err)
	}
	if len(candidates) != 1 || candidates[0].Memory.Name != "migration-rollback" {
		t.Fatalf("candidates = %#v, want matching memory", candidates)
	}
}

func TestRuleDistillerSkipsNonMatches(t *testing.T) {
	distiller := RuleDistiller{{
		WhenResultContains: "rollback",
		Memory:             Memory{Name: "rollback", Content: "notes"},
	}}
	candidates, err := distiller.Distill(context.Background(), DistillRequest{Result: "No lasting lesson."})
	if err != nil {
		t.Fatalf("Distill returned error: %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("candidates = %#v, want none", candidates)
	}
}

func TestWriterHandlerWritesAcceptedCandidates(t *testing.T) {
	store := NewMemoryStore(nil)
	handler := WriterHandler{
		Writer:        store,
		MinConfidence: 0.7,
		Scopes:        []Scope{ScopeProject},
	}

	err := handler.HandleCandidates(context.Background(), CandidateRequest{
		SessionID:       "session-1",
		ParentSessionID: "parent-1",
		Candidates: []Candidate{
			{
				Memory:     Memory{Name: "keep", Scope: ScopeProject, Content: "Keep this lesson."},
				Confidence: 0.9,
			},
			{
				Memory:     Memory{Name: "low", Scope: ScopeProject, Content: "Too uncertain."},
				Confidence: 0.2,
			},
			{
				Memory:     Memory{Name: "user", Scope: ScopeUser, Content: "Wrong scope."},
				Confidence: 0.9,
			},
		},
	})
	if err != nil {
		t.Fatalf("HandleCandidates returned error: %v", err)
	}
	items, err := store.Memories(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Memories returned error: %v", err)
	}
	if len(items) != 1 || items[0].Name != "keep" {
		t.Fatalf("stored memories = %#v, want only accepted candidate", items)
	}
}

func TestCandidateHandlerFuncNilIsNoop(t *testing.T) {
	var handler CandidateHandlerFunc
	if err := handler.HandleCandidates(context.Background(), CandidateRequest{}); err != nil {
		t.Fatalf("HandleCandidates returned error: %v", err)
	}
}
