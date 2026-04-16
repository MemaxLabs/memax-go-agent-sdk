package main

import (
	"context"
	"fmt"
	"log"
	"time"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/contextwindow"
	"github.com/MemaxLabs/memax-go-agent-sdk/permission"
	"github.com/MemaxLabs/memax-go-agent-sdk/providers/openai"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/filetools"
)

func main() {
	client := openai.NewFromEnv("", openai.WithTimeout(60*time.Second))
	if client.APIKey == "" || client.Model == "" {
		log.Fatal("set OPENAI_API_KEY and OPENAI_MODEL")
	}

	fs := filetools.NewMemoryFS(map[string]string{
		"README.md": "The live example uses memory-backed tools, not the host filesystem.",
	})
	registry := tool.NewRegistry(
		filetools.NewListTool(fs),
		filetools.NewReadTool(fs),
	)
	events, err := memaxagent.Query(context.Background(), "Inspect the workspace and summarize what you found.", memaxagent.Options{
		Model:       client,
		Tools:       registry,
		Permissions: permission.ReadOnly{},
		Context:     contextwindow.TokenBudget{MaxTokens: 16000},
		MaxTurns:    8,
	})
	if err != nil {
		log.Fatal(err)
	}
	printEvents(events)
}

func printEvents(events <-chan memaxagent.Event) {
	for event := range events {
		switch event.Kind {
		case memaxagent.EventToolUse:
			fmt.Printf("tool use: %s\n", event.ToolUse.Name)
		case memaxagent.EventToolResult:
			fmt.Printf("tool result: %s\n", event.ToolResult.Name)
		case memaxagent.EventAssistant:
			fmt.Print(event.Message.PlainText())
		case memaxagent.EventResult:
			fmt.Printf("\n\nresult: %s\n", event.Result)
		case memaxagent.EventError:
			log.Fatal(event.Err)
		}
	}
}
