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
	passed := 0
	for _, result := range report.Results {
		status := "PASS"
		if !result.Passed() {
			status = "FAIL"
		} else {
			passed++
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
	fmt.Printf("%d passed, %d failed\n", passed, len(report.Results)-passed)
	if err := report.Error(); err != nil {
		log.Fatal(err)
	}
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
