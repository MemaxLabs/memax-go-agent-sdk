package budget

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

func TestPolicyZeroValueAllows(t *testing.T) {
	decision := (Policy{}).Check(context.Background(), Snapshot{
		Turns:      100,
		ModelCalls: 100,
		ToolCalls:  100,
		Usage:      model.Usage{InputTokens: 100, OutputTokens: 100, TotalTokens: 200},
	})
	if !decision.Allow {
		t.Fatalf("decision = %#v, want allow", decision)
	}
}

func TestPolicyRejectsExceededLimit(t *testing.T) {
	decision := (Policy{MaxToolCalls: 2}).Check(context.Background(), Snapshot{ToolCalls: 3})
	if decision.Allow || !strings.Contains(decision.Reason, "max tool calls") {
		t.Fatalf("decision = %#v, want max tool calls denial", decision)
	}
}

func TestPolicyChecksTokenUsage(t *testing.T) {
	decision := (Policy{MaxTotalTokens: 10}).Check(context.Background(), Snapshot{
		Usage: model.Usage{InputTokens: 6, OutputTokens: 5, TotalTokens: 11},
	})
	if decision.Allow || !strings.Contains(decision.Reason, "max total tokens") {
		t.Fatalf("decision = %#v, want total token denial", decision)
	}
}

func TestPolicyChecksDuration(t *testing.T) {
	started := time.Now().Add(-2 * time.Second)
	decision := (Policy{MaxDuration: time.Second}).Check(context.Background(), Snapshot{StartedAt: started})
	if decision.Allow || !strings.Contains(decision.Reason, "max duration") {
		t.Fatalf("decision = %#v, want duration denial", decision)
	}
}

func TestGovernorFuncNilAllows(t *testing.T) {
	if decision := (GovernorFunc(nil)).Check(context.Background(), Snapshot{}); !decision.Allow {
		t.Fatalf("decision = %#v, want allow", decision)
	}
}
