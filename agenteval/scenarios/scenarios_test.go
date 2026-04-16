package scenarios

import (
	"context"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/agenteval"
)

func TestScenariosPass(t *testing.T) {
	report := agenteval.Runner{}.Run(context.Background(), All()...)
	if err := report.Error(); err != nil {
		t.Fatalf("scenario report error = %v", err)
	}
	if !report.Passed() || len(report.Results) != 30 {
		t.Fatalf("report = %#v, want thirty passing scenarios", report)
	}
}

func TestScenarioNamesAreStable(t *testing.T) {
	cases := All()
	got := make([]string, len(cases))
	for i, c := range cases {
		got[i] = c.Name
	}
	want := []string{
		"tool_recovery",
		"structured_output_repair",
		"memory_search_and_save",
		"memory_distillation_candidates",
		"memory_candidate_handler_writes",
		"session_resume",
		"context_retry",
		"subagent_delegation",
		"planner_guided_tool_use",
		"planner_task_state_updates",
		"progressive_skill_disclosure",
		"progressive_skill_resource_loading",
		"context_preserves_loaded_skill",
		"context_compaction_provenance",
		"openai_provider_text_and_usage",
		"anthropic_provider_text_and_usage",
		"openai_provider_tool_use_round_trip",
		"anthropic_provider_tool_use_round_trip",
		"permission_denial_recovery",
		"hook_denial_recovery",
		"large_result_storage_recovery",
		"budget_stops_before_second_model_call",
		"budget_stops_before_tool_batch",
		"budget_stops_after_token_usage",
		"deferred_tool_discovery_recovery",
		"streaming_safe_tool_overlap",
		"streaming_mutating_tool_waits",
		"streaming_permission_denial_recovery",
		"streaming_failure_cancels_early_tool",
		"streaming_cancellation",
	}
	if len(got) != len(want) {
		t.Fatalf("scenario names = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("scenario names = %#v, want %#v", got, want)
		}
	}
}
