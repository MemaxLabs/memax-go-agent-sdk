package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/MemaxLabs/memax-go-agent-sdk/providers/anthropic"
	"github.com/MemaxLabs/memax-go-agent-sdk/providers/openai"
	"github.com/MemaxLabs/memax-go-agent-sdk/stack/coding"
)

func main() {
	if err := runExample(os.Args[1:], os.Stdout); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		log.Fatal(err)
	}
}

// runExample demonstrates the CLI/config boundary a coding-agent product can
// build on: parse a stable profile name, then map it to concrete provider
// adapter options. It does not call a live provider.
func runExample(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("coding_model_profiles", flag.ContinueOnError)
	fs.SetOutput(w)
	provider := fs.String("provider", "openai", "provider mapping to show: openai or anthropic")
	profileRaw := fs.String("profile", "", "coding depth profile: fast, balanced, or deep")
	if err := fs.Parse(args); err != nil {
		return err
	}

	profile, err := coding.ParseModelProfile(*profileRaw)
	if err != nil {
		return err
	}
	providerName := strings.ToLower(strings.TrimSpace(*provider))

	fmt.Fprintf(w, "provider: %s\n", providerName)
	fmt.Fprintf(w, "profile: %s\n", profile)
	fmt.Fprintf(w, "description: %s\n", profile.Description())

	switch providerName {
	case "openai":
		opts, err := coding.OpenAIModelOptions(profile)
		if err != nil {
			return err
		}
		client := openai.New("example-key", "example-model", opts...)
		if client.Reasoning != nil {
			fmt.Fprintf(w, "openai reasoning effort: %s\n", client.Reasoning.Effort)
		}
		if client.Text != nil {
			fmt.Fprintf(w, "openai text verbosity: %s\n", client.Text.Verbosity)
		}
		fmt.Fprintf(w, "openai include: %s\n", strings.Join(client.Include, ","))
	case "anthropic":
		opts, err := coding.AnthropicModelOptions(profile)
		if err != nil {
			return err
		}
		client := anthropic.New("example-key", "example-model", opts...)
		fmt.Fprintf(w, "anthropic effort: %s\n", client.Effort)
		if client.Thinking != nil {
			fmt.Fprintf(w, "anthropic thinking: %s\n", client.Thinking.Type)
		}
	default:
		return fmt.Errorf("unknown provider %q", *provider)
	}
	return nil
}
