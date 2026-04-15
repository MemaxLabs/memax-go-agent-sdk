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
	if !report.Passed() || len(report.Results) != 6 {
		t.Fatalf("report = %#v, want six passing scenarios", report)
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
		"session_resume",
		"context_retry",
		"subagent_delegation",
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
