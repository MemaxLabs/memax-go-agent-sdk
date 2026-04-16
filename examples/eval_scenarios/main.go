package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/MemaxLabs/memax-go-agent-sdk/agenteval"
	"github.com/MemaxLabs/memax-go-agent-sdk/agenteval/scenarios"
)

func main() {
	report := agenteval.Runner{}.Run(context.Background(), scenarios.All()...)
	for _, result := range report.Results {
		status := "PASS"
		if !result.Passed() {
			status = "FAIL"
		}
		fmt.Printf(
			"%s %s events=%d tools=%s duration=%s result=%q\n",
			status,
			result.Name,
			len(result.Events),
			toolNames(result),
			result.Duration,
			result.Final,
		)
	}
	if err := report.Error(); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%d passed, 0 failed\n", len(report.Results))
}

func toolNames(result agenteval.Result) string {
	uses := result.ToolUses()
	if len(uses) == 0 {
		return "-"
	}
	names := make([]string, 0, len(uses))
	for _, use := range uses {
		names = append(names, use.Name)
	}
	return strings.Join(names, ",")
}
