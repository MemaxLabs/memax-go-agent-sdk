package main

import (
	"bytes"
	"errors"
	"flag"
	"strings"
	"testing"
)

func TestRunExampleDefaultsToBalancedOpenAI(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	if err := runExample(nil, &out); err != nil {
		t.Fatalf("runExample() error = %v", err)
	}
	output := out.String()
	for _, want := range []string{
		"provider: openai",
		"profile: balanced",
		"openai reasoning effort: medium",
		"openai text verbosity: medium",
		"openai include: reasoning.encrypted_content",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}

func TestRunExampleShowsAnthropicDeep(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	if err := runExample([]string{"-provider", "anthropic", "-profile", "deep"}, &out); err != nil {
		t.Fatalf("runExample() error = %v", err)
	}
	output := out.String()
	for _, want := range []string{
		"provider: anthropic",
		"profile: deep",
		"anthropic effort: xhigh",
		"anthropic thinking: adaptive",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}

func TestRunExampleRejectsUnknownProvider(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	if err := runExample([]string{"-provider", "local"}, &out); err == nil || !strings.Contains(err.Error(), `unknown provider "local"`) {
		t.Fatalf("runExample() error = %v, want unknown provider", err)
	}
}

func TestRunExampleHelpWritesUsage(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	err := runExample([]string{"-help"}, &out)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("runExample() error = %v, want ErrHelp", err)
	}
	output := out.String()
	for _, want := range []string{
		"Usage of coding_model_profiles:",
		"-profile",
		"-provider",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("help output missing %q:\n%s", want, output)
		}
	}
}
